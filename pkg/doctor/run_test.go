package doctor

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	types "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"

	"github.com/openshift-online/openshellctl/pkg/auth"
	"github.com/openshift-online/openshellctl/pkg/gateway"
	"github.com/openshift-online/openshellctl/pkg/gatewayconfig"
)

func names(results []CheckResult) []string {
	out := make([]string, len(results))
	for i, r := range results {
		out[i] = r.Name
	}
	return out
}

func statusOf(results []CheckResult, name string) Status {
	for _, r := range results {
		if r.Name == name {
			return r.Status
		}
	}
	return ""
}

// fakeTokenSource is a minimal auth.TokenSource for Run's tests.
type fakeTokenSource struct {
	tok *auth.Token
	err error
}

func (f *fakeTokenSource) Token(context.Context) (*auth.Token, error) { return f.tok, f.err }
func (f *fakeTokenSource) Invalidate()                                {}
func (f *fakeTokenSource) Describe() string                           { return "fake" }

func wantResolvedTarget() *gatewayconfig.Target {
	return &gatewayconfig.Target{
		Name:     "rosa",
		Endpoint: "https://gw.example.com:443",
		Resolved: &gatewayconfig.Resolved{Name: "rosa", Metadata: gatewayconfig.Metadata{}},
	}
}

func fullHappyDeps(t *testing.T) Deps {
	t.Helper()
	validToken := &auth.Token{
		Subject: "svc", Audience: []string{"openshell-cli"}, Roles: []string{"openshell-user"},
		Expiry: time.Now().Add(time.Hour),
	}
	src := &fakeTokenSource{tok: validToken}
	target := wantResolvedTarget()
	return Deps{
		ResolveAuth: func(context.Context) (auth.TokenSource, *gatewayconfig.Target, error) {
			return src, target, nil
		},
		LookupIP: func(context.Context, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("10.0.0.1")}, nil
		},
		HTTPGet: func(context.Context, string) (*http.Response, error) {
			return httpJSONResponse(200, `{"issuer":"https://issuer","audience":"openshell-cli"}`), nil
		},
		WantAudience: "openshell-cli",
	}
}

// TestRun_Order confirms the fixed check sequence and that a fully-healthy
// Deps produces nothing but passes (and a skip for the not-requested
// Providers check).
func TestRun_Order(t *testing.T) {
	results := Run(context.Background(), fullHappyDeps(t))
	want := []string{
		"Endpoint URL", "DNS", "HTTP reachability", "Credentials",
		"Audience", "Roles", "Expiry", "OIDC config match", "Providers",
	}
	if got := names(results); !equalStrings(got, want) {
		t.Fatalf("check order = %v, want %v", got, want)
	}
	for _, r := range results {
		if r.Name == "Providers" {
			if r.Status != StatusSkip {
				t.Errorf("Providers = %v, want skip (nothing requested)", r.Status)
			}
			continue
		}
		if r.Status != StatusPass {
			t.Errorf("%s = %v, want pass (detail: %s)", r.Name, r.Status, r.Detail)
		}
	}
}

// TestRun_CredentialsFailureSkipsDownstream is the ticket's second
// acceptance criterion: with credentials failing, Audience/Roles/Expiry are
// skipped (not failed, not silently absent), and the Credentials check's
// detail shows the checked-sources list from auth.ErrNoCredentials.
func TestRun_CredentialsFailureSkipsDownstream(t *testing.T) {
	d := fullHappyDeps(t)
	d.ResolveAuth = func(context.Context) (auth.TokenSource, *gatewayconfig.Target, error) {
		return nil, wantResolvedTarget(), &auth.ErrNoCredentials{
			Endpoint: "https://gw.example.com:443",
			Checked:  []string{"--token / OPENSHELL_TOKEN", "OPENSHELL_OIDC_CLIENT_SECRET (or --client-secret-file)"},
		}
	}
	results := Run(context.Background(), d)

	cred := results[3]
	if cred.Name != "Credentials" || cred.Status != StatusFail {
		t.Fatalf("Credentials = %+v, want a fail", cred)
	}
	if !strings.Contains(cred.Detail, "--token / OPENSHELL_TOKEN") {
		t.Errorf("Credentials.Detail = %q, want it to show the checked-sources list", cred.Detail)
	}
	if cred.NextStep == "" {
		t.Error("Credentials fail should have a NextStep (copy-pasteable next command)")
	}

	for _, name := range []string{"Audience", "Roles", "Expiry"} {
		if got := statusOf(results, name); got != StatusSkip {
			t.Errorf("%s = %v, want skip (Credentials failed)", name, got)
		}
	}
}

