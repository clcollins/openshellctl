package gatewayconfig

import (
	"encoding/json"
	"io/fs"
	"path"
)

// VaultAuthConfig is the Vault KV coordinates (mount, path, field prefix) a
// user supplied via --vault-kv-mount/--vault-kv-path/--vault-field-prefix (or
// their OPENSHELL_VAULT_* env equivalents), persisted to
// gateways/<name>/vault_auth.json alongside last_sandbox and oidc_token.json
// so a later command against the same gateway doesn't have to repeat them
// (ROSAENG-74343).
//
// Only the coordinates are ever written here — never the Vault secret itself
// or any field read from it (client id, client secret, ...). That is the
// deliberate answer to "how does persistence interact with Vault token/
// session expiry": there is nothing here that can go stale in a way that
// matters, because nothing secret or session-bound is stored. A command run
// against a persisted mount/path still performs a real Vault KV read using
// whatever Vault session (VAULT_TOKEN / ~/.vault-token) is live *at that
// moment* — exactly as if --vault-kv-mount/--vault-kv-path had been typed by
// hand. If the Vault session has since expired, that read fails the same way
// it always has (vaultconfig.ErrNoToken, ErrForbidden, ...); the persisted
// file cannot mask that, since it only tells the CLI where to look, not what
// it will find.
//
// Two known limitations, both inherited from the exact same pattern
// last_sandbox (lastsandbox.go) already has, not introduced by this type:
//
//   - System-tree gateways: SaveVaultAuthConfig always writes through a
//     Writer rooted at the USER config tree, but LoadVaultAuthConfig reads
//     from a Resolved's FS, which for a gateway found only in the system
//     tree is rooted there instead. The two trees aren't reconciled, so a
//     persisted file for a system-tree-only gateway is effectively
//     write-only (never read back). last_sandbox has this identical gap
//     today; fixing it for either would mean writing into the system tree,
//     which is a bigger, separate change this feature doesn't attempt.
//   - Ambient OPENSHELL_VAULT_* env vars get persisted too, not just flags:
//     if a shell still has gateway A's OPENSHELL_VAULT_KV_MOUNT/PATH
//     exported and the user runs `gateway add`/`login` for gateway B, B
//     silently inherits A's coordinates. This matches the documented
//     "flag or env, same precedence" contract, but is easy to trigger by
//     accident when switching between gateways in one shell session.
type VaultAuthConfig struct {
	Mount       string `json:"mount"`
	Path        string `json:"path"`
	FieldPrefix string `json:"field_prefix,omitempty"`
}

// vaultAuthRel is the path (relative to the user config root) of a gateway's
// persisted Vault auth-source coordinates.
func vaultAuthRel(gatewayName string) string {
	return path.Join(GatewayDir(gatewayName), "vault_auth.json")
}

// LoadVaultAuthConfig reads gateways/<name>/vault_auth.json from the resolved
// gateway's sub-FS. ok is false when the file is absent, empty, unparseable,
// or missing either Mount or Path — callers treat all of those as "nothing
// usable persisted" and fall through to flags/env/no Vault source, the same
// way LoadLastSandbox tolerates a missing or legacy file rather than erroring
// every later command against the gateway.
func LoadVaultAuthConfig(r *Resolved) (VaultAuthConfig, bool) {
	if r == nil || r.FS == nil {
		return VaultAuthConfig{}, false
	}
	b, err := fs.ReadFile(r.FS, "vault_auth.json")
	if err != nil {
		return VaultAuthConfig{}, false
	}
	var cfg VaultAuthConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return VaultAuthConfig{}, false
	}
	if cfg.Mount == "" || cfg.Path == "" {
		return VaultAuthConfig{}, false
	}
	return cfg, true
}

// SaveVaultAuthConfig writes cfg as gateways/<name>/vault_auth.json to the
// USER tree (w is responsible for the atomic write, 0600 file, 0700 parent
// dir — see Writer/OSWriter), so later commands against the same gateway
// reuse the mount/path/prefix without repeating the flags/env vars.
//
// Validates gatewayName the same way every other mutator in this package
// does (SetActive, WriteGateway, RemoveGateway, Logout) — ROSAENG-74343
// review flagged this as the one mutator that didn't, which matters here
// specifically because vaultAuthRel builds a path from gatewayName.
func SaveVaultAuthConfig(w Writer, gatewayName string, cfg VaultAuthConfig) error {
	if err := ValidateGatewayName(gatewayName); err != nil {
		return err
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return w.WriteFile(vaultAuthRel(gatewayName), data, 0o600)
}
