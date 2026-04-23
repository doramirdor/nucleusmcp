// Package vault stores per-profile credentials in the OS keychain.
//
// Each credential is keyed by (profileID, credKey) and flattened to an
// account string of the form "profile:<profileID>:<credKey>" under the
// service name "nucleusmcp". This keeps everything under one keychain
// service and makes manual inspection possible.
//
// Linux requires libsecret (GNOME Keyring / KWallet) via D-Bus; headless
// environments will fail — an encrypted file fallback is on the M5 menu.
package vault

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zalando/go-keyring"
)

const service = "nucleusmcp"

// ErrNotFound is returned when a credential does not exist.
var ErrNotFound = errors.New("credential not found")

// Vault is a thin wrapper over the OS keychain. It has no state; the zero
// value is usable. It's a struct (not free functions) so tests can inject
// an in-memory mock through an interface if needed later.
type Vault struct{}

// New returns a Vault backed by the OS keychain.
func New() *Vault { return &Vault{} }

// Set stores value under (profileID, credKey). Overwrites on conflict.
func (v *Vault) Set(profileID, credKey, value string) error {
	return keyring.Set(service, accountKey(profileID, credKey), value)
}

// Get retrieves the credential or returns ErrNotFound.
func (v *Vault) Get(profileID, credKey string) (string, error) {
	s, err := keyring.Get(service, accountKey(profileID, credKey))
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("vault get %s/%s: %w", profileID, credKey, err)
	}
	return s, nil
}

// Delete removes the credential. Returns nil if already absent.
func (v *Vault) Delete(profileID, credKey string) error {
	err := keyring.Delete(service, accountKey(profileID, credKey))
	if err != nil && errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

// DeleteProfile removes every credential for a profile. Callers pass the
// list of cred keys (usually derived from the manifest).
func (v *Vault) DeleteProfile(profileID string, credKeys []string) error {
	var firstErr error
	for _, k := range credKeys {
		if err := v.Delete(profileID, k); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func accountKey(profileID, credKey string) string {
	return "profile:" + profileID + ":" + credKey
}

// AuthDir returns the per-profile OAuth directory under
// ~/.nucleusmcp/oauth/<safe-id>/ and ensures it exists with 0700 perms.
//
// Used by HTTP connectors: the supervisor passes this path to the
// mcp-remote bridge via MCP_REMOTE_CONFIG_DIR so each profile keeps its
// own OAuth tokens separate from every other profile's.
//
// The profile ID "<connector>:<name>" has its colon replaced with '_' to
// stay filesystem-safe on every platform.
func (v *Vault) AuthDir(profileID string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	safe := strings.ReplaceAll(profileID, ":", "_")
	dir := filepath.Join(home, ".nucleusmcp", "oauth", safe)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create auth dir: %w", err)
	}
	return dir, nil
}

// DeleteAuthDir removes the OAuth directory for a profile. No-op if the
// directory is already gone.
func (v *Vault) DeleteAuthDir(profileID string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("home dir: %w", err)
	}
	safe := strings.ReplaceAll(profileID, ":", "_")
	dir := filepath.Join(home, ".nucleusmcp", "oauth", safe)
	return os.RemoveAll(dir)
}
