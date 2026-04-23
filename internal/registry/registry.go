// Package registry persists profiles (connector + name + metadata) in a
// local SQLite database. Credentials live in the vault, not here.
//
// Schema is versioned in the `meta` table and advanced by an ordered list
// of migrations applied at Open() time.
package registry

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go sqlite driver, no CGO
)

// Profile is a single authenticated connection to a connector.
type Profile struct {
	ID        string            // "<connector>:<name>"
	Connector string            // manifest name, e.g. "supabase"
	Name      string            // user-supplied, e.g. "acme-prod"
	Metadata  map[string]string // arbitrary; used by resolver autodetect
	CreatedAt time.Time
	IsDefault bool // if true, resolver uses this when no other rule fires
}

// Errors.
var (
	ErrNotFound  = errors.New("profile not found")
	ErrDuplicate = errors.New("profile already exists")
	ErrInvalid   = errors.New("invalid profile")
)

// Registry is a SQLite-backed profile store.
type Registry struct {
	db *sql.DB
}

// migrations is the ordered list of DDL applied to advance schema_version.
// Never reorder or edit historical entries — only append.
var migrations = []string{
	// v1 — initial profile table. IF NOT EXISTS so upgraders from M2
	// (which created this table outside the migrations system) are
	// handled cleanly — v1 becomes a no-op for them and v2 is applied.
	`CREATE TABLE IF NOT EXISTS profiles (
        id         TEXT PRIMARY KEY,
        connector  TEXT NOT NULL,
        name       TEXT NOT NULL,
        metadata   TEXT NOT NULL DEFAULT '{}',
        created_at INTEGER NOT NULL,
        UNIQUE(connector, name)
    );
    CREATE INDEX IF NOT EXISTS idx_profiles_connector ON profiles(connector);`,

	// v2 — is_default per-connector default flag
	`ALTER TABLE profiles ADD COLUMN is_default INTEGER NOT NULL DEFAULT 0;`,
}

// DefaultPath is ~/.nucleusmcp/registry.db.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".nucleusmcp", "registry.db"), nil
}

// Open creates or opens the registry at path, applying any pending
// migrations. Parent directory is created with 0700 if missing.
func Open(path string) (*Registry, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create dir: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	_, _ = db.Exec("PRAGMA journal_mode=WAL;")
	_, _ = db.Exec("PRAGMA foreign_keys=ON;")

	if err := applyMigrations(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply migrations: %w", err)
	}
	return &Registry{db: db}, nil
}

func applyMigrations(db *sql.DB) error {
	if _, err := db.Exec(
		`CREATE TABLE IF NOT EXISTS meta (
            key TEXT PRIMARY KEY,
            val TEXT NOT NULL
        );`,
	); err != nil {
		return fmt.Errorf("create meta table: %w", err)
	}

	var curStr string
	err := db.QueryRow(`SELECT val FROM meta WHERE key = 'schema_version'`).Scan(&curStr)
	cur := 0
	if err == nil {
		_, _ = fmt.Sscanf(curStr, "%d", &cur)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read schema_version: %w", err)
	}

	for i := cur; i < len(migrations); i++ {
		// Each migration runs in its own transaction for atomicity.
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration v%d: %w", i+1, err)
		}
		if _, err := tx.Exec(
			`INSERT INTO meta (key, val) VALUES ('schema_version', ?)
             ON CONFLICT(key) DO UPDATE SET val = excluded.val`,
			fmt.Sprintf("%d", i+1),
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("bump schema_version to v%d: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit v%d: %w", i+1, err)
		}
	}
	return nil
}

// Close releases resources.
func (r *Registry) Close() error { return r.db.Close() }

// MakeID returns the canonical profile ID for a connector/name pair.
func MakeID(connector, name string) string {
	return connector + ":" + name
}

// ValidateName enforces name charset — lowercase alnum plus '-' and '_'.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: name is empty", ErrInvalid)
	}
	for _, r := range name {
		ok := (r >= 'a' && r <= 'z') ||
			(r >= '0' && r <= '9') ||
			r == '-' || r == '_'
		if !ok {
			return fmt.Errorf("%w: name %q has invalid char %q (allowed: a-z 0-9 - _)",
				ErrInvalid, name, r)
		}
	}
	return nil
}