// TestRun_NilDeps confirms nil-safety: every injectable func left nil
// produces a skip/fail result for the checks that need it, never a panic.
func TestRun_NilDeps(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Run panicked with nil Deps: %v", r)
		}
	}()
	results := Run(context.Background(), Deps{})
	if len(results) == 0 {
		t.Fatal("expected results even with a fully nil Deps")
	}
	// Endpoint URL fails (no ResolveAuth → no endpoint); everything else
	// cascades to skip/fail, but must not panic.
	if statusOf(results, "Endpoint URL") != StatusFail {
		t.Errorf("Endpoint URL = %v, want fail (no endpoint resolvable)", statusOf(results, "Endpoint URL"))
	}
}

// TestRun_ProvidersRequested_DialsAndChecks confirms the Providers check
// dials the gateway (only when something was actually requested) and lists
// providers via Deps.ListProviders.
func TestRun_ProvidersRequested_DialsAndChecks(t *testing.T) {
	d := fullHappyDeps(t)
	d.RequestedProviders = []string{"my-openai"}
	dialed := false
	d.Dial = func(context.Context, *gatewayconfig.Target, auth.TokenSource) (gateway.Gateway, io.Closer, error) {
		dialed = true
		return nil, io.NopCloser(nil), nil
	}
	d.ListProviders = func(context.Context, gateway.Gateway, string) ([]*types.Provider, error) {
		return []*types.Provider{{Name: "my-openai", Type: "openai"}}, nil
	}
	results := Run(context.Background(), d)
	if !dialed {
		t.Error("expected the gateway to be dialed for the Providers check")
	}
	if got := statusOf(results, "Providers"); got != StatusPass {
		t.Errorf("Providers = %v, want pass", got)
	}
}

