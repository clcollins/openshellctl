package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/viper"

	"github.com/openshift-online/openshellctl/pkg/vaultconfig"
)

// fakeVaultReader is a hermetic VaultReader double — no network, no real
// Vault server — used to drive applyVaultAuthSource's wiring logic.
type fakeVaultReader struct {
	data  map[string]any
	err   error
	calls int
}

func (f *fakeVaultReader) ReadSecret(_ context.Context, _, _ string) (map[string]any, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.data, nil
}

func withFakeVaultReader(reader vaultconfig.VaultReader) context.Context {
	return withVaultDeps(context.Background(), vaultDeps{Reader: reader})
}

// captureStderr redirects os.Stderr to an in-memory pipe for the duration of
// fn, then returns whatever was written. t.Cleanup restores os.Stderr even if
// fn calls t.Fatalf or panics, so subsequent tests in the binary are not left
// with a closed or invalid stderr fd.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	t.Cleanup(func() { os.Stderr = orig })

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("captureStderr: os.Pipe: %v", err)
	}
	os.Stderr = w
	fn()
	if closeErr := w.Close(); closeErr != nil {
		t.Fatalf("captureStderr: close write end: %v", closeErr)
	}
	var buf strings.Builder
	if _, copyErr := io.Copy(&buf, r); copyErr != nil {
		t.Fatalf("captureStderr: read stderr: %v", copyErr)
	}
	return buf.String()
}

// resetVaultOnce resets applyVaultAuthSource's process-lifetime memoization
// (vaultAuthOnce/vaultAuthResult). Shared by resetVaultTestState here and by
// runCmdCtx (sandbox_cmd_test.go), so every test path that can reach
// applyVaultAuthSource resets the same two package vars the same way.
func resetVaultOnce() {
	vaultAuthOnce = sync.Once{}
	vaultAuthResult = nil
}

// resetVaultTestState resets both viper and the Vault-auth memoization,
// before and after the test. Without the latter, the first test in the
// package to call applyVaultAuthSource would permanently cache its result for
// every test that runs afterward in the same test binary.
func resetVaultTestState(t *testing.T) {
	t.Helper()
	reset := func() {
		viper.Reset()
		resetVaultOnce()
	}
	reset()
	t.Cleanup(reset)
}

func TestApplyVaultAuthSource_NoopWhenNeitherFlagSet(t *testing.T) {
	resetVaultTestState(t)

	reader := &fakeVaultReader{data: map[string]any{"oidc-issuer": "https://issuer"}}
	if err := applyVaultAuthSource(withFakeVaultReader(reader)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reader.calls != 0 {
		t.Errorf("reader was called %d times, want 0", reader.calls)
	}
	if got := viper.GetString("oidc-issuer"); got != "" {
		t.Errorf("oidc-issuer = %q, want empty (no-op)", got)
	}
}

func TestApplyVaultAuthSource_UsageErrorWhenOnlyMountSet(t *testing.T) {
	resetVaultTestState(t)
	viper.Set("vault-kv-mount", "osd-sre")

	err := applyVaultAuthSource(withFakeVaultReader(&fakeVaultReader{}))
	var usageErr *UsageError
	if !errors.As(err, &usageErr) {
		t.Fatalf("expected *UsageError, got %T: %v", err, err)
	}
	for _, want := range []string{"--vault-kv-mount", "--vault-kv-path"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error message %q missing %q", err.Error(), want)
		}
	}
}

func TestApplyVaultAuthSource_UsageErrorWhenOnlyPathSet(t *testing.T) {
	resetVaultTestState(t)
	viper.Set("vault-kv-path", "rosa-agent")

	err := applyVaultAuthSource(withFakeVaultReader(&fakeVaultReader{}))
	var usageErr *UsageError
	if !errors.As(err, &usageErr) {
		t.Fatalf("expected *UsageError, got %T: %v", err, err)
	}
}

