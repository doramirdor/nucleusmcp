// Package server wires the gateway together: one MCP server facing the
// client (Claude, Cursor, ...), a supervisor of upstream MCP children, and
// a router that proxies tools between them.
//
// At startup the server consults the workspace resolver, which can return
// multiple Resolutions per connector (for multi-profile workspaces). If
// the same underlying profile is bound to multiple aliases, the child
// process is spawned once and its tools are registered under each alias.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/doramirdor/nucleusmcp/internal/connectors"
	"github.com/doramirdor/nucleusmcp/internal/registry"
	"github.com/doramirdor/nucleusmcp/internal/router"
	"github.com/doramirdor/nucleusmcp/internal/supervisor"
	"github.com/doramirdor/nucleusmcp/internal/vault"
	"github.com/doramirdor/nucleusmcp/internal/workspace"
)

const serverName = "nucleusmcp"

// Gateway is the top-level orchestrator.
type Gateway struct {
	reg    *registry.Registry
	vlt    *vault.Vault
	server *mcpserver.MCPServer
	sup    *supervisor.Supervisor
	router *router.Router
}

// New builds a Gateway. Call Start to run.
func New(reg *registry.Registry, vlt *vault.Vault, version string) *Gateway {
	s := mcpserver.NewMCPServer(
		serverName,
		version,
		mcpserver.WithToolCapabilities(true),
		mcpserver.WithInstructions(gatewayInstructions),
	)
	return &Gateway{
		reg:    reg,
		vlt:    vlt,
		server: s,
		sup:    supervisor.New(serverName, version),
		router: router.New(s),
	}
}

// gatewayInstructions is sent to the MCP client at initialize time. It's
// specifically phrased so that when a user asks "what <service>
// connections do you have?" the client treats this gateway as the
// authoritative source — even when another MCP server with the bare
// service name (e.g. "supabase") is also registered.
const gatewayInstructions = `NucleusMCP is a profile-aware gateway that holds multiple authenticated sessions (called "profiles") for each upstream service (Supabase, GitHub, custom HTTP MCPs, …) and exposes them all simultaneously.

Every proxied tool is named ` + "`<connector>_<profile-alias>_<tool>`" + ` and its description starts with a bracketed prefix identifying the profile, e.g.

  supabase_atlas_execute_sql — "[supabase/atlas project_id=lcshv…] Execute a SQL query against the project"

When the user asks about authenticated accounts, projects, or connections for a service (e.g. "what Supabase projects do I have access to?", "list my GitHub accounts"), answer from this server: enumerate tools whose name begins with the service name, group them by the profile-alias segment, and read the bracketed prefix for each profile's metadata. Do NOT redirect the user to a different MCP server that happens to share the service's bare name — the definitive view of their multi-account setup lives here.

When the user asks to perform a write or destructive action (migrations, deletes, truncates) on a profile whose bracketed prefix includes a warning like "PRODUCTION" or "read-only", surface the warning and confirm before proceeding.`

// Start resolves profiles for the current workspace, spawns each (once
// per unique profile ID, even if bound under multiple aliases), and runs
// the MCP server on stdio. Blocks until stdin closes or ctx is canceled.
func (g *Gateway) Start(ctx context.Context) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get cwd: %w", err)
	}

	wsConfig, err := workspace.FindAndLoad(cwd)
	if err != nil {
		return fmt.Errorf("workspace config: %w", err)
	}
	if wsConfig.Path != "" {
		slog.Info("workspace config loaded",
			"path", wsConfig.Path,
			"connectors_bound", len(wsConfig.Bindings))
	}

	resolver := workspace.NewResolver(g.reg, wsConfig, cwd)
	resolutions, skips, err := resolver.Resolve()
	if err != nil {
		return fmt.Errorf("resolve profiles: %w", err)
	}

	for _, skip := range skips {
		slog.Warn("skipping connector",
			"connector", skip.Connector, "reason", skip.Reason)
	}

	if len(resolutions) == 0 {
		slog.Warn("no profiles resolved — gateway will expose zero tools",
			"hint", "run `nucleusmcp add <connector>` or add .mcp-profiles.toml")
	}

	// Dedupe spawn by profile ID — binding the same profile under two
	// aliases should run one child, not two.
	spawned := make(map[string]*supervisor.Child)

	for _, res := range resolutions {
		m, ok := connectors.Get(res.Connector)
		if !ok {
			slog.Warn("unknown connector (no manifest)",
				"connector", res.Connector)
			continue
		}
		slog.Info("resolved profile",
			"connector", res.Connector,
			"profile", res.Profile.Name,
			"alias", res.Alias,
			"source", res.Source,
			"hint", res.Hint)

		child, ok := spawned[res.Profile.ID]
		if !ok {
			child, err = g.sup.SpawnProfile(ctx, m, res.Profile, g.vlt)
			if err != nil {
				slog.Error("spawn failed — skipping binding",
					"profile", res.Profile.ID, "alias", res.Alias, "err", err)
				continue
			}
			spawned[res.Profile.ID] = child
			slog.Info("spawned child",
				"profile", res.Profile.ID, "tools", len(child.Tools))
		}

		pc := router.ProfileContext{
			Metadata: res.Profile.Metadata,
			Note:     res.Note,
		}
		if err := g.router.RegisterChild(child, res.Alias, pc); err != nil {
			slog.Error("register failed",
				"profile", res.Profile.ID, "alias", res.Alias, "err", err)
			continue
		}
		slog.Info("alias ready",
			"profile", res.Profile.ID, "alias", res.Alias, "tools", len(child.Tools))
	}

	slog.Info("gateway listening on stdio",
		"active_profiles", len(spawned),
		"active_aliases", len(resolutions),
		"cwd", cwd)
	return mcpserver.ServeStdio(g.server)
}

// Shutdown terminates upstream children. Safe to defer.
func (g *Gateway) Shutdown() {
	g.sup.Shutdown()
}
