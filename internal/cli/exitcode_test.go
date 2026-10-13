package cli

import (
	"errors"
	"testing"

	"github.com/openshift-online/openshellctl/pkg/auth"
	"github.com/openshift-online/openshellctl/pkg/doctor"
	"github.com/openshift-online/openshellctl/pkg/gateway"
	"github.com/openshift-online/openshellctl/pkg/gatewayconfig"
	"github.com/openshift-online/openshellctl/pkg/paramtemplate"
	"github.com/openshift-online/openshellctl/pkg/sandbox"
	"github.com/openshift-online/openshellctl/pkg/vaultconfig"
)

func TestExitCodeFor(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"nil is ok", nil, ExitOK},
		{"generic error", errors.New("boom"), ExitError},
		{"usage error", &UsageError{Err: errors.New("bad flag")}, ExitUsage},
		{"wrapped usage error", errWrap(&UsageError{Err: errors.New("bad flag")}), ExitUsage},
		{"not implemented is generic", &NotImplementedError{Command: "create"}, ExitError},

		// paramtemplate typed errors (ROSAENG-74244) — the CLI layer always
		// wraps these in *UsageError (sandbox_create_params.go), the same
		// convention every other --param/-f input-validation error in this
		// package already follows, so they map to ExitUsage like the rest.
		{"malformed --param → usage", &UsageError{Err: &paramtemplate.MalformedParamError{Key: "1BAD", HasEquals: true, Source: "--param"}}, ExitUsage},
		{"unreadable --param-file → usage", &UsageError{Err: &paramtemplate.ParamFileUnreadableError{Path: "p.txt", Err: errors.New("no such file")}}, ExitUsage},
		{"unresolved parameter reference → usage", &UsageError{Err: &paramtemplate.UnresolvedParamsError{Keys: []string{"NAME"}}}, ExitUsage},
		{"unknown parameter → usage", &UsageError{Err: &paramtemplate.UnknownParamsError{Keys: []string{"EXTRA"}}}, ExitUsage},

		// Gateway typed errors.
		{"gateway unauthenticated → auth", &gateway.UnauthenticatedError{Message: "no token"}, ExitAuth},
		{"gateway permission denied → forbidden", &gateway.PermissionDeniedError{Message: "role"}, ExitForbidden},
		{"gateway not found → notfound", &gateway.NotFoundError{Resource: "sandbox", Name: "x"}, ExitNotFound},
		{"wrapped gateway not found → notfound", errWrap(&gateway.NotFoundError{Name: "x"}), ExitNotFound},
		{"gateway already exists → conflict", &gateway.AlreadyExistsError{Name: "x"}, ExitConflict},
		{"gateway conflict → conflict", &gateway.ConflictError{Message: "modified"}, ExitConflict},
		{"gateway invalid argument → usage", &gateway.InvalidArgumentError{Message: "name exceeds maximum length (20 > 19)"}, ExitUsage},
		{"wrapped gateway invalid argument → usage", errWrap(&gateway.InvalidArgumentError{Message: "bad"}), ExitUsage},
		{"gateway unavailable → error", &gateway.UnavailableError{Message: "down"}, ExitError},
		{"gateway deadline exceeded → error", &gateway.DeadlineError{}, ExitError},
		{"gateway rpc error (catch-all) → error", &gateway.RPCError{Message: "huh"}, ExitError},

		// Sandbox provisioning / lifecycle errors.
		{"provision failed → provision", &sandbox.ErrProvisionFailed{Reason: "boom"}, ExitProvision},
		{"provision timeout → provision", &sandbox.ErrProvisionTimeout{}, ExitProvision},
		{"lifecycle error → provision", &sandbox.ErrLifecycle{Target: "Stopped"}, ExitProvision},
		{"lifecycle timeout → provision", &sandbox.ErrLifecycleTimeout{Target: "Ready"}, ExitProvision},
		{"delete timeout → provision", &sandbox.ErrDeleteTimeout{Name: "sb"}, ExitProvision},

		// doctor's ErrChecksFailed delegates to its own ExitCode() priority
		// table; these four confirm exitCodeFor actually calls it (the
		// table itself, including every check name, is exhaustively tested
		// in pkg/doctor/errors_test.go — see TestDoctorExitCodesMatchConstants
		// below for the cross-package value pinning).
		{"doctor roles check failed → forbidden", &doctor.ErrChecksFailed{Results: []doctor.CheckResult{{Name: "Roles", Status: doctor.StatusFail}}}, ExitForbidden},
		{"doctor credentials check failed → auth", &doctor.ErrChecksFailed{Results: []doctor.CheckResult{{Name: "Credentials", Status: doctor.StatusFail}}}, ExitAuth},
		{"doctor endpoint url check failed → usage", &doctor.ErrChecksFailed{Results: []doctor.CheckResult{{Name: "Endpoint URL", Status: doctor.StatusFail}}}, ExitUsage},
		{"doctor dns check failed → error", &doctor.ErrChecksFailed{Results: []doctor.CheckResult{{Name: "DNS", Status: doctor.StatusFail}}}, ExitError},

		// gatewayconfig typed errors (gateway add/list/select/remove/logout).
		{"gateway exists → conflict", &gatewayconfig.GatewayExistsError{Name: "x"}, ExitConflict},
		{"edge gateway unsupported → usage", &gatewayconfig.EdgeGatewayUnsupportedError{Endpoint: "https://x"}, ExitUsage},
		{"mtls unsupported → usage", &gatewayconfig.MTLSUnsupportedError{}, ExitUsage},
		{"invalid endpoint → usage", &gatewayconfig.InvalidEndpointError{Endpoint: "x", Cause: errors.New("bad")}, ExitUsage},
		{"invalid gateway name → usage", &gatewayconfig.InvalidGatewayNameError{Name: "a/b"}, ExitUsage},
		{"metadata parse error → usage", &gatewayconfig.MetadataParseError{Name: "x", Cause: errors.New("bad json")}, ExitUsage},

		// pkg/auth typed errors.
		{"token expired → auth", &auth.ErrTokenExpired{}, ExitAuth},
		{"exchange error → auth", &auth.ExchangeError{Cause: errors.New("boom")}, ExitAuth},
		{"oidc config missing → auth", &auth.ErrOIDCConfigMissing{}, ExitAuth},
		{"bundle invalid → auth", &auth.ErrBundleInvalid{Reason: "bad"}, ExitAuth},
		{"mtls material missing → auth", &auth.ErrMTLSMaterialMissing{Gateway: "x"}, ExitAuth},
		{"unsupported auth mode → usage", &auth.ErrUnsupportedAuthMode{Mode: "cloudflare_jwt"}, ExitUsage},
		{"no expiry (sentinel) → auth", auth.ErrNoExpiry, ExitAuth},
		{"no credentials → auth", &auth.ErrNoCredentials{}, ExitAuth},
		{"wrapped no credentials → auth", errWrap(&auth.ErrNoCredentials{}), ExitAuth},
		{"nothing to refresh → auth", &auth.ErrNothingToRefresh{}, ExitAuth},
		{"wrapped nothing to refresh → auth", errWrap(&auth.ErrNothingToRefresh{}), ExitAuth},
		{"not a jwt (sentinel) → usage", auth.ErrNotJWT, ExitUsage},

		// pkg/vaultconfig typed errors (Vault auth config source).
		{"vault no token → auth", &vaultconfig.ErrNoToken{}, ExitAuth},
		{"wrapped vault no token → auth", errWrap(&vaultconfig.ErrNoToken{}), ExitAuth},
		{"vault addr not set → auth", &vaultconfig.ErrVaultAddrNotSet{}, ExitAuth},
		{"vault forbidden → forbidden", &vaultconfig.ErrForbidden{Mount: "osd-sre", Path: "rosa-agent"}, ExitForbidden},
		{"vault secret not found → notfound", &vaultconfig.ErrSecretNotFound{Mount: "osd-sre", Path: "rosa-agent"}, ExitNotFound},
		{"vault field not string → usage", &vaultconfig.ErrFieldNotString{Field: "oidc-client-id"}, ExitUsage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exitCodeFor(tt.err); got != tt.want {
				t.Errorf("exitCodeFor(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

type wrapErr struct{ err error }

func (w wrapErr) Error() string { return "wrapped: " + w.err.Error() }
func (w wrapErr) Unwrap() error { return w.err }

func errWrap(e error) error { return wrapErr{err: e} }

func TestNotImplementedError_Message(t *testing.T) {
	e := &NotImplementedError{Command: "exec"}
	if e.Error() != "exec: not implemented yet" {
		t.Errorf("unexpected message: %q", e.Error())
	}
}

// TestDoctorExitCodesMatchConstants pins doctor.ErrChecksFailed.ExitCode()'s
// locally-mirrored exit-code values (pkg/doctor/errors.go — duplicated
// there, not imported, since this package imports pkg/doctor and the
// reverse would cycle) against this package's real constants, for every
// check name doctor's priority table branches on. If the two ever drift
// apart, this test — not a confusing exit-code mismatch at runtime — is
// where it surfaces.
func TestDoctorExitCodesMatchConstants(t *testing.T) {
	tests := []struct {
		checkName string
		want      int
	}{
		{"Roles", ExitForbidden},
		{"Credentials", ExitAuth},
		{"Audience", ExitAuth},
		{"Expiry", ExitAuth},
		{"Endpoint URL", ExitUsage},
		{"Providers", ExitUsage},
		{"DNS", ExitError},
		{"HTTP reachability", ExitError},
		{"OIDC config match", ExitError},
	}
	for _, tt := range tests {
		t.Run(tt.checkName, func(t *testing.T) {
			err := &doctor.ErrChecksFailed{Results: []doctor.CheckResult{{Name: tt.checkName, Status: doctor.StatusFail}}}
			if got := err.ExitCode(); got != tt.want {
				t.Errorf("ErrChecksFailed{%s failed}.ExitCode() = %d, want %d (this package's constant)", tt.checkName, got, tt.want)
			}
		})
	}
}
