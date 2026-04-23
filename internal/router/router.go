// Package router exposes upstream MCP tools to the gateway's own MCP server
// with a namespaced name, and proxies CallTool through to the appropriate
// child.
//
// Naming convention: "<connector>_<alias>_<tool>".
//
//	alias = profile name when no alias is set in .mcp-profiles.toml
//
// Every proxied tool also gets a short prefix injected into its
// description, so an MCP client (e.g. Claude) reading tool metadata
// knows which profile each tool is scoped to without needing a separate
// CLAUDE.md. The prefix has the shape:
//
//	[supabase/atlas project_id=xxx] Execute a SQL query against the project
//
// The proxy itself is transparent: input schemas, descriptions (after
// prefix), and result payloads pass through unchanged.
package router

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/doramirdor/nucleusmcp/internal/supervisor"
)

// Router registers proxied tools on a gateway MCP server.
type Router struct {
	s *mcpserver.MCPServer
}

// New creates a router bound to the given MCP server.
func New(s *mcpserver.MCPServer) *Router {
	return &Router{s: s}
}

// ProfileContext carries profile metadata down from the server so the
// router can splice it into tool descriptions.
type ProfileContext struct {
	// Metadata is the stored profile metadata (e.g. project_id=xxx).
	Metadata map[string]string
	// Note is free-form text the user set (e.g. "PROD — writes require
	// confirmation"). Optional.
	Note string
}

// RegisterChild advertises every tool from the child under a namespaced
// name, using the given alias. A short context prefix is injected into
// each tool's description so clients understand which profile owns it.
//
// Passing "" for alias means "use the profile name as the alias" —
// preserves pre-alias behavior.
func (r *Router) RegisterChild(c *supervisor.Child, alias string, pc ProfileContext) error {
	if alias == "" {
		alias = c.Profile
	}
	prefix := buildDescriptionPrefix(c.Connector, alias, pc)
	for _, t := range c.Tools {
		proxied := t
		proxied.Name = NamespacedName(c.Connector, alias, t.Name)
		proxied.Description = prependDescription(prefix, proxied.Description)
		r.s.AddTool(proxied, r.makeHandler(c, t.Name))
	}
	return nil
}

// NamespacedName is the public name for a proxied tool.
func NamespacedName(connector, alias, tool string) string {
	return connector + "_" + alias + "_" + tool
}

// buildDescriptionPrefix renders the profile context into a compact
// bracketed prefix, e.g.
//
//	[supabase/atlas project_id=lcshv...]
//	[supabase/atlas project_id=lcshv...] PROD — writes require confirmation:
func buildDescriptionPrefix(connector, alias string, pc ProfileContext) string {
	parts := []string{connector + "/" + alias}
	keys := make([]string, 0, len(pc.Metadata))
	for k := range pc.Metadata {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, k+"="+pc.Metadata[k])
	}
	prefix := "[" + strings.Join(parts, " ") + "]"
	if pc.Note != "" {
		prefix += " " + pc.Note + " —"
	}
	return prefix
}

// prependDescription glues the prefix onto the original upstream
// description. If the upstream shipped no description, just the prefix
// is enough — it tells the client at least which profile owns the tool.
func prependDescription(prefix, original string) string {
	original = strings.TrimSpace(original)
	if original == "" {
		return prefix
	}
	return prefix + " " + original
}

// makeHandler returns a ToolHandler that forwards CallTool to the upstream
// child, preserving arguments and remapping the tool name back to its
// upstream form.
func (r *Router) makeHandler(c *supervisor.Child, upstreamTool string) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		upstream := req
		upstream.Params.Name = upstreamTool

		res, err := c.Client.CallTool(ctx, upstream)
		if err != nil {
			return nil, fmt.Errorf("proxy %s/%s: %w", c.ProfileID, upstreamTool, err)
		}
		return res, nil
	}
}
