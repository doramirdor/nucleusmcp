package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/doramirdor/nucleus/internal/config"
	"github.com/doramirdor/nucleus/internal/server"
	"github.com/spf13/cobra"
)

func newServeCmd() *cobra.Command {
	var (
		httpAddr  string
		httpToken string
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the gateway as an MCP server (stdio by default, --http for daemon mode)",
		Long: `Run the gateway.

Without flags, serves the MCP protocol over stdio — this is the mode
invoked by Claude Code / Cursor via their mcp server config (what
` + "`nucleus install`" + ` wires up).

With --http, the gateway runs as a long-lived HTTP server on a local
port. The endpoint is /mcp. Use this mode to register Nucleus in
Claude's UI "Add custom connector" dialog, which only accepts HTTP(S)
URLs.

Safety defaults for --http: loopback-only (127.0.0.1) unless you supply
--token, which activates bearer-token auth and allows non-loopback
binds.

Examples:
  nucleus serve                                  # stdio, standard MCP client flow
  nucleus serve --http 127.0.0.1:8787            # listens on that loopback addr
  nucleus serve --http :9000                     # listens on 127.0.0.1 (all IPv4 loopback) :9000
  nucleus serve --http 0.0.0.0:9000 --token s3cret   # LAN-reachable with bearer auth`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Logs to stderr — stdout is reserved for MCP protocol frames
			// in stdio mode.
			slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr,
				&slog.HandlerOptions{Level: slog.LevelInfo})))

			path, err := resolveConfigPath()
			if err != nil {
				return err
			}
			if _, err := config.Load(path); err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			reg, err := openRegistry()
			if err != nil {
				return fmt.Errorf("open registry: %w", err)
			}
			defer reg.Close()

			ctx, cancel := signal.NotifyContext(
				context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			gw := server.New(reg, newVault(), version)
			defer gw.Shutdown()

			slog.Info("nucleus starting", "version", version, "config", path)

			// Fail fast on misconfigured HTTP flags *before* Prepare
			// spawns upstream children (which can take several seconds
			// on first run per connector). A non-empty --http value
			// means HTTP mode.
			httpMode := httpAddr != ""
			httpOpts := server.HTTPOptions{Addr: httpAddr, Token: httpToken}
			if httpMode {
				if err := httpOpts.Validate(); err != nil {
					return err
				}
			}

			if err := gw.Prepare(ctx); err != nil {
				return err
			}

			if httpMode {
				return gw.ServeHTTP(ctx, httpOpts)
			}
			return gw.ServeStdio()
		},
	}

	cmd.Flags().StringVar(&httpAddr, "http", "",
		"serve over streamable HTTP on this bind address, e.g. '127.0.0.1:8787' or ':9000'. "+
			"Non-loopback binds require --token.")
	cmd.Flags().StringVar(&httpToken, "token", "",
		"require this bearer token on every HTTP request (required for non-loopback binds)")
	return cmd
}

func resolveConfigPath() (string, error) {
	if configPath != "" {
		return configPath, nil
	}
	return config.DefaultPath()
}
