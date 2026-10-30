package cli

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	types "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"
	"go.uber.org/mock/gomock"

	"github.com/openshift-online/openshellctl/pkg/auth"
	"github.com/openshift-online/openshellctl/pkg/gateway/mock"
	"github.com/openshift-online/openshellctl/pkg/gatewayconfig"
)

// setupDoctorGatewayTree registers a gateway named "rosa" at the given
// endpoint, with an oidc_token.json bundle built from jwt. Reuses
// setupGatewayTreeWithEndpoint (tokenflow_test.go) rather than hand-rolling
// the tree.
func setupDoctorGatewayTree(t *testing.T, endpoint, jwt string) {
	t.Helper()
	xdg := setupGatewayTreeWithEndpoint(t, endpoint)
	bundle := `{"access_token":"` + jwt + `","expires_at":9999999999,"issuer":"https://issuer","client_id":"openshell-cli"}`
	if err := os.WriteFile(filepath.Join(xdg, "openshell", "gateways", "rosa", "oidc_token.json"), []byte(bundle), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestDoctorCommand_AllGreen runs `doctor` end to end through the real command
// tree with no cliDeps injection. The gateway endpoint is a plain-HTTP
// httptest.Server, which gateway.Dial correctly rejects with ErrPlaintextWithAuth
// when a bearer token is present. The Roles check falls back to CheckRolesToken
// (token-claims path); with no --provider requested, Providers is skipped.
// Skips render as "-", not "✗", so all-green holds via the fallback path.
// The gateway-authoritative Roles path is covered hermetically in pkg/doctor.
func TestDoctorCommand_AllGreen(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/oidc-config" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"issuer":"https://issuer","audience":"openshell-cli"}`))
	}))
	defer srv.Close()

	setupDoctorGatewayTree(t, srv.URL, staticJWT(t))

	out, err := runCmd(t, "doctor", "--gateway-endpoint", srv.URL)
	if err != nil {
		t.Fatalf("doctor: %v\noutput:\n%s", err, out)
	}
	for _, want := range []string{"Endpoint URL", "DNS", "HTTP reachability", "Credentials", "Audience", "Roles", "Expiry"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing check %q; got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "✗") {
		t.Errorf("expected no failures, got:\n%s", out)
	}
}

// TestDoctorCommand_NoCredentials_ChecksSkippedAndExit3 is the ticket's
// second acceptance criterion: with the secret unset (and no disk bundle),
// the Credentials check shows the checked-sources list, downstream checks
// are skipped, and the exit code is 3.
func TestDoctorCommand_NoCredentials_ChecksSkippedAndExit3(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"issuer":"https://issuer","audience":"openshell-cli"}`))
	}))
	defer srv.Close()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	out, err := runCmd(t, "doctor", "--gateway-endpoint", srv.URL)
	if err == nil {
		t.Fatalf("expected an error, got success:\n%s", out)
	}
	if exitCodeFor(err) != ExitAuth {
		t.Errorf("exit = %d, want ExitAuth (3)", exitCodeFor(err))
	}
	if !strings.Contains(out, "OPENSHELL_OIDC_CLIENT_SECRET") {
		t.Errorf("Credentials line should show the checked-sources list; got:\n%s", out)
	}
	if !strings.Contains(out, "Hint:") {
		t.Errorf("a failing check should print a copy-pasteable next step; got:\n%s", out)
	}
	for _, name := range []string{"Audience", "Roles", "Expiry"} {
		if !strings.Contains(out, name) {
			t.Errorf("output should still list the skipped %q check; got:\n%s", name, out)
		}
	}
}

// TestDoctorCommand_OutputJSON confirms -o json renders the full check list
// as a real JSON array. Uses a loopback endpoint, not a DNS name: the DNS
// check does a real net.DefaultResolver lookup (CheckDNS has no test-only
// injection seam at the CLI layer — doctor.go always wires the real
// resolver), and an unresolvable DNS name would mean an actual, slow
// network query in this sandbox; a literal IP resolves immediately with no
// real lookup at all.
func TestDoctorCommand_OutputJSON(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	out, err := runCmd(t, "doctor", "--gateway-endpoint", "https://127.0.0.1:1", "-o", "json")
	if err == nil {
		t.Fatal("expected an error (no credentials), but still expect valid JSON output")
	}
	var parsed []map[string]any
	if jsonErr := json.Unmarshal([]byte(out), &parsed); jsonErr != nil {
		t.Fatalf("output is not a JSON array: %v\n%s", jsonErr, out)
	}
	if len(parsed) == 0 {
		t.Fatal("expected at least one check in the JSON output")
	}
}

// noRoleJWT builds a JWT (same shape as staticJWTWithAudience,
// refresh_honesty_test.go) but deliberately omitting realm_access.roles —
// the exact "authenticated, but lacks the required role" scenario the Roles
// check exists to catch.
func noRoleJWT(t *testing.T, aud string) string {
	t.Helper()
	enc := func(v any) string {
		raw, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	payload := map[string]any{
		"iss": "https://issuer",
		"sub": "user-9",
		"aud": aud,
		"exp": float64(time.Now().Add(time.Hour).Unix()),
	}
	return enc(map[string]any{"alg": "RS256"}) + "." + enc(payload) + ".c2ln"
}

// TestDoctorCommand_RolesFail_Exit7 confirms a token with no gateway role
// fails the Roles check with exit code 7. Uses the cliDeps seam: a mock
// gateway whose CurrentUser returns an empty Roles slice so CheckRolesGateway
// (the authoritative path) fires; CheckRolesToken is advisory-only and would
// only skip, not fail, when roles are absent.
func TestDoctorCommand_RolesFail_Exit7(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"issuer":"https://issuer","audience":"openshell-cli"}`))
	}))
	defer srv.Close()

	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gw.EXPECT().
		CurrentUser(gomock.Any()).
		Return(&types.CurrentUser{Roles: []string{}}, nil)

	deps := cliDeps{
		Gateway:     gw,
		Target:      &gatewayconfig.Target{Name: "test", Endpoint: srv.URL},
		TokenSource: auth.NewStaticSource(noRoleJWT(t, "openshell-cli"), nil),
	}
	out, err := runCmdWithGateway(t, deps, "doctor")
	if err == nil {
		t.Fatalf("expected an error, got success:\n%s", out)
	}
	if exitCodeFor(err) != ExitForbidden {
		t.Errorf("exit = %d, want ExitForbidden (7)", exitCodeFor(err))
	}
	if !strings.Contains(out, "openshell-user") {
		t.Errorf("Roles failure should name the required role; got:\n%s", out)
	}
}