// Create inserts a new profile. Returns ErrDuplicate if (connector, name)
// already exists.
func (r *Registry) Create(p Profile) error {
	if err := ValidateName(p.Name); err != nil {
		return err
	}
	if p.Connector == "" {
		return fmt.Errorf("%w: connector is empty", ErrInvalid)
	}
	if p.ID == "" {
		p.ID = MakeID(p.Connector, p.Name)
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now()
	}
	if p.Metadata == nil {
		p.Metadata = map[string]string{}
	}
	metaJSON, err := json.Marshal(p.Metadata)
	if err != nil {
		return fmt.Errorf("encode metadata: %w", err)
	}

	_, err = r.db.Exec(
		`INSERT INTO profiles (id, connector, name, metadata, created_at, is_default)
         VALUES (?, ?, ?, ?, ?, ?)`,
		p.ID, p.Connector, p.Name, string(metaJSON), p.CreatedAt.Unix(),
		boolToInt(p.IsDefault),
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return fmt.Errorf("%w: %s/%s", ErrDuplicate, p.Connector, p.Name)
		}
		return fmt.Errorf("insert profile: %w", err)
	}
	return nil
}

// Get fetches a profile by ID.
func (r *Registry) Get(id string) (Profile, error) {
	row := r.db.QueryRow(
		`SELECT id, connector, name, metadata, created_at, is_default
         FROM profiles WHERE id = ?`, id)
	return scanProfile(row.Scan)
}

// List returns all profiles sorted by connector then name.
func (r *Registry) List() ([]Profile, error) {
	rows, err := r.db.Query(
		`SELECT id, connector, name, metadata, created_at, is_default
         FROM profiles ORDER BY connector, name`)
	if err != nil {
		return nil, fmt.Errorf("query profiles: %w", err)
	}
	defer rows.Close()
	return scanProfiles(rows)
}

// ListByConnector returns profiles for a single connector.
func (r *Registry) ListByConnector(connector string) ([]Profile, error) {
	rows, err := r.db.Query(
		`SELECT id, connector, name, metadata, created_at, is_default
         FROM profiles WHERE connector = ? ORDER BY name`,
		connector)
	if err != nil {
		return nil, fmt.Errorf("query profiles: %w", err)
	}
	defer rows.Close()
	return scanProfiles(rows)
}

// Delete removes a profile by ID.
func (r *Registry) Delete(id string) error {
	res, err := r.db.Exec(`DELETE FROM profiles WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete profile: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetDefault marks a profile as the default for its connector, clearing
// the flag on any other profile of the same connector. Transactional.
func (r *Registry) SetDefault(id string) error {
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	var connector string
	err = tx.QueryRow(`SELECT connector FROM profiles WHERE id = ?`, id).Scan(&connector)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("lookup connector: %w", err)
	}

	if _, err := tx.Exec(
		`UPDATE profiles SET is_default = 0 WHERE connector = ?`, connector); err != nil {
		return fmt.Errorf("clear defaults: %w", err)
	}
	if _, err := tx.Exec(
		`UPDATE profiles SET is_default = 1 WHERE id = ?`, id); err != nil {
		return fmt.Errorf("set default: %w", err)
	}
	return tx.Commit()
}

// GetDefault returns the default profile for a connector, or ErrNotFound.
func (r *Registry) GetDefault(connector string) (Profile, error) {
	row := r.db.QueryRow(
		`SELECT id, connector, name, metadata, created_at, is_default
         FROM profiles WHERE connector = ? AND is_default = 1 LIMIT 1`,
		connector)
	return scanProfile(row.Scan)
}

// ── scan helpers ────────────────────────────────────────────────────────

type scanFn func(...any) error

func scanProfile(scan scanFn) (Profile, error) {
	var (
		p        Profile
		meta     string
		ts       int64
		isDef    int
	)
	if err := scan(&p.ID, &p.Connector, &p.Name, &meta, &ts, &isDef); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Profile{}, ErrNotFound
		}
		return Profile{}, fmt.Errorf("scan profile: %w", err)
	}
	p.CreatedAt = time.Unix(ts, 0)
	p.IsDefault = isDef != 0
	if err := json.Unmarshal([]byte(meta), &p.Metadata); err != nil {
		return Profile{}, fmt.Errorf("decode metadata for %s: %w", p.ID, err)
	}
	if p.Metadata == nil {
		p.Metadata = map[string]string{}
	}
	return p, nil
}

func scanProfiles(rows *sql.Rows) ([]Profile, error) {
	var out []Profile
	for rows.Next() {
		p, err := scanProfile(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
