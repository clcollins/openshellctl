package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

// --- Review finding 1: persisted lookup ignored --gateway-endpoint --------

// TestApplyVaultAuthSource_PersistedLookupRespectsGatewayEndpoint pins a
// cross-gateway leak found in review: persistedVaultAuthConfig must resolve
// the gateway the exact same way gatewayconfig.Resolve does (endpoint match
// included), not just via -g/OPENSHELL_GATEWAY/active_gateway. Without this,
// a command run with --gateway-endpoint pointing at gateway B, while A is
// the active gateway, would read A's persisted Vault coordinates instead of
// B's (or instead of correctly finding nothing, if B has none).
func TestApplyVaultAuthSource_PersistedLookupRespectsGatewayEndpoint(t *testing.T) {
	resetVaultTestState(t)
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)

	writeTestGatewayWithEndpoint(t, xdg, "gw-a", "https://a.example.com")
	writeTestVaultAuth(t, xdg, "gw-a", `{"mount":"mount-a","path":"path-a"}`)
	if err := os.WriteFile(filepath.Join(xdg, "openshell", "active_gateway"), []byte("gw-a"), 0o600); err != nil {
		t.Fatal(err)
	}

	writeTestGatewayWithEndpoint(t, xdg, "gw-b", "https://b.example.com")
	writeTestVaultAuth(t, xdg, "gw-b", `{"mount":"mount-b","path":"path-b"}`)

	// No -g given; --gateway-endpoint names gw-b's endpoint. The resolved
	// gateway is gw-b (via FindByEndpoint), not the active gw-a.
	viper.Set("gateway-endpoint", "https://b.example.com")

	reader := &capturingVaultReader{data: map[string]any{}}
	if err := applyVaultAuthSource(withFakeVaultReader(reader)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reader.calls != 1 {
		t.Fatalf("reader was called %d times, want 1", reader.calls)
	}
	if reader.gotMount != "mount-b" || reader.gotPath != "path-b" {
		t.Errorf("read (%q,%q), want gw-b's (mount-b,path-b) — got the active gateway's config instead of the endpoint-resolved one", reader.gotMount, reader.gotPath)
	}
}

// TestApplyVaultAuthSource_PersistedLookupFindsNothingForUnmatchedEndpoint
// confirms the fix doesn't just "resolve the active gateway always" — an
// endpoint matching no registered gateway must find no persisted config at
// all (not fall back to whatever happens to be active).
func TestApplyVaultAuthSource_PersistedLookupFindsNothingForUnmatchedEndpoint(t *testing.T) {
	resetVaultTestState(t)
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)

	writeTestGatewayWithEndpoint(t, xdg, "gw-a", "https://a.example.com")
	writeTestVaultAuth(t, xdg, "gw-a", `{"mount":"mount-a","path":"path-a"}`)
	if err := os.WriteFile(filepath.Join(xdg, "openshell", "active_gateway"), []byte("gw-a"), 0o600); err != nil {
		t.Fatal(err)
	}

	viper.Set("gateway-endpoint", "https://unregistered.example.com")

	reader := &capturingVaultReader{data: map[string]any{}}
	if err := applyVaultAuthSource(withFakeVaultReader(reader)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reader.calls != 0 {
		t.Errorf("reader was called %d times, want 0 (no match for the given endpoint)", reader.calls)
	}
}

// --- Review finding 2: `gateway add` leaking the active gateway's config --

