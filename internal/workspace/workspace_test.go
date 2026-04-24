package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindAndLoad_BothForms(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		wantKeys []string
		// wantBindings maps connector -> expected (profile, alias, note)
		// tuples for the bindings in that connector, in order.
		wantBindings map[string][]Binding
	}{
		{
			name: "simple table form",
			content: `
[supabase]
profile = "acme-prod"
`,
			wantKeys: []string{"supabase"},
			wantBindings: map[string][]Binding{
				"supabase": {{Profile: "acme-prod", Alias: "acme-prod"}},
			},
		},
		{
			name: "array-of-tables multi with aliases and notes",
			content: `
[[supabase]]
profile = "acme-prod"
alias   = "prod"
note    = "PRODUCTION"

[[supabase]]
profile = "acme-staging"
alias   = "staging"
`,
			wantKeys: []string{"supabase"},
			wantBindings: map[string][]Binding{
				"supabase": {
					{Profile: "acme-prod", Alias: "prod", Note: "PRODUCTION"},
					{Profile: "acme-staging", Alias: "staging"},
				},
			},
		},
		{
			name: "alias defaults to profile when omitted",
			content: `
[[supabase]]
profile = "only"
`,
			wantKeys: []string{"supabase"},
			wantBindings: map[string][]Binding{
				"supabase": {{Profile: "only", Alias: "only"}},
			},
		},
		{
			name: "multiple connectors mixed",
			content: `
[supabase]
profile = "atlas"

[github]
profile = "work"
`,
			wantKeys: []string{"supabase", "github"},
			wantBindings: map[string][]Binding{
				"supabase": {{Profile: "atlas", Alias: "atlas"}},
				"github":   {{Profile: "work", Alias: "work"}},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeConfig(t, dir, tc.content)

			got, err := FindAndLoad(dir)
			if err != nil {
				t.Fatalf("FindAndLoad: %v", err)
			}
			for _, k := range tc.wantKeys {
				if _, ok := got.Bindings[k]; !ok {
					t.Errorf("expected connector %q in Bindings, got %v", k, got.Bindings)
				}
			}
			for connector, want := range tc.wantBindings {
				gotBs := got.Bindings[connector]
				if len(gotBs) != len(want) {
					t.Fatalf("%s: len=%d, want %d (got %v)", connector, len(gotBs), len(want), gotBs)
				}
				for i, w := range want {
					if gotBs[i] != w {
						t.Errorf("%s[%d] = %+v, want %+v", connector, i, gotBs[i], w)
					}
				}
			}
		})
	}
}

func TestFindAndLoad_MissingFile(t *testing.T) {
	// No .mcp-profiles.toml anywhere — expect empty config, no error.
	dir := t.TempDir()
	got, err := FindAndLoad(dir)
	if err != nil {
		t.Fatalf("FindAndLoad: %v", err)
	}
	if got.Path != "" {
		t.Errorf("Path = %q, want empty", got.Path)
	}
	if len(got.Bindings) != 0 {
		t.Errorf("Bindings = %v, want empty", got.Bindings)
	}
}

func TestFindAndLoad_DuplicateAliasRejected(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
[[supabase]]
profile = "a"
alias   = "dup"
[[supabase]]
profile = "b"
alias   = "dup"
`)
	if _, err := FindAndLoad(dir); err == nil {
		t.Fatal("expected duplicate-alias error, got nil")
	}
}

func TestFindAndLoad_AncestorWalk(t *testing.T) {
	// .mcp-profiles.toml in the parent should be discovered from a
	// child directory.
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, root, `
[supabase]
profile = "ancestor-match"
`)
	got, err := FindAndLoad(child)
	if err != nil {
		t.Fatalf("FindAndLoad: %v", err)
	}
	if got.Path == "" {
		t.Fatal("expected to discover ancestor config, got no Path")
	}
	b, ok := got.Bindings["supabase"]
	if !ok || len(b) != 1 || b[0].Profile != "ancestor-match" {
		t.Errorf("Bindings = %v, want supabase=[{ancestor-match …}]", got.Bindings)
	}
}

// writeConfig drops `content` at <dir>/.mcp-profiles.toml.
func writeConfig(t *testing.T, dir, content string) {
	t.Helper()
	path := filepath.Join(dir, ConfigFile)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
