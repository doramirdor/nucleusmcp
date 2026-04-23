# Changelog

All notable changes follow [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html) (starting at 0.1.0).

## [Unreleased]

### Added
- Gateway with stdio MCP server, per-profile credential injection, and transparent tool proxy
- Profile registry in SQLite at `~/.nucleusmcp/registry.db` with schema migrations
- OS keychain–backed credential vault (macOS Keychain, Linux libsecret, Windows Credential Manager)
- Workspace resolution: `.mcp-profiles.toml` (explicit bindings), autodetect via manifest rules, user-set defaults, and an expose-all fallback
- Multi-profile-per-connector aliases with dedup spawn (same profile under two aliases reuses one child process)
- HTTP / OAuth connectors bridged via `mcp-remote` with per-profile isolated auth directories
- Post-OAuth resource discovery: picker lists projects from the upstream after auth (Supabase)
- Tool description prefix: proxied tools carry `[connector/alias metadata]` and optional user note so MCP clients read profile context natively
- Custom connector support: `nucleusmcp add <name> --transport http <url>` saves a manifest under `~/.nucleusmcp/connectors/`
- Built-in connectors: Supabase (OAuth) and GitHub (PAT)
- CLI: `add`, `remove`, `list`, `info`, `use`, `connectors`, `install`, `serve`

### Known gaps
- Eager spawn at startup — no idle reaper yet
- No mid-session cwd-change hot-swap
- No audit log CLI surface
- `mcp-remote` dependency for HTTP connectors (native OAuth is roadmapped)