// TestDoctorCommand_ProviderFlag_DialsMockGateway confirms --provider wires
// through to the Providers check end to end via the Feature 0 cliDeps seam
// (mock.MockGateway) — this is only reachable with both Gateway and
// TokenSource injected together (resolveAuth couples the two; injecting
// only Gateway would mean a no-auth TokenSource whose empty token fails the
// Audience/Roles checks this test isn't trying to exercise), using a real
// JWT (staticJWT, tokenflow_test.go) so Credentials/Audience/Roles/Expiry
// all pass and only the Providers check is actually under test.
func TestDoctorCommand_ProviderFlag_DialsMockGateway(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"issuer":"https://issuer","audience":"openshell-cli"}`))
	}))
	defer srv.Close()

	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gw.EXPECT().
		CurrentUser(gomock.Any()).
		Return(&types.CurrentUser{Roles: []string{"openshell-user"}}, nil)
	gw.EXPECT().
		ListProviders(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]*types.Provider{{Name: "my-openai", Type: "openai"}}, nil)

	deps := cliDeps{
		Gateway:     gw,
		Target:      &gatewayconfig.Target{Name: "test", Endpoint: srv.URL},
		TokenSource: auth.NewStaticSource(staticJWT(t), nil),
	}
	out, err := runCmdWithGateway(t, deps, "doctor", "--provider", "my-openai")
	if err != nil {
		t.Fatalf("doctor --provider: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "Providers") || strings.Contains(out, "✗") {
		t.Errorf("expected the Providers check to pass; got:\n%s", out)
	}
}

// TestDoctorCommand_ProviderFlag_NotFound confirms a requested provider that
// doesn't exist on the gateway fails the Providers check with exit 2
// (ErrProviderNotFound-shaped, a usage/configuration problem).
func TestDoctorCommand_ProviderFlag_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"issuer":"https://issuer","audience":"openshell-cli"}`))
	}))
	defer srv.Close()

	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gw.EXPECT().
		CurrentUser(gomock.Any()).
		Return(&types.CurrentUser{Roles: []string{"openshell-user"}}, nil)
	gw.EXPECT().
		ListProviders(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, nil)

	deps := cliDeps{
		Gateway:     gw,
		Target:      &gatewayconfig.Target{Name: "test", Endpoint: srv.URL},
		TokenSource: auth.NewStaticSource(staticJWT(t), nil),
	}
	out, err := runCmdWithGateway(t, deps, "doctor", "--provider", "not-a-real-provider")
	if err == nil {
		t.Fatalf("expected an error, got success:\n%s", out)
	}
	if exitCodeFor(err) != ExitUsage {
		t.Errorf("exit = %d, want ExitUsage (2)", exitCodeFor(err))
	}
}
