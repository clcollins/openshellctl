package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

// capturingVaultReader is a VaultReader double that records the mount/path it
// was called with, so tests can assert resolveVaultAuthSource picked up the
// expected (persisted vs. explicit) coordinates, not just that a read
// happened.
type capturingVaultReader struct {
	data     map[string]any
	gotMount string
	gotPath  string
	calls    int
}

func (f *capturingVaultReader) ReadSecret(_ context.Context, mount, path string) (map[string]any, error) {
	f.calls++
	f.gotMount = mount
	f.gotPath = path
	return f.data, nil
}

// writeTestGateway writes a minimal, valid metadata.json for name under the
// XDG-config-rooted user gateway tree, so gatewayconfig.Load/Resolve can find
// it the same way `gateway add` would have left it.
func writeTestGateway(t *testing.T, xdg, name string) {
	t.Helper()
	writeTestGatewayWithEndpoint(t, xdg, name, "https://gw.example.com")
}

// writeTestGatewayWithEndpoint is writeTestGateway, but lets the caller pick
// a distinct endpoint — needed for tests with more than one registered
// gateway, where FindByEndpoint must be able to tell them apart.
func writeTestGatewayWithEndpoint(t *testing.T, xdg, name, endpoint string) {
	t.Helper()
	dir := filepath.Join(xdg, "openshell", "gateways", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	md := `{"name":"` + name + `","gateway_endpoint":"` + endpoint + `","is_remote":true,"gateway_port":0,"auth_mode":"oidc","oidc_issuer":"https://issuer"}`
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(md), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeTestVaultAuth(t *testing.T, xdg, name, content string) {
	t.Helper()
	dir := filepath.Join(xdg, "openshell", "gateways", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vault_auth.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestApplyVaultAuthSource_UsesPersistedConfigForNamedGateway pins the core
// ROSAENG-74343 behavior: with no --vault-kv-mount/--vault-kv-path/
// --vault-field-prefix given at all, a previously-persisted vault_auth.json
// for the resolved gateway (-g/OPENSHELL_GATEWAY) is used instead.
func TestApplyVaultAuthSource_UsesPersistedConfigForNamedGateway(t *testing.T) {
	resetVaultTestState(t)
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	writeTestGateway(t, xdg, "rosa")
	writeTestVaultAuth(t, xdg, "rosa", `{"mount":"osd-sre","path":"rosa-agent","field_prefix":"hypershell"}`)
	viper.Set("gateway", "rosa")

	reader := &capturingVaultReader{data: map[string]any{"hypershell-oidc-client-id": "abc"}}
	if err := applyVaultAuthSource(withFakeVaultReader(reader)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reader.calls != 1 {
		t.Fatalf("reader was called %d times, want 1", reader.calls)
	}
	if reader.gotMount != "osd-sre" || reader.gotPath != "rosa-agent" {
		t.Errorf("read (%q,%q), want (osd-sre,rosa-agent) from the persisted config", reader.gotMount, reader.gotPath)
	}
	if got := viper.GetString("oidc-client-id"); got != "abc" {
		t.Errorf("oidc-client-id = %q, want %q", got, "abc")
	}
}

// TestApplyVaultAuthSource_UsesPersistedConfigForActiveGateway is the same as
// above but via the active_gateway file rather than an explicit -g/env, since
// persistedVaultAuthConfig must fall back the same way gatewayconfig.Resolve
// itself does.
func TestApplyVaultAuthSource_UsesPersistedConfigForActiveGateway(t *testing.T) {
	resetVaultTestState(t)
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	writeTestGateway(t, xdg, "rosa")
	writeTestVaultAuth(t, xdg, "rosa", `{"mount":"osd-sre","path":"rosa-agent"}`)
	if err := os.WriteFile(filepath.Join(xdg, "openshell", "active_gateway"), []byte("rosa"), 0o600); err != nil {
		t.Fatal(err)
	}

	reader := &capturingVaultReader{data: map[string]any{}}
	if err := applyVaultAuthSource(withFakeVaultReader(reader)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reader.calls != 1 || reader.gotMount != "osd-sre" || reader.gotPath != "rosa-agent" {
		t.Errorf("reader calls=%d mount=%q path=%q, want 1/osd-sre/rosa-agent", reader.calls, reader.gotMount, reader.gotPath)
	}
}

// TestApplyVaultAuthSource_ExplicitFlagsOverridePersistedConfig pins
// acceptance criterion 3: an explicit --vault-kv-mount/--vault-kv-path still
// wins over whatever is persisted for the resolved gateway.
func TestApplyVaultAuthSource_ExplicitFlagsOverridePersistedConfig(t *testing.T) {
	resetVaultTestState(t)
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	writeTestGateway(t, xdg, "rosa")
	writeTestVaultAuth(t, xdg, "rosa", `{"mount":"stale-mount","path":"stale-path"}`)
	viper.Set("gateway", "rosa")
	viper.Set("vault-kv-mount", "osd-sre")
	viper.Set("vault-kv-path", "rosa-agent")

	reader := &capturingVaultReader{data: map[string]any{}}
	if err := applyVaultAuthSource(withFakeVaultReader(reader)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reader.gotMount != "osd-sre" || reader.gotPath != "rosa-agent" {
		t.Errorf("read (%q,%q), want the explicit flags, not the persisted (stale-mount,stale-path)", reader.gotMount, reader.gotPath)
	}
}

// TestApplyVaultAuthSource_NoPersistedConfigAndNoFlagsIsNoop confirms a
// registered gateway with no vault_auth.json at all is still a complete
// no-op, same as before this feature existed.
func TestApplyVaultAuthSource_NoPersistedConfigAndNoFlagsIsNoop(t *testing.T) {
	resetVaultTestState(t)
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	writeTestGateway(t, xdg, "rosa")
	viper.Set("gateway", "rosa")

	reader := &capturingVaultReader{}
	if err := applyVaultAuthSource(withFakeVaultReader(reader)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reader.calls != 0 {
		t.Errorf("reader was called %d times, want 0", reader.calls)
	}
}

// TestGatewayAdd_PersistsVaultAuthConfig confirms `gateway add` with explicit
// --vault-kv-mount/--vault-kv-path/--vault-field-prefix writes
// gateways/<name>/vault_auth.json, so a later command reuses them.
func TestGatewayAdd_PersistsVaultAuthConfig(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("OPENSHELL_NO_BROWSER", "1")

	ctx := withVaultDeps(context.Background(), vaultDeps{Reader: &capturingVaultReader{data: map[string]any{}}})
	out, err := runCmdCtx(t, ctx,
		"--vault-kv-mount", "osd-sre", "--vault-kv-path", "rosa-agent", "--vault-field-prefix", "hypershell",
		"gateway", "add", "https://gw.example.com", "--name", "svc-gw", "--oidc-issuer", "https://issuer")
	if err != nil {
		t.Fatalf("gateway add: %v\noutput:\n%s", err, out)
	}

	data, rerr := os.ReadFile(filepath.Join(xdg, "openshell", "gateways", "svc-gw", "vault_auth.json"))
	if rerr != nil {
		t.Fatalf("vault_auth.json not written: %v", rerr)
	}
	for _, want := range []string{`"mount":"osd-sre"`, `"path":"rosa-agent"`, `"field_prefix":"hypershell"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("vault_auth.json = %s, missing %q", data, want)
		}
	}
}

// TestGatewayAdd_NoVaultFlagsDoesNotWriteVaultAuthConfig confirms a plain
// `gateway add` with no Vault flags at all never creates the file.
func TestGatewayAdd_NoVaultFlagsDoesNotWriteVaultAuthConfig(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("OPENSHELL_NO_BROWSER", "1")

	_, err := runCmd(t, "gateway", "add", "https://gw.example.com", "--name", "svc-gw", "--oidc-issuer", "https://issuer")
	if err != nil {
		t.Fatalf("gateway add: %v", err)
	}
	if _, rerr := os.Stat(filepath.Join(xdg, "openshell", "gateways", "svc-gw", "vault_auth.json")); !os.IsNotExist(rerr) {
		t.Errorf("expected no vault_auth.json, stat err = %v", rerr)
	}
}

// TestGatewayAddThenLaterCommand_ReusesPersistedVaultConfig is the
// end-to-end acceptance test: `gateway add` with explicit Vault flags, then a
// second, separate command (a distinct root.Execute() run, not a direct
// function call) against the same gateway with no Vault flags at all, still
// resolves auth via Vault using the persisted mount/path.
func TestGatewayAddThenLaterCommand_ReusesPersistedVaultConfig(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("OPENSHELL_NO_BROWSER", "1")

	addReader := &capturingVaultReader{data: map[string]any{}}
	addCtx := withVaultDeps(context.Background(), vaultDeps{Reader: addReader})
	if _, err := runCmdCtx(t, addCtx,
		"--vault-kv-mount", "osd-sre", "--vault-kv-path", "rosa-agent",
		"gateway", "add", "https://gw.example.com", "--name", "svc-gw", "--oidc-issuer", "https://issuer"); err != nil {
		t.Fatalf("gateway add: %v", err)
	}

	// A genuinely separate command invocation: no -g, no vault flags — relies
	// entirely on active_gateway (set by `gateway add`) + the persisted
	// vault_auth.json. `gateway logout` is a real command (not a direct call
	// to applyVaultAuthSource) that goes through the exact same
	// resolveVaultAuthSource path a production second process would.
	// runCmdCtx resets viper + the vaultAuthOnce memoization before running,
	// matching what a fresh `openshellctl` process actually gets.
	laterReader := &capturingVaultReader{data: map[string]any{}}
	laterCtx := withVaultDeps(context.Background(), vaultDeps{Reader: laterReader})
	if out, err := runCmdCtx(t, laterCtx, "gateway", "logout"); err != nil {
		t.Fatalf("gateway logout: %v\noutput:\n%s", err, out)
	}
	if laterReader.calls != 1 {
		t.Fatalf("reader was called %d times, want 1", laterReader.calls)
	}
	if laterReader.gotMount != "osd-sre" || laterReader.gotPath != "rosa-agent" {
		t.Errorf("later command read (%q,%q), want the persisted (osd-sre,rosa-agent)", laterReader.gotMount, laterReader.gotPath)
	}
}