// TestApplyVaultAuthSource_UsageErrorWhenOnlyPrefixSet pins a specific
// misconfiguration: --vault-field-prefix has no effect without
// --vault-kv-mount/--vault-kv-path, and silently ignoring it (rather than
// erroring) would mask a typo'd or forgotten mount/path flag.
func TestApplyVaultAuthSource_UsageErrorWhenOnlyPrefixSet(t *testing.T) {
	resetVaultTestState(t)
	viper.Set("vault-field-prefix", "hypershell")

	err := applyVaultAuthSource(withFakeVaultReader(&fakeVaultReader{}))
	var usageErr *UsageError
	if !errors.As(err, &usageErr) {
		t.Fatalf("expected *UsageError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "--vault-field-prefix") {
		t.Errorf("error message %q should name --vault-field-prefix", err.Error())
	}
}

func TestApplyVaultAuthSource_FillsDefaultsFromPrefixedFields(t *testing.T) {
	resetVaultTestState(t)
	viper.Set("vault-kv-mount", "osd-sre")
	viper.Set("vault-kv-path", "rosa-agent")
	viper.Set("vault-field-prefix", "hypershell")

	reader := &fakeVaultReader{data: map[string]any{
		"hypershell-oidc-client-id":     "abc",
		"hypershell-oidc-client-secret": "shh",
	}}
	if err := applyVaultAuthSource(withFakeVaultReader(reader)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reader.calls != 1 {
		t.Errorf("reader was called %d times, want 1", reader.calls)
	}
	if got := viper.GetString("oidc-client-id"); got != "abc" {
		t.Errorf("oidc-client-id = %q, want %q", got, "abc")
	}
	if got := viper.GetString("oidc.client_secret"); got != "shh" {
		t.Errorf("oidc.client_secret = %q, want %q", got, "shh")
	}
}

func TestApplyVaultAuthSource_ExplicitGatewayFlagBeatsVaultForBothHalvesOfThePair(t *testing.T) {
	resetVaultTestState(t)
	viper.Set("vault-kv-mount", "osd-sre")
	viper.Set("vault-kv-path", "rosa-agent")
	viper.Set("gateway", "mine") // simulates an explicit --gateway/env

	reader := &fakeVaultReader{data: map[string]any{
		"gateway":          "vault-gw",
		"gateway-endpoint": "https://vault.example.com",
	}}
	if err := applyVaultAuthSource(withFakeVaultReader(reader)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := viper.GetString("gateway"); got != "mine" {
		t.Errorf("gateway = %q, want %q (explicit value must win)", got, "mine")
	}
	if got := viper.GetString("gateway-endpoint"); got != "" {
		t.Errorf("gateway-endpoint = %q, want empty (vault's half of the pair must be suppressed too)", got)
	}
}

func TestApplyVaultAuthSource_ClientSecretFileBeatsVaultSecret(t *testing.T) {
	resetVaultTestState(t)
	viper.Set("vault-kv-mount", "osd-sre")
	viper.Set("vault-kv-path", "rosa-agent")
	viper.Set("client-secret-file", "/tmp/my-secret")

	reader := &fakeVaultReader{data: map[string]any{
		"oidc-client-id":     "vault-client",
		"oidc-client-secret": "vault-secret",
	}}
	if err := applyVaultAuthSource(withFakeVaultReader(reader)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := viper.GetString("oidc.client_secret"); got != "" {
		t.Errorf("oidc.client_secret = %q, want empty (--client-secret-file must win)", got)
	}
	if got := viper.GetString("oidc-client-id"); got != "" {
		t.Errorf("oidc-client-id = %q, want empty (client-secret-file occupies the whole pair)", got)
	}
}

// TestApplyVaultAuthSource_ClientSecretFileAlsoExcludesIssuerAndAudience is an
// end-to-end pin of AC1 for the --client-secret-file path: a Vault secret with
// oidc-issuer and oidc-audience must not pollute viper when ClientSecretSource
// is already occupied.
func TestApplyVaultAuthSource_ClientSecretFileAlsoExcludesIssuerAndAudience(t *testing.T) {
	resetVaultTestState(t)
	viper.Set("vault-kv-mount", "osd-sre")
	viper.Set("vault-kv-path", "rosa-agent")
	viper.Set("client-secret-file", "/run/secrets/my-secret") // ClientSecretSource

	reader := &fakeVaultReader{data: map[string]any{
		"oidc-client-id":     "vault-client",
		"oidc-client-secret": "vault-secret",
		"oidc-issuer":        "https://prod.issuer.example.com",
		"oidc-audience":      "prod-audience",
		"oidc-scopes":        "openid profile email",
	}}
	if err := applyVaultAuthSource(withFakeVaultReader(reader)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The entire cluster (including issuer, audience, and scopes) must be blocked.
	for _, key := range []string{"oidc-client-id", "oidc.client_secret", "oidc-issuer", "oidc-audience", "oidc-scopes"} {
		if got := viper.GetString(key); got != "" {
			t.Errorf("%s = %q, want empty (client-secret-file activates cluster block)", key, got)
		}
	}
}

func TestApplyVaultAuthSource_GatewayOverrideAloneDoesNotExcludeIssuerOrAudience(t *testing.T) {
	resetVaultTestState(t)
	viper.Set("vault-kv-mount", "osd-sre")
	viper.Set("vault-kv-path", "rosa-agent")
	viper.Set("gateway", "mine") // only the gateway pair is overridden

	reader := &fakeVaultReader{data: map[string]any{
		"gateway":       "vault-gw",
		"oidc-issuer":   "https://issuer",
		"oidc-audience": "openshell-cli",
	}}
	if err := applyVaultAuthSource(withFakeVaultReader(reader)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// gateway override only blocks the gateway pair — oidc-issuer/audience should still fill
	if got := viper.GetString("oidc-issuer"); got != "https://issuer" {
		t.Errorf("oidc-issuer = %q, want %q", got, "https://issuer")
	}
	if got := viper.GetString("oidc-audience"); got != "openshell-cli" {
		t.Errorf("oidc-audience = %q, want %q", got, "openshell-cli")
	}
}

// TestApplyVaultAuthSource_ClientPairOverrideAlsoExcludesIssuerAndAudience pins
// AC1 of MCS-357: when the user supplies explicit client credentials via flag or
// env, Vault must NOT fill oidc-issuer or oidc-audience either — mixing an
// explicit client-id with a Vault-sourced issuer from a different environment
// produces a confusing 401 at the gateway.
func TestApplyVaultAuthSource_ClientPairOverrideAlsoExcludesIssuerAndAudience(t *testing.T) {
	resetVaultTestState(t)
	viper.Set("vault-kv-mount", "osd-sre")
	viper.Set("vault-kv-path", "rosa-agent")
	viper.Set("oidc-client-id", "my-client") // explicit client-id — activates the cluster rule

	reader := &fakeVaultReader{data: map[string]any{
		"oidc-client-id":     "vault-client",
		"oidc-client-secret": "vault-secret",
		"oidc-issuer":        "https://prod.issuer.example.com",
		"oidc-audience":      "prod-audience",
	}}
	if err := applyVaultAuthSource(withFakeVaultReader(reader)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// client-id from viper wins (it was set explicitly before vault ran)
	if got := viper.GetString("oidc-client-id"); got != "my-client" {
		t.Errorf("oidc-client-id = %q, want %q (explicit value must win)", got, "my-client")
	}
	// vault secret must NOT contaminate oidc-issuer or oidc-audience
	if got := viper.GetString("oidc-issuer"); got != "" {
		t.Errorf("oidc-issuer = %q, want empty (vault must not fill when client pair is overridden)", got)
	}
	if got := viper.GetString("oidc-audience"); got != "" {
		t.Errorf("oidc-audience = %q, want empty (vault must not fill when client pair is overridden)", got)
	}
	// vault secret must NOT fill client-secret either (client pair excluded)
	if got := viper.GetString("oidc.client_secret"); got != "" {
		t.Errorf("oidc.client_secret = %q, want empty (client pair excluded)", got)
	}
}

// TestApplyVaultAuthSource_ExplicitIssuerFlagBeatsVaultIssuer pins that an
// explicit --oidc-issuer flag prevents Vault from overwriting it with a default.
func TestApplyVaultAuthSource_ExplicitIssuerFlagBeatsVaultIssuer(t *testing.T) {
	resetVaultTestState(t)
	viper.Set("vault-kv-mount", "osd-sre")
	viper.Set("vault-kv-path", "rosa-agent")
	viper.Set("oidc-issuer", "https://my.issuer.example.com")

	reader := &fakeVaultReader{data: map[string]any{
		"oidc-issuer":   "https://vault.issuer.example.com",
		"oidc-audience": "vault-audience",
	}}
	if err := applyVaultAuthSource(withFakeVaultReader(reader)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := viper.GetString("oidc-issuer"); got != "https://my.issuer.example.com" {
		t.Errorf("oidc-issuer = %q, want explicit value %q", got, "https://my.issuer.example.com")
	}
	// oidc-audience should still fill (only oidc-issuer was overridden, not the client pair)
	if got := viper.GetString("oidc-audience"); got != "vault-audience" {
		t.Errorf("oidc-audience = %q, want %q from vault", got, "vault-audience")
	}
}

// TestApplyVaultAuthSource_GatewayMismatchWarnsToStderr pins AC3 of MCS-357:
// when the user supplies --gateway/-g and Vault's secret also carries a
// gateway field with a DIFFERENT value, a warning is printed to stderr so
// the user knows the mismatch — the explicit flag still wins (no error).
func TestApplyVaultAuthSource_GatewayMismatchWarnsToStderr(t *testing.T) {
	resetVaultTestState(t)
	viper.Set("vault-kv-mount", "osd-sre")
	viper.Set("vault-kv-path", "rosa-agent")
	viper.Set("gateway", "my-gateway") // explicit -g flag

	reader := &fakeVaultReader{data: map[string]any{
		"gateway":       "vault-gateway", // different value in Vault
		"oidc-issuer":   "https://issuer",
		"oidc-audience": "aud",
	}}

	var runErr error
	stderr := captureStderr(t, func() {
		runErr = applyVaultAuthSource(withFakeVaultReader(reader))
	})

	if runErr != nil {
		t.Fatalf("unexpected error: %v", runErr)
	}
	if !strings.Contains(stderr, "vault-gateway") || !strings.Contains(stderr, "my-gateway") {
		t.Errorf("expected warning mentioning both gateway values, got stderr=%q", stderr)
	}
	// Explicit --gateway must win: vault's value must not become the default.
	if got := viper.GetString("gateway"); got != "my-gateway" {
		t.Errorf("gateway = %q, want explicit value %q", got, "my-gateway")
	}
}

// TestApplyVaultAuthSource_SameGatewayNoWarn checks that no spurious warning
// is printed when Vault's gateway matches the user's explicit --gateway.
func TestApplyVaultAuthSource_SameGatewayNoWarn(t *testing.T) {
	resetVaultTestState(t)
	viper.Set("vault-kv-mount", "osd-sre")
	viper.Set("vault-kv-path", "rosa-agent")
	viper.Set("gateway", "same-gw")

	reader := &fakeVaultReader{data: map[string]any{"gateway": "same-gw"}}

	var runErr error
	stderr := captureStderr(t, func() {
		runErr = applyVaultAuthSource(withFakeVaultReader(reader))
	})

	if runErr != nil {
		t.Fatalf("unexpected error: %v", runErr)
	}
	if strings.Contains(stderr, "Warning") {
		t.Errorf("unexpected warning when gateways match: %q", stderr)
	}
}

// TestApplyVaultAuthSource_GatewayEndpointMismatchWarnsToStderr checks the
// symmetric AC3 path for --gateway-endpoint: when the explicit endpoint
// differs from Vault's gateway-endpoint field, a warning is printed.
func TestApplyVaultAuthSource_GatewayEndpointMismatchWarnsToStderr(t *testing.T) {
	resetVaultTestState(t)
	viper.Set("vault-kv-mount", "osd-sre")
	viper.Set("vault-kv-path", "rosa-agent")
	viper.Set("gateway-endpoint", "https://my.gw.example.com") // explicit endpoint

	reader := &fakeVaultReader{data: map[string]any{
		"gateway-endpoint": "https://vault.gw.example.com", // different value in Vault
	}}

	var runErr error
	stderr := captureStderr(t, func() {
		runErr = applyVaultAuthSource(withFakeVaultReader(reader))
	})

	if runErr != nil {
		t.Fatalf("unexpected error: %v", runErr)
	}
	if !strings.Contains(stderr, "vault.gw.example.com") || !strings.Contains(stderr, "my.gw.example.com") {
		t.Errorf("expected warning mentioning both endpoint values, got stderr=%q", stderr)
	}
}

func TestApplyVaultAuthSource_ReaderErrorPropagatesWhenNothingOverridden(t *testing.T) {
	resetVaultTestState(t)
	viper.Set("vault-kv-mount", "osd-sre")
	viper.Set("vault-kv-path", "rosa-agent")
	// No oidc-client-id or client-secret-file set — user is relying on Vault for credentials.

	wantErr := &vaultconfig.ErrForbidden{Mount: "osd-sre", Path: "rosa-agent"}
	reader := &fakeVaultReader{err: wantErr}
	err := applyVaultAuthSource(withFakeVaultReader(reader))
	var forbidden *vaultconfig.ErrForbidden
	if !errors.As(err, &forbidden) {
		t.Fatalf("expected *vaultconfig.ErrForbidden, got %T: %v", err, err)
	}
}

// TestApplyVaultAuthSource_VaultReadErrorNonFatalWhenClientIDOverridden pins
// AC2 of MCS-357: when the user has supplied ALL auth values via flag/env —
// both the OIDC client-credentials cluster and the gateway cluster — a Vault
// read failure is demoted to a no-op and a warning is emitted. Vault has
// nothing left to contribute.
func TestApplyVaultAuthSource_VaultReadErrorNonFatalWhenClientIDOverridden(t *testing.T) {
	resetVaultTestState(t)
	viper.Set("vault-kv-mount", "osd-sre")
	viper.Set("vault-kv-path", "rosa-agent")
	viper.Set("oidc-client-id", "my-client") // explicit client credentials
	viper.Set("gateway", "my-gw")            // explicit gateway — Vault has nothing left

	reader := &fakeVaultReader{err: &vaultconfig.ErrForbidden{Mount: "osd-sre", Path: "rosa-agent"}}
	var err error
	stderr := captureStderr(t, func() {
		err = applyVaultAuthSource(withFakeVaultReader(reader))
	})
	if err != nil {
		t.Errorf("expected nil (non-fatal when all clusters overridden), got: %v", err)
	}
	if !strings.Contains(stderr, "Warning") {
		t.Errorf("expected a warning on stderr about the suppressed vault error, got: %q", stderr)
	}
}

// TestApplyVaultAuthSource_VaultReadErrorNonFatalWhenClientSecretFileOverridden
// covers the --client-secret-file path: it also occupies the ClientSecretSource
// override slot, so a Vault failure is equally non-fatal when gateway is also set,
// and a warning is emitted.
func TestApplyVaultAuthSource_VaultReadErrorNonFatalWhenClientSecretFileOverridden(t *testing.T) {
	resetVaultTestState(t)
	viper.Set("vault-kv-mount", "osd-sre")
	viper.Set("vault-kv-path", "rosa-agent")
	viper.Set("client-secret-file", "/run/secrets/my-secret") // explicit secret file
	viper.Set("gateway", "my-gw")                             // explicit gateway

	reader := &fakeVaultReader{err: &vaultconfig.ErrForbidden{Mount: "osd-sre", Path: "rosa-agent"}}
	var err error
	stderr := captureStderr(t, func() {
		err = applyVaultAuthSource(withFakeVaultReader(reader))
	})
	if err != nil {
		t.Errorf("expected nil (non-fatal when all clusters overridden), got: %v", err)
	}
	if !strings.Contains(stderr, "Warning") {
		t.Errorf("expected a warning on stderr about the suppressed vault error, got: %q", stderr)
	}
}

// TestApplyVaultAuthSource_VaultReadErrorFatalWhenGatewayNotOverridden checks
// that a Vault read failure is still fatal when the client-credentials cluster
// is explicitly set but the gateway cluster is not — Vault may still be the
// source of the gateway value and silently swallowing the error would surface
// later as a confusing "no gateway" failure instead of the real root cause.
func TestApplyVaultAuthSource_VaultReadErrorFatalWhenGatewayNotOverridden(t *testing.T) {
	resetVaultTestState(t)
	viper.Set("vault-kv-mount", "osd-sre")
	viper.Set("vault-kv-path", "rosa-agent")
	viper.Set("oidc-client-id", "my-client") // client creds set, but no gateway override

	wantErr := &vaultconfig.ErrForbidden{Mount: "osd-sre", Path: "rosa-agent"}
	reader := &fakeVaultReader{err: wantErr}
	err := applyVaultAuthSource(withFakeVaultReader(reader))
	var forbidden *vaultconfig.ErrForbidden
	if !errors.As(err, &forbidden) {
		t.Errorf("expected fatal *vaultconfig.ErrForbidden when gateway not overridden, got: %v", err)
	}
}

func TestApplyVaultAuthSource_NonStringFieldErrors(t *testing.T) {
	resetVaultTestState(t)
	viper.Set("vault-kv-mount", "osd-sre")
	viper.Set("vault-kv-path", "rosa-agent")

	reader := &fakeVaultReader{data: map[string]any{"oidc-client-id": 123}}
	err := applyVaultAuthSource(withFakeVaultReader(reader))
	var fieldErr *vaultconfig.ErrFieldNotString
	if !errors.As(err, &fieldErr) {
		t.Fatalf("expected *vaultconfig.ErrFieldNotString, got %T: %v", err, err)
	}
}

func TestApplyVaultAuthSource_NoInjectedReaderBuildsRealOneAndFailsFastWithoutVaultAddr(t *testing.T) {
	resetVaultTestState(t)
	viper.Set("vault-kv-mount", "osd-sre")
	viper.Set("vault-kv-path", "rosa-agent")
	t.Setenv("VAULT_ADDR", "")
	t.Setenv("VAULT_TOKEN", "")

	// No vaultDeps injected into the context: exercises the real,
	// production vaultReaderFor/realVaultReader path. With VAULT_ADDR
	// unset, this must fail fast without attempting any I/O.
	err := applyVaultAuthSource(context.Background())
	var addrErr *vaultconfig.ErrVaultAddrNotSet
	if !errors.As(err, &addrErr) {
		t.Fatalf("expected *vaultconfig.ErrVaultAddrNotSet, got %T: %v", err, err)
	}
}

// TestApplyVaultAuthSource_ReaderBuildErrorNonFatalWhenAllClustersOverridden
// covers MCS-357 AC2 for the vaultReaderFor failure path (not just ReadSecret).
// When VAULT_ADDR is unset the real reader cannot be built; if the user has
// already supplied both the OIDC client-credentials cluster AND the gateway
// cluster, that error is demoted to a no-op — Vault has nothing left to
// contribute to the invocation.
func TestApplyVaultAuthSource_ReaderBuildErrorNonFatalWhenAllClustersOverridden(t *testing.T) {
	resetVaultTestState(t)
	viper.Set("vault-kv-mount", "osd-sre")
	viper.Set("vault-kv-path", "rosa-agent")
	viper.Set("oidc-client-id", "my-client")
	viper.Set("gateway", "my-gw") // gateway also overridden — Vault has nothing left
	t.Setenv("VAULT_ADDR", "")
	t.Setenv("VAULT_TOKEN", "")

	// No fake reader: forces the real vaultReaderFor path, which fails with
	// ErrVaultAddrNotSet. With both clusters overridden this must be non-fatal
	// and emit a warning.
	var err error
	stderr := captureStderr(t, func() {
		err = applyVaultAuthSource(context.Background())
	})
	if err != nil {
		t.Errorf("expected nil (reader build error non-fatal when all clusters overridden), got: %v", err)
	}
	if !strings.Contains(stderr, "Warning") {
		t.Errorf("expected a warning on stderr about the suppressed vault error, got: %q", stderr)
	}
}

// TestApplyVaultAuthSource_MemoizedAcrossMultipleCallsInOneProcess pins the
// fix for the double-read bug found in review: a single command whose flow
// reaches more than one call site (e.g. `gateway add` with a Vault-sourced
// client secret calls applyVaultAuthSource directly, then again via
// authenticateNewGateway -> resolveAuth -> resolveTokenSource) must only
// issue one Vault read per process, not one per call site — a single-use or
// response-wrapped Vault token would fail the second read outright.
func TestApplyVaultAuthSource_MemoizedAcrossMultipleCallsInOneProcess(t *testing.T) {
	resetVaultTestState(t)
	viper.Set("vault-kv-mount", "osd-sre")
	viper.Set("vault-kv-path", "rosa-agent")

	reader := &fakeVaultReader{data: map[string]any{"oidc-client-id": "abc"}}
	ctx := withFakeVaultReader(reader)

	if err := applyVaultAuthSource(ctx); err != nil {
		t.Fatalf("first call: unexpected error: %v", err)
	}
	if err := applyVaultAuthSource(ctx); err != nil {
		t.Fatalf("second call: unexpected error: %v", err)
	}
	if reader.calls != 1 {
		t.Errorf("reader was called %d times across two calls in one process, want 1", reader.calls)
	}
}

// TestApplyVaultAuthSource_MemoizedErrorIsAlsoCached confirms a failed first
// call doesn't retry the Vault read on a second call within the same
// process either — the inputs (mount/path/prefix) can't have changed, so a
// retry would just fail the same way while doing another network round trip.
func TestApplyVaultAuthSource_MemoizedErrorIsAlsoCached(t *testing.T) {
	resetVaultTestState(t)
	viper.Set("vault-kv-mount", "osd-sre")
	viper.Set("vault-kv-path", "rosa-agent")

	reader := &fakeVaultReader{err: &vaultconfig.ErrForbidden{Mount: "osd-sre", Path: "rosa-agent"}}
	ctx := withFakeVaultReader(reader)

	err1 := applyVaultAuthSource(ctx)
	err2 := applyVaultAuthSource(ctx)
	var forbidden *vaultconfig.ErrForbidden
	if !errors.As(err1, &forbidden) || !errors.As(err2, &forbidden) {
		t.Fatalf("expected both calls to return *vaultconfig.ErrForbidden, got %v and %v", err1, err2)
	}
	if reader.calls != 1 {
		t.Errorf("reader was called %d times across two calls in one process, want 1", reader.calls)
	}
}

// TestVersionCommand_NeverTouchesVaultEvenWhenConfigured pins the acceptance
// criterion that only commands resolving auth touch Vault: `version` reads
// none of the affected viper keys, so it must succeed without ever invoking
// a reader, even when --vault-kv-mount/--vault-kv-path are set (e.g. from a
// user's shell profile) and would otherwise fail fast on a missing Vault
// session. If this regresses (e.g. someone moves applyVaultAuthSource into
// root.PersistentPreRunE), this test starts failing because the injected
// fake reader records a call that must never happen.
func TestVersionCommand_NeverTouchesVaultEvenWhenConfigured(t *testing.T) {
	reader := &fakeVaultReader{err: errors.New("must not be called")}
	ctx := withFakeVaultReader(reader)

	out, err := runCmdCtx(t, ctx, "--vault-kv-mount", "osd-sre", "--vault-kv-path", "rosa-agent", "version")
	if err != nil {
		t.Fatalf("version: %v\noutput:\n%s", err, out)
	}
	if reader.calls != 0 {
		t.Errorf("reader was called %d times, want 0 — version has no auth dependency", reader.calls)
	}
}

// TestGatewayLogout_PicksUpVaultSuppliedGatewayName is the one true end-to-end
// smoke test for the real wiring: it runs an actual command (gateway logout)
// through root.Execute() with no -g/--gateway flag and no OPENSHELL_GATEWAY
// env var, relying entirely on a Vault-injected default for the gateway name.
// A negative-control run without the Vault deps injected confirms the
// positive case genuinely depends on Vault, not on some other fallback
// (e.g. an active_gateway file, which this setup deliberately omits).
func TestGatewayLogout_PicksUpVaultSuppliedGatewayName(t *testing.T) {
	setupLogoutGatewayTree(t)

	reader := &fakeVaultReader{data: map[string]any{"gateway": "rosa"}}
	ctx := withFakeVaultReader(reader)
	out, err := runCmdCtx(t, ctx, "--vault-kv-mount", "osd-sre", "--vault-kv-path", "rosa-agent", "gateway", "logout")
	if err != nil {
		t.Fatalf("gateway logout: %v\noutput:\n%s", err, out)
	}
	if reader.calls != 1 {
		t.Errorf("reader was called %d times, want 1", reader.calls)
	}
}

func TestGatewayLogout_WithoutVaultFailsWithNoActiveGateway(t *testing.T) {
	setupLogoutGatewayTree(t)

	_, err := runCmdCtx(t, context.Background(), "gateway", "logout")
	if err == nil {
		t.Fatal("expected an error without a Vault-supplied or active gateway name")
	}
}

// setupLogoutGatewayTree writes a registered gateway named "rosa" but
// deliberately does NOT write an active_gateway file, so resolving the
// gateway name has no fallback other than an explicit flag/env/Vault value.
func setupLogoutGatewayTree(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	xdg := filepath.Join(root, "config")
	gwDir := filepath.Join(xdg, "openshell", "gateways", "rosa")
	if err := os.MkdirAll(gwDir, 0o700); err != nil {
		t.Fatal(err)
	}
	md := `{"name":"rosa","gateway_endpoint":"https://gw.example.com","is_remote":false,"gateway_port":443,"auth_mode":"oidc","oidc_issuer":"https://issuer"}`
	if err := os.WriteFile(filepath.Join(gwDir, "metadata.json"), []byte(md), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", xdg)
}