// TestRun_ProvidersRequestedButCredentialsFailed confirms Providers is
// skipped (not dialed at all) when Credentials already failed — there's no
// token to dial with.
func TestRun_ProvidersRequestedButCredentialsFailed(t *testing.T) {
	d := fullHappyDeps(t)
	d.RequestedProviders = []string{"my-openai"}
	d.ResolveAuth = func(context.Context) (auth.TokenSource, *gatewayconfig.Target, error) {
		return nil, wantResolvedTarget(), &auth.ErrNoCredentials{}
	}
	dialed := false
	d.Dial = func(context.Context, *gatewayconfig.Target, auth.TokenSource) (gateway.Gateway, io.Closer, error) {
		dialed = true
		return nil, io.NopCloser(nil), nil
	}
	results := Run(context.Background(), d)
	if dialed {
		t.Error("Providers must not dial when Credentials already failed")
	}
	if got := statusOf(results, "Providers"); got != StatusSkip {
		t.Errorf("Providers = %v, want skip", got)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestErrChecksFailed_Error(t *testing.T) {
	err := &ErrChecksFailed{Results: []CheckResult{
		{Name: "A", Status: StatusPass},
		{Name: "B", Status: StatusFail},
		{Name: "C", Status: StatusFail},
		{Name: "D", Status: StatusSkip},
	}}
	if !strings.Contains(err.Error(), "2") {
		t.Errorf("Error() = %q, want it to mention 2 failed checks", err.Error())
	}
}

func TestErrChecksFailed_ExitCode(t *testing.T) {
	tests := []struct {
		name       string
		failedName string
		want       int
	}{
		{"roles failure -> forbidden", "Roles", 7},
		{"credentials failure -> auth", "Credentials", 3},
		{"audience failure -> auth", "Audience", 3},
		{"expiry failure -> auth", "Expiry", 3},
		{"endpoint url failure -> usage", "Endpoint URL", 2},
		{"providers failure -> usage", "Providers", 2},
		{"dns failure -> generic error", "DNS", 1},
		{"http failure -> generic error", "HTTP reachability", 1},
		{"oidc config match failure -> generic error", "OIDC config match", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &ErrChecksFailed{Results: []CheckResult{{Name: tt.failedName, Status: StatusFail}}}
			if got := err.ExitCode(); got != tt.want {
				t.Errorf("ExitCode() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestErrChecksFailed_ExitCode_AllPassIsZero(t *testing.T) {
	err := &ErrChecksFailed{Results: []CheckResult{{Name: "A", Status: StatusPass}}}
	if got := err.ExitCode(); got != 0 {
		t.Errorf("ExitCode() = %d, want 0 when nothing failed", got)
	}
}

// TestErrChecksFailed_ExitCode_PriorityAmongMultipleFailures locks in the
// priority ordering among *simultaneous* failures — TestErrChecksFailed_ExitCode
// above only ever constructs a single failing check at a time, which could
// not catch a bug in the switch's branch ordering (e.g. a later case
// shadowing an earlier, higher-priority one). Each case here fails two
// checks from different priority tiers at once and asserts the
// higher-priority tier's code wins, regardless of slice order.
func TestErrChecksFailed_ExitCode_PriorityAmongMultipleFailures(t *testing.T) {
	fail := func(name string) CheckResult { return CheckResult{Name: name, Status: StatusFail} }
	tests := []struct {
		name    string
		results []CheckResult
		want    int
	}{
		{
			name:    "Roles beats Credentials regardless of order",
			results: []CheckResult{fail("Credentials"), fail("Roles")},
			want:    exitForbidden,
		},
		{
			name:    "Roles beats Credentials, reverse order",
			results: []CheckResult{fail("Roles"), fail("Credentials")},
			want:    exitForbidden,
		},
		{
			name:    "Credentials beats Endpoint URL",
			results: []CheckResult{fail("Endpoint URL"), fail("Credentials")},
			want:    exitAuth,
		},
		{
			name:    "Endpoint URL beats DNS",
			results: []CheckResult{fail("DNS"), fail("Endpoint URL")},
			want:    exitUsage,
		},
		{
			name:    "all four tiers at once -> Roles still wins",
			results: []CheckResult{fail("DNS"), fail("Endpoint URL"), fail("Credentials"), fail("Roles")},
			want:    exitForbidden,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &ErrChecksFailed{Results: tt.results}
			if got := err.ExitCode(); got != tt.want {
				t.Errorf("ExitCode() = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestHostOf covers hostOf's branches directly (Run only calls it once
// CheckEndpointURL has already confirmed the endpoint normalizes, so the
// malformed-input branch isn't reachable through Run itself).
func TestHostOf(t *testing.T) {
	tests := []struct{ endpoint, want string }{
		{"https://gw.example.com:443", "gw.example.com"},
		{"gw.example.com", "gw.example.com"},
		{"https://gw.example.com:notaport", ""},
	}
	for _, tt := range tests {
		if got := hostOf(tt.endpoint); got != tt.want {
			t.Errorf("hostOf(%q) = %q, want %q", tt.endpoint, got, tt.want)
		}
	}
}

// TestDeps_DefaultNow confirms the nil-Now fallback to time.Now is actually
// exercised (table tests elsewhere always inject a fixed clock).
func TestDeps_DefaultNow(t *testing.T) {
	d := Deps{}
	before := time.Now()
	got := d.now()
	after := time.Now()
	if got.Before(before) || got.After(after) {
		t.Errorf("now() = %v, want between %v and %v", got, before, after)
	}
}

// TestCheckCredentials_NilTokenNilErr covers the defensive "minted nothing,
// but no error either" branch — not reachable through Run's real
// TokenSource.Token contract, but CheckCredentials is a pure function and
// should degrade safely regardless.
func TestCheckCredentials_NilTokenNilErr(t *testing.T) {
	got := CheckCredentials(nil, nil)
	if got.Status != StatusFail {
		t.Errorf("Status = %v, want fail", got.Status)
	}
	if got.NextStep == "" {
		t.Error("expected a NextStep")
	}
}

// TestRun_Roles_GatewayAuthoritative is the ticket's hyp2 case: the gateway
// CurrentUser returns [openshell-user, openshell-admin] while the JWT token
// claims are empty — Roles must pass from the gateway result, not the token.
func TestRun_Roles_GatewayAuthoritative(t *testing.T) {
	d := fullHappyDeps(t)
	noRoleToken := &auth.Token{
		Subject: "svc", Audience: []string{"openshell-cli"}, Roles: []string{},
		Expiry: time.Now().Add(time.Hour),
	}
	src := &fakeTokenSource{tok: noRoleToken}
	target := wantResolvedTarget()
	d.ResolveAuth = func(context.Context) (auth.TokenSource, *gatewayconfig.Target, error) {
		return src, target, nil
	}
	d.Dial = func(context.Context, *gatewayconfig.Target, auth.TokenSource) (gateway.Gateway, io.Closer, error) {
		return nil, io.NopCloser(nil), nil
	}
	d.CurrentUser = func(_ context.Context, _ gateway.Gateway) (*types.CurrentUser, error) {
		return &types.CurrentUser{Roles: []string{"openshell-user", "openshell-admin"}}, nil
	}

	results := Run(context.Background(), d)
	if got := statusOf(results, "Roles"); got != StatusPass {
		t.Errorf("Roles = %v, want pass (gateway returned openshell-user, openshell-admin; JWT claims empty)", got)
	}
}

// TestRun_Roles_GatewayFail_ExitForbidden confirms that an empty gateway role
// list fails Roles and that ErrChecksFailed maps it to exitForbidden (7).
func TestRun_Roles_GatewayFail_ExitForbidden(t *testing.T) {
	d := fullHappyDeps(t)
	d.Dial = func(context.Context, *gatewayconfig.Target, auth.TokenSource) (gateway.Gateway, io.Closer, error) {
		return nil, io.NopCloser(nil), nil
	}
	d.CurrentUser = func(_ context.Context, _ gateway.Gateway) (*types.CurrentUser, error) {
		return &types.CurrentUser{Roles: []string{}}, nil
	}

	results := Run(context.Background(), d)
	if got := statusOf(results, "Roles"); got != StatusFail {
		t.Errorf("Roles = %v, want fail (gateway returned no gateway roles)", got)
	}
	err := &ErrChecksFailed{Results: results}
	if got := err.ExitCode(); got != exitForbidden {
		t.Errorf("ExitCode() = %d, want %d (exitForbidden)", got, exitForbidden)
	}
}

// TestRun_Roles_DialError_TokenClaimsEmpty_Skips confirms a dial error with
// empty JWT claims produces StatusSkip (advisory only), not StatusFail —
// hsctl-managed gateways assign roles gateway-side, so absent JWT claims
// don't prove permission is missing.
func TestRun_Roles_DialError_TokenClaimsEmpty_Skips(t *testing.T) {
	d := fullHappyDeps(t)
	noRoleToken := &auth.Token{
		Subject: "svc", Audience: []string{"openshell-cli"}, Roles: []string{},
		Expiry: time.Now().Add(time.Hour),
	}
	src := &fakeTokenSource{tok: noRoleToken}
	target := wantResolvedTarget()
	d.ResolveAuth = func(context.Context) (auth.TokenSource, *gatewayconfig.Target, error) {
		return src, target, nil
	}
	d.Dial = func(context.Context, *gatewayconfig.Target, auth.TokenSource) (gateway.Gateway, io.Closer, error) {
		return nil, nil, errors.New("gateway unreachable")
	}
	d.CurrentUser = func(_ context.Context, _ gateway.Gateway) (*types.CurrentUser, error) {
		t.Error("CurrentUser must not be called when Dial fails")
		return nil, nil
	}

	results := Run(context.Background(), d)
	if got := statusOf(results, "Roles"); got != StatusSkip {
		t.Errorf("Roles = %v, want skip (dial failed, JWT claims empty — advisory only)", got)
	}
}

// TestRun_Roles_DialError_TokenClaimsPresent_Passes confirms the reliable
// positive fallback: if the gateway is unreachable but JWT has openshell-user,
// CheckRolesToken passes (a positive claim is reliable even without the gateway).
func TestRun_Roles_DialError_TokenClaimsPresent_Passes(t *testing.T) {
	d := fullHappyDeps(t) // token has Roles: []string{"openshell-user"}
	d.Dial = func(context.Context, *gatewayconfig.Target, auth.TokenSource) (gateway.Gateway, io.Closer, error) {
		return nil, nil, errors.New("gateway unreachable")
	}
	d.CurrentUser = func(_ context.Context, _ gateway.Gateway) (*types.CurrentUser, error) {
		t.Error("CurrentUser must not be called when Dial fails")
		return nil, nil
	}

	results := Run(context.Background(), d)
	if got := statusOf(results, "Roles"); got != StatusPass {
		t.Errorf("Roles = %v, want pass (dial failed but JWT has openshell-user)", got)
	}
}

// TestRun_Roles_CurrentUserError_FallsBackToToken confirms that when Dial
// succeeds but CurrentUser returns an error, Run falls back to CheckRolesToken
// rather than failing the check outright — the gateway may accept the call
// but not yet have the caller's profile indexed.
func TestRun_Roles_CurrentUserError_FallsBackToToken(t *testing.T) {
	// Subtest A: token has no roles → skip (advisory).
	t.Run("token claims empty -> skip", func(t *testing.T) {
		d := fullHappyDeps(t)
		noRoleToken := &auth.Token{
			Subject: "svc", Audience: []string{"openshell-cli"}, Roles: []string{},
			Expiry: time.Now().Add(time.Hour),
		}
		src := &fakeTokenSource{tok: noRoleToken}
		target := wantResolvedTarget()
		d.ResolveAuth = func(context.Context) (auth.TokenSource, *gatewayconfig.Target, error) {
			return src, target, nil
		}
		d.Dial = func(context.Context, *gatewayconfig.Target, auth.TokenSource) (gateway.Gateway, io.Closer, error) {
			return nil, io.NopCloser(nil), nil
		}
		d.CurrentUser = func(_ context.Context, _ gateway.Gateway) (*types.CurrentUser, error) {
			return nil, errors.New("rpc: not found")
		}
		results := Run(context.Background(), d)
		if got := statusOf(results, "Roles"); got != StatusSkip {
			t.Errorf("Roles = %v, want skip (CurrentUser errored, JWT claims empty)", got)
		}
	})

	// Subtest B: token has openshell-user → pass via token fallback.
	t.Run("token claims present -> pass", func(t *testing.T) {
		d := fullHappyDeps(t) // token has Roles: []string{"openshell-user"}
		d.Dial = func(context.Context, *gatewayconfig.Target, auth.TokenSource) (gateway.Gateway, io.Closer, error) {
			return nil, io.NopCloser(nil), nil
		}
		d.CurrentUser = func(_ context.Context, _ gateway.Gateway) (*types.CurrentUser, error) {
			return nil, errors.New("rpc: not found")
		}
		results := Run(context.Background(), d)
		if got := statusOf(results, "Roles"); got != StatusPass {
			t.Errorf("Roles = %v, want pass (CurrentUser errored, JWT has openshell-user)", got)
		}
	})
}

// TestRun_RolesAndProviders_SharedDial confirms the single-dial guarantee:
// when Roles dials successfully (both Dial and CurrentUser wired), Providers
// reuses the same connection rather than opening a second one.
func TestRun_RolesAndProviders_SharedDial(t *testing.T) {
	d := fullHappyDeps(t)
	d.RequestedProviders = []string{"my-openai"}
	dialCount := 0
	d.Dial = func(context.Context, *gatewayconfig.Target, auth.TokenSource) (gateway.Gateway, io.Closer, error) {
		dialCount++
		return nil, io.NopCloser(nil), nil
	}
	d.CurrentUser = func(_ context.Context, _ gateway.Gateway) (*types.CurrentUser, error) {
		return &types.CurrentUser{Roles: []string{"openshell-user"}}, nil
	}
	d.ListProviders = func(_ context.Context, _ gateway.Gateway, _ string) ([]*types.Provider, error) {
		return []*types.Provider{{Name: "my-openai", Type: "openai"}}, nil
	}

	results := Run(context.Background(), d)
	if dialCount != 1 {
		t.Errorf("Dial called %d times, want exactly 1 (shared between Roles and Providers)", dialCount)
	}
	if got := statusOf(results, "Roles"); got != StatusPass {
		t.Errorf("Roles = %v, want pass", got)
	}
	if got := statusOf(results, "Providers"); got != StatusPass {
		t.Errorf("Providers = %v, want pass", got)
	}
}

// TestRun_ProvidersRequested_ListProvidersNotWired covers the dial-succeeds-
// but-ListProviders-nil branch.
func TestRun_ProvidersRequested_ListProvidersNotWired(t *testing.T) {
	d := fullHappyDeps(t)
	d.RequestedProviders = []string{"my-openai"}
	d.Dial = func(context.Context, *gatewayconfig.Target, auth.TokenSource) (gateway.Gateway, io.Closer, error) {
		return nil, io.NopCloser(nil), nil
	}
	// d.ListProviders deliberately left nil.
	results := Run(context.Background(), d)
	if got := statusOf(results, "Providers"); got != StatusSkip {
		t.Errorf("Providers = %v, want skip (ListProviders not wired)", got)
	}
}
