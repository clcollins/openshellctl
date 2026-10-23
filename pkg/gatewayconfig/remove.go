package gatewayconfig

import "strings"

// activeGatewayRel is the path (relative to the user config root) of the
// active_gateway pointer file — config-root level, not under gateways/ (same
// path readActive reads in config.go).
const activeGatewayRel = "active_gateway"

// knownGatewayFiles are the files RemoveGateway removes: only the ones
// openshellctl itself writes and can recreate from scratch (metadata.json,
// oidc_token.json, vault_auth.json). last_sandbox and mtls/{ca.crt,tls.crt,
// tls.key} are deliberately NOT here: a last_sandbox pointer is harmless to
// leave behind, but mTLS material is typically admin-issued and not
// recoverable by this CLI — removing a registration (e.g. to fix a typo'd
// endpoint and re-add it) must never destroy a cert the user cannot get
// back.
//
// vault_auth.json (ROSAENG-74343) is included even though it's "just a
// pointer" like last_sandbox, because unlike last_sandbox it isn't harmless
// to leave behind: it steers which Vault secret later commands read
// credentials from. Leaving it behind after `remove` would mean re-adding
// the same name (e.g. the "fix a typo'd endpoint" workflow this function's
// own doc comment describes) silently inherits a previous, possibly
// unrelated registration's Vault coordinates.
var knownGatewayFiles = []string{
	"metadata.json",
	"oidc_token.json",
	"vault_auth.json",
}

// SetActive writes name to the active_gateway pointer file.
func SetActive(w Writer, name string) error {
	if err := ValidateGatewayName(name); err != nil {
		return err
	}
	return w.WriteFile(activeGatewayRel, []byte(name), 0o600)
}

// ClearActiveIfMatches removes the active_gateway pointer when it currently
// names name (so removing or logging out of an unrelated, non-active gateway
// never clears it). A missing active_gateway file is a no-op. Mirrors
// ClearLastSandboxIfMatches's read-compare-remove idiom (lastsandbox.go).
func ClearActiveIfMatches(w Writer, name string) error {
	b, err := w.ReadFile(activeGatewayRel)
	if err != nil {
		return nil // nothing to clear
	}
	if strings.TrimSpace(string(b)) != name {
		return nil
	}
	return w.Remove(activeGatewayRel)
}

// RemoveGateway deletes every known on-disk file for a gateway registration
// (metadata, token, last_sandbox, mtls material). Each removal is a no-op if
// the file was never written — removing a gateway that only has a subset of
// these (e.g. never authenticated) is not an error.
func RemoveGateway(w Writer, name string) error {
	if err := ValidateGatewayName(name); err != nil {
		return err
	}
	for _, rel := range knownGatewayFiles {
		if err := w.Remove(GatewayDir(name) + "/" + rel); err != nil {
			return err
		}
	}
	return nil
}

// Logout removes only a gateway's oidc_token.json — de-authenticating while
// keeping the registration (metadata.json) intact, unlike RemoveGateway. A
// missing token file is a no-op.
func Logout(w Writer, name string) error {
	if err := ValidateGatewayName(name); err != nil {
		return err
	}
	return w.Remove(GatewayDir(name) + "/oidc_token.json")
}
