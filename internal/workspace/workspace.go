// Package workspace discovers workspace-scoped configuration and resolves
// which profile(s) to use for each connector.
//
// The gateway runs as a subprocess of the MCP client (Claude, Cursor, ...).
// At startup the cwd is the user's workspace — commonly the root of a
// repo. From there we:
//
//  1. Walk up the directory tree looking for .mcp-profiles.toml
//     (explicit binding).
//  2. For each connector without an explicit binding, try the manifest's
//     autodetect rules, also via ancestor walk.
//  3. Fall back to the single-profile implicit default or a user-set
//     default.
package workspace

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// ConfigFile is the filename the gateway looks for in the workspace tree.
const ConfigFile = ".mcp-profiles.toml"

// Config is the parsed form of .mcp-profiles.toml.
//
// Single-profile form:
//
//	[supabase]
//	profile = "acme-prod"
//
// Multi-profile form (aliases):
//
//	[[supabase]]
//	profile = "acme-prod"
//	alias   = "prod"
//	[[supabase]]
//	profile = "acme-staging"
//	alias   = "staging"
type Config struct {
	// Path is the absolute path to the file this config was parsed from.
	// Empty if no file was found.
	Path string

	// Bindings maps connector name -> list of bindings.
	// A connector with no binding is absent from the map.
	Bindings map[string][]Binding
}

// Binding is one profile selection for a connector.
type Binding struct {
	// Profile is the profile name (not the full ID).
	Profile string

	// Alias is the segment used in tool names after the connector prefix.
	// Defaults to Profile when unset.
	Alias string

	// Note is a free-form human message (e.g. "PROD — writes require
	// confirmation") that the gateway splices into the description of
	// every proxied tool so an MCP client like Claude picks up the
	// context. Optional.
	Note string
}

// FindAndLoad walks up from startDir looking for ConfigFile and parses it
// if found. Missing file is not an error — returns empty Config.
func FindAndLoad(startDir string) (*Config, error) {
	path, err := findAncestor(startDir, ConfigFile)
	if err != nil {
		return nil, err
	}
	if path == "" {
		return &Config{Bindings: map[string][]Binding{}}, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	// The TOML tree is parsed generically so we can accept both a single
	// [connector] table and a [[connector]] array of tables under the
	// same key shape. go-toml exposes tables as map[string]any and
	// arrays of tables as []any whose elements are map[string]any.
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	bindings := make(map[string][]Binding, len(raw))
	for connector, v := range raw {
		bs, err := decodeBindings(v)
		if err != nil {
			return nil, fmt.Errorf("%s: connector %q: %w", path, connector, err)
		}
		if err := validateAliases(bs); err != nil {
			return nil, fmt.Errorf("%s: connector %q: %w", path, connector, err)
		}
		bindings[connector] = bs
	}
	return &Config{Path: path, Bindings: bindings}, nil
}

// decodeBindings accepts either a single table or an array of tables and
// returns a list of Binding.
func decodeBindings(v any) ([]Binding, error) {
	switch vv := v.(type) {
	case map[string]any:
		b, err := decodeBinding(vv)
		if err != nil {
			return nil, err
		}
		return []Binding{b}, nil
	case []any:
		out := make([]Binding, 0, len(vv))
		for i, item := range vv {
			m, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("entry %d: expected table, got %T", i, item)
			}
			b, err := decodeBinding(m)
			if err != nil {
				return nil, fmt.Errorf("entry %d: %w", i, err)
			}
			out = append(out, b)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("expected table or array of tables, got %T", v)
	}
}

func decodeBinding(m map[string]any) (Binding, error) {
	var b Binding
	if p, ok := m["profile"]; ok {
		s, ok := p.(string)
		if !ok {
			return Binding{}, fmt.Errorf("`profile` must be a string, got %T", p)
		}
		b.Profile = s
	}
	if a, ok := m["alias"]; ok {
		s, ok := a.(string)
		if !ok {
			return Binding{}, fmt.Errorf("`alias` must be a string, got %T", a)
		}
		b.Alias = s
	}
	if n, ok := m["note"]; ok {
		s, ok := n.(string)
		if !ok {
			return Binding{}, fmt.Errorf("`note` must be a string, got %T", n)
		}
		b.Note = s
	}
	if b.Profile == "" {
		return Binding{}, errors.New("`profile` is required")
	}
	if b.Alias == "" {
		b.Alias = b.Profile
	}
	return b, nil
}

// validateAliases rejects duplicate aliases within a single connector's
// bindings — two identical aliases would collide on tool names.
func validateAliases(bs []Binding) error {
	seen := make(map[string]struct{}, len(bs))
	for _, b := range bs {
		if _, dup := seen[b.Alias]; dup {
			return fmt.Errorf("alias %q used more than once", b.Alias)
		}
		seen[b.Alias] = struct{}{}
	}
	return nil
}

// findAncestor walks from startDir up toward the root and $HOME looking
// for a file named `name`. Returns the first hit or "".
func findAncestor(startDir, name string) (string, error) {
	abs, err := filepath.Abs(startDir)
	if err != nil {
		return "", fmt.Errorf("absolute path: %w", err)
	}
	home := normalizedHome()

	dir := abs
	for {
		candidate := filepath.Join(dir, name)
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() {
			return candidate, nil
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		if home != "" && dir == home {
			return "", nil
		}
		dir = parent
	}
}

// FindFileInAncestors walks up looking for any of the given relative paths
// and returns (absolute, relative, nil) for the first hit.
func FindFileInAncestors(startDir string, relPaths []string) (string, string, error) {
	abs, err := filepath.Abs(startDir)
	if err != nil {
		return "", "", err
	}
	home := normalizedHome()

	dir := abs
	for {
		for _, rel := range relPaths {
			candidate := filepath.Join(dir, rel)
			info, err := os.Stat(candidate)
			if err == nil && !info.IsDir() {
				return candidate, rel, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", nil
		}
		if home != "" && dir == home {
			return "", "", nil
		}
		dir = parent
	}
}

func normalizedHome() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		return ""
	}
	if h, err := filepath.Abs(home); err == nil {
		return h
	}
	return home
}