// TestGatewayAdd_PlainAddDoesNotTouchActiveGatewaysVaultConfig pins the most
// serious finding from review: a plain `gateway add <endpoint>` with NO
// --vault-kv-* flags of its own must never read another, already-active
// gateway's persisted Vault secret and must never need a live Vault session
// to succeed — exactly the "zero behavior change for anyone not using Vault
// flags on this invocation" guarantee the feature was supposed to preserve.
// Before the fix, applyVaultAuthSource ran as runGatewayAdd's very first
// step, before the new gateway existed, and fell back to whatever gateway
// was already active.
func TestGatewayAdd_PlainAddDoesNotTouchActiveGatewaysVaultConfig(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("OPENSHELL_NO_BROWSER", "1")

	// gw-a is registered, active, and has a persisted Vault config — the
	// "ambient" state a totally unrelated `gateway add` must not touch.
	writeTestGatewayWithEndpoint(t, xdg, "gw-a", "https://a.example.com")
	writeTestVaultAuth(t, xdg, "gw-a", `{"mount":"mount-a","path":"path-a"}`)
	if err := os.WriteFile(filepath.Join(xdg, "openshell", "active_gateway"), []byte("gw-a"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The reader would error if called at all — proving the add path below
	// never invokes it.
	reader := &capturingVaultReader{}
	ctx := withVaultDeps(context.Background(), vaultDeps{Reader: reader})
	out, err := runCmdCtx(t, ctx, "gateway", "add", "https://b.example.com", "--name", "gw-b", "--oidc-issuer", "https://issuer-b")
	if err != nil {
		t.Fatalf("gateway add: %v\noutput:\n%s", err, out)
	}
	if reader.calls != 0 {
		t.Fatalf("reader was called %d times, want 0 — a plain `gateway add` must never consult another gateway's Vault config", reader.calls)
	}

	data, rerr := os.ReadFile(filepath.Join(xdg, "openshell", "gateways", "gw-b", "metadata.json"))
	if rerr != nil {
		t.Fatalf("metadata.json not written: %v", rerr)
	}
	if strings.Contains(string(data), "mount-a") || strings.Contains(string(data), "path-a") {
		t.Errorf("gw-b's metadata.json leaked gw-a's vault config: %s", data)
	}
}

// TestGatewayAdd_ExplicitVaultFlagsStillWorkWithActiveGatewayPresent confirms
// the fix above doesn't break the designed, documented path: `gateway add`
// WITH its own explicit --vault-kv-mount/--vault-kv-path must still read
// Vault and persist them for the new gateway, even while another gateway
// with a different persisted config is active.
func TestGatewayAdd_ExplicitVaultFlagsStillWorkWithActiveGatewayPresent(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("OPENSHELL_NO_BROWSER", "1")

	writeTestGatewayWithEndpoint(t, xdg, "gw-a", "https://a.example.com")
	writeTestVaultAuth(t, xdg, "gw-a", `{"mount":"mount-a","path":"path-a"}`)
	if err := os.WriteFile(filepath.Join(xdg, "openshell", "active_gateway"), []byte("gw-a"), 0o600); err != nil {
		t.Fatal(err)
	}

	reader := &capturingVaultReader{data: map[string]any{}}
	ctx := withVaultDeps(context.Background(), vaultDeps{Reader: reader})
	out, err := runCmdCtx(t, ctx,
		"--vault-kv-mount", "mount-b", "--vault-kv-path", "path-b",
		"gateway", "add", "https://b.example.com", "--name", "gw-b", "--oidc-issuer", "https://issuer-b")
	if err != nil {
		t.Fatalf("gateway add: %v\noutput:\n%s", err, out)
	}
	if reader.calls != 1 || reader.gotMount != "mount-b" || reader.gotPath != "path-b" {
		t.Errorf("reader calls=%d mount=%q path=%q, want 1/mount-b/path-b", reader.calls, reader.gotMount, reader.gotPath)
	}
	data, rerr := os.ReadFile(filepath.Join(xdg, "openshell", "gateways", "gw-b", "vault_auth.json"))
	if rerr != nil {
		t.Fatalf("vault_auth.json not written for gw-b: %v", rerr)
	}
	if !strings.Contains(string(data), "mount-b") {
		t.Errorf("gw-b's vault_auth.json = %s, want mount-b", data)
	}
}

// --- Review finding 5: `gateway remove` leaves vault_auth.json behind -----

// TestGatewayRemove_RemovesPersistedVaultAuthConfig pins the found gap:
// vault_auth.json steers credential sourcing (unlike the harmless
// last_sandbox pointer it's modeled on), so leaving it behind after `gateway
// remove` means a later `gateway add` reusing the same name silently
// inherits stale Vault coordinates from a previous, possibly unrelated
// registration.
func TestGatewayRemove_RemovesPersistedVaultAuthConfig(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("OPENSHELL_NO_BROWSER", "1")

	reader := &capturingVaultReader{data: map[string]any{}}
	ctx := withVaultDeps(context.Background(), vaultDeps{Reader: reader})
	if _, err := runCmdCtx(t, ctx,
		"--vault-kv-mount", "osd-sre", "--vault-kv-path", "rosa-agent",
		"gateway", "add", "https://gw.example.com", "--name", "gw", "--oidc-issuer", "https://issuer"); err != nil {
		t.Fatalf("gateway add: %v", err)
	}
	vaultAuthPath := filepath.Join(xdg, "openshell", "gateways", "gw", "vault_auth.json")
	if _, err := os.Stat(vaultAuthPath); err != nil {
		t.Fatalf("precondition failed, vault_auth.json missing: %v", err)
	}

	if _, err := runCmd(t, "gateway", "remove", "gw"); err != nil {
		t.Fatalf("gateway remove: %v", err)
	}
	if _, err := os.Stat(vaultAuthPath); !os.IsNotExist(err) {
		t.Errorf("vault_auth.json should be removed by `gateway remove`, stat err = %v", err)
	}
}

// --- Review: SaveVaultAuthConfig should validate the gateway name ---------

func TestSaveVaultAuthConfig_RejectsInvalidGatewayName(t *testing.T) {
	// Covered at the gatewayconfig-package level in vaultauth_test.go would
	// duplicate pkg/gatewayconfig's own ValidateGatewayName tests; this just
	// pins that persistVaultAuthConfig's write path surfaces the error
	// rather than writing an unvalidated path.
	resetVaultTestState(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	viper.Set("vault-kv-mount", "osd-sre")
	viper.Set("vault-kv-path", "rosa-agent")

	if err := persistVaultAuthConfig("../escape"); err == nil {
		t.Error("expected an error persisting vault auth config for an invalid gateway name")
	}
}

// --- Review finding 4: no way to disable Vault once persisted -------------

// TestApplyVaultAuthSource_NoVaultFlagDisablesPersistedConfig pins the
// requested escape hatch: once a gateway has a persisted vault_auth.json,
// --no-vault/OPENSHELL_NO_VAULT lets a single invocation skip Vault
// entirely, without deleting the file by hand.
func TestApplyVaultAuthSource_NoVaultFlagDisablesPersistedConfig(t *testing.T) {
	resetVaultTestState(t)
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	writeTestGateway(t, xdg, "rosa")
	writeTestVaultAuth(t, xdg, "rosa", `{"mount":"osd-sre","path":"rosa-agent"}`)
	viper.Set("gateway", "rosa")
	viper.Set("no-vault", true)

	reader := &capturingVaultReader{}
	if err := applyVaultAuthSource(withFakeVaultReader(reader)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reader.calls != 0 {
		t.Errorf("reader was called %d times, want 0 with --no-vault set", reader.calls)
	}
}

// TestApplyVaultAuthSource_NoVaultFlagDisablesExplicitFlagsToo confirms
// --no-vault is a full disable, not just a persisted-config bypass — an
// explicit --vault-kv-mount/--vault-kv-path on the same invocation is also
// ignored.
func TestApplyVaultAuthSource_NoVaultFlagDisablesExplicitFlagsToo(t *testing.T) {
	resetVaultTestState(t)
	viper.Set("vault-kv-mount", "osd-sre")
	viper.Set("vault-kv-path", "rosa-agent")
	viper.Set("no-vault", true)

	reader := &capturingVaultReader{}
	if err := applyVaultAuthSource(withFakeVaultReader(reader)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reader.calls != 0 {
		t.Errorf("reader was called %d times, want 0 with --no-vault set", reader.calls)
	}
}

// TestGatewayAdd_NoVaultFlagSkipsPersistingEvenWithExplicitCoordinates
// confirms --no-vault also suppresses writing vault_auth.json, for the edge
// case of someone passing both flags together on one invocation.
func TestGatewayAdd_NoVaultFlagSkipsPersistingEvenWithExplicitCoordinates(t *testing.T) {
	resetVaultTestState(t)
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	writeTestGateway(t, xdg, "rosa")
	viper.Set("vault-kv-mount", "osd-sre")
	viper.Set("vault-kv-path", "rosa-agent")
	viper.Set("no-vault", true)

	if err := persistVaultAuthConfig("rosa"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(xdg, "openshell", "gateways", "rosa", "vault_auth.json")); !os.IsNotExist(err) {
		t.Errorf("vault_auth.json should not be written with --no-vault set, stat err = %v", err)
	}
}

// TestGatewayAdd_ForceRollbackRestoresPreviousVaultAuthConfig fills the gap
// called out in review: the PR claimed test coverage for `gateway add`
// "writing/rolling-back the file", but no test exercised the rollback path.
// Mirrors TestGatewayAdd_ForceRollbackRestoresPrevious (gateway_add_test.go)
// exactly, for vault_auth.json: a --force overwrite with new Vault
// coordinates, followed by an authentication failure, must restore the
// PREVIOUS registration's vault_auth.json bytes, not leave the new
// (never-authenticated) coordinates in place.
func TestGatewayAdd_ForceRollbackRestoresPreviousVaultAuthConfig(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("OPENSHELL_NO_BROWSER", "1")

	reader := &capturingVaultReader{data: map[string]any{}}
	vctx := withVaultDeps(context.Background(), vaultDeps{Reader: reader})

	// A working registration with vault_auth.json pointing at the old
	// coordinates.
	if _, err := runCmdCtx(t, vctx,
		"--vault-kv-mount", "mount-old", "--vault-kv-path", "path-old",
		"gateway", "add", "https://old.example.com", "--name", "dup", "--oidc-issuer", "https://old-issuer"); err != nil {
		t.Fatalf("first add: %v", err)
	}
	vaultAuthPath := filepath.Join(xdg, "openshell", "gateways", "dup", "vault_auth.json")
	previousVaultAuth, err := os.ReadFile(vaultAuthPath)
	if err != nil {
		t.Fatalf("precondition: vault_auth.json not written: %v", err)
	}
	if !strings.Contains(string(previousVaultAuth), "mount-old") {
		t.Fatalf("precondition: vault_auth.json = %s, want mount-old", previousVaultAuth)
	}

	// Force-overwrite with NEW Vault coordinates, then fail authentication
	// (via an injected failing TokenSource, same as the existing rollback
	// tests — this bypasses the real client-credentials exchange entirely).
	// A client secret must be present so authenticateNewGateway actually
	// takes the client-credentials branch (resolveAuth) rather than the
	// register-only branch NO_BROWSER+no-secret would otherwise take.
	t.Setenv("OPENSHELL_OIDC_CLIENT_SECRET", "wrong-secret")
	failing := fakeTokenSource{err: errors.New("invalid_client")}
	forceCtx := withVaultDeps(withDeps(context.Background(), cliDeps{TokenSource: failing}), vaultDeps{Reader: reader})
	_, err = runCmdCtx(t, forceCtx,
		"--vault-kv-mount", "mount-new", "--vault-kv-path", "path-new",
		"gateway", "add", "https://new.example.com", "--name", "dup", "--oidc-issuer", "https://new-issuer", "--force")
	if err == nil {
		t.Fatal("expected the auth failure to propagate")
	}

	gotVaultAuth, rerr := os.ReadFile(vaultAuthPath)
	if rerr != nil {
		t.Fatalf("vault_auth.json should have been restored, not removed: %v", rerr)
	}
	if string(gotVaultAuth) != string(previousVaultAuth) {
		t.Errorf("vault_auth.json = %s, want the previous registration's restored: %s", gotVaultAuth, previousVaultAuth)
	}
}

// TestGatewayAdd_AuthFailureRollsBackRemovesVaultAuthConfigWhenNoneExistedBefore
// is the non-force counterpart: a brand-new gateway (no prior registration
// at all) whose authentication fails must have its just-written
// vault_auth.json removed, not left behind for a future GatewayExistsError
// retry to silently inherit.
func TestGatewayAdd_AuthFailureRollsBackRemovesVaultAuthConfigWhenNoneExistedBefore(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)

	reader := &capturingVaultReader{data: map[string]any{}}
	failing := fakeTokenSource{err: errors.New("invalid_client")}
	ctx := withVaultDeps(withDeps(context.Background(), cliDeps{TokenSource: failing}), vaultDeps{Reader: reader})
	_, err := runCmdCtx(t, ctx,
		"--vault-kv-mount", "osd-sre", "--vault-kv-path", "rosa-agent",
		"gateway", "add", "https://gw.example.com", "--name", "bad-gw", "--oidc-issuer", "https://issuer")
	if err == nil {
		t.Fatal("expected the auth failure to propagate")
	}

	if _, statErr := os.Stat(filepath.Join(xdg, "openshell", "gateways", "bad-gw", "vault_auth.json")); statErr == nil {
		t.Error("vault_auth.json should have been rolled back (removed) after an auth failure with no prior registration")
	}
}
