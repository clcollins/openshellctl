package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/openshift-online/openshellctl/pkg/auth"
	"github.com/openshift-online/openshellctl/pkg/doctor"
	"github.com/openshift-online/openshellctl/pkg/gateway"
	"github.com/openshift-online/openshellctl/pkg/gatewayconfig"
	"github.com/openshift-online/openshellctl/pkg/sandbox"
	"github.com/openshift-online/openshellctl/pkg/transfer"
	"github.com/openshift-online/openshellctl/pkg/vaultconfig"
)

// Exit codes, per the implementation spec §5.9.
const (
	ExitOK        = 0 // success
	ExitError     = 1 // generic / RPC error
	ExitUsage     = 2 // usage: flag parse, validation, unsupported flag combos
	ExitAuth      = 3 // authentication failures (unauthenticated, expired, no credentials)
	ExitNotFound  = 4 // sandbox/gateway not found
	ExitConflict  = 5 // conflict / already-exists
	ExitProvision = 6 // provisioning timeout/failure, lifecycle
	ExitForbidden = 7 // authenticated, but not authorized (permission denied)
)

// UsageError marks an error as a usage error (exit code 2). Command layers wrap
// flag/validation problems in this so Execute maps them to ExitUsage.
type UsageError struct{ Err error }

func (e *UsageError) Error() string { return e.Err.Error() }
func (e *UsageError) Unwrap() error { return e.Err }

// NotImplementedError is returned by command stubs that are not yet wired up.
// It maps to ExitError. It exists so the full command tree can be built (and
// enumerated by the parity test) before every command is implemented.
type NotImplementedError struct{ Command string }

func (e *NotImplementedError) Error() string {
	return e.Command + ": not implemented yet"
}

// RemoteExitError carries the exit code of a remote process (exec/connect/create
// with an attached command). It maps directly to that code so the local process
// exits with the remote's status. It is only constructed for non-zero codes.
type RemoteExitError struct{ Code int }

func (e *RemoteExitError) Error() string {
	return fmt.Sprintf("remote process exited with status %d", e.Code)
}

// exitCodeError returns a *RemoteExitError for a non-zero remote code, or nil for
// zero (so the command succeeds silently on exit 0).
func exitCodeError(code int) error {
	if code == 0 {
		return nil
	}
	return &RemoteExitError{Code: code}
}

// exitCodeFor maps an error returned by a command to a process exit code.
// As typed errors from pkg/* land in later commits, they are classified here.
func exitCodeFor(err error) int {
	if err == nil {
		return ExitOK
	}
	var remote *RemoteExitError
	if errors.As(err, &remote) {
		return remote.Code
	}
	var usage *UsageError
	if errors.As(err, &usage) {
		return ExitUsage
	}

	// doctor's own priority table among its failing checks (pkg/doctor/
	// errors.go) — checked early since it's a self-contained decision, not
	// competing with the buckets below.
	var checksFailed *doctor.ErrChecksFailed
	if errors.As(err, &checksFailed) {
		return checksFailed.ExitCode()
	}

	// SSH channel closed without exit-status (transport loss mid-attach) → 1.
	// Checked before all gateway error buckets: AttachClosedError.Unwrap()
	// exposes the tunnel error, and if that tunnel error is itself a gateway
	// type (e.g. UnauthenticatedError on a future SSHTunnel that classifies
	// errors), errors.As would otherwise reach through and misclassify
	// transport loss as an auth or permission failure.
	var attachClosed *transfer.AttachClosedError
	if errors.As(err, &attachClosed) {
		return ExitError
	}

	// Permission denied → 7 (authenticated, but not authorized). Checked
	// before the generic auth bucket below since it is NOT an auth failure.
	var gwPerm *gateway.PermissionDeniedError
	var vaultForbidden *vaultconfig.ErrForbidden
	if errors.As(err, &gwPerm) || errors.As(err, &vaultForbidden) {
		return ExitForbidden
	}

	// Authentication failures → 3 (missing, expired, or otherwise unusable
	// credentials — never an authorization/permission problem).
	var expired *auth.ErrTokenExpired
	var exchange *auth.ExchangeError
	var oidcMissing *auth.ErrOIDCConfigMissing
	var gwUnauth *gateway.UnauthenticatedError
	var bundleInvalid *auth.ErrBundleInvalid
	var mtlsMissing *auth.ErrMTLSMaterialMissing
	var nothingToRefresh *auth.ErrNothingToRefresh
	var noCreds *auth.ErrNoCredentials
	var vaultNoToken *vaultconfig.ErrNoToken
	var vaultAddrNotSet *vaultconfig.ErrVaultAddrNotSet
	if errors.As(err, &expired) || errors.As(err, &exchange) || errors.As(err, &oidcMissing) ||
		errors.As(err, &gwUnauth) || errors.As(err, &bundleInvalid) || errors.As(err, &mtlsMissing) ||
		errors.As(err, &nothingToRefresh) || errors.As(err, &noCreds) || errors.Is(err, auth.ErrNoExpiry) ||
		errors.As(err, &vaultNoToken) || errors.As(err, &vaultAddrNotSet) {
		return ExitAuth
	}

	// Not found → 4 (gateway-config resolution, gateway RPC NotFound, and a
	// missing Vault secret).
	var gwNotFound *gateway.NotFoundError
	var vaultNotFound *vaultconfig.ErrSecretNotFound
	if errors.Is(err, gatewayconfig.ErrGatewayNotFound) ||
		errors.Is(err, gatewayconfig.ErrUnknownGateway) ||
		errors.Is(err, gatewayconfig.ErrNoActiveGateway) ||
		errors.As(err, &gwNotFound) || errors.As(err, &vaultNotFound) {
		return ExitNotFound
	}

	// Invalid argument → 2 (gateway-rejected validation, e.g. name length).
	var gwInvalidArg *gateway.InvalidArgumentError
	if errors.As(err, &gwInvalidArg) {
		return ExitUsage
	}

	// gatewayconfig usage errors → 2 (bad input to gateway add/select/remove,
	// or corrupt local config/an auth mode this client doesn't support).
	var metadataParse *gatewayconfig.MetadataParseError
	var unsupportedAuthMode *auth.ErrUnsupportedAuthMode
	var vaultFieldNotString *vaultconfig.ErrFieldNotString
	if errors.Is(err, gatewayconfig.ErrInvalidGatewayName) ||
		errors.Is(err, gatewayconfig.ErrEdgeGatewayUnsupported) ||
		errors.Is(err, gatewayconfig.ErrMTLSUnsupported) ||
		errors.Is(err, gatewayconfig.ErrInvalidEndpoint) ||
		errors.Is(err, auth.ErrNotJWT) ||
		errors.As(err, &metadataParse) || errors.As(err, &unsupportedAuthMode) ||
		errors.As(err, &vaultFieldNotString) {
		return ExitUsage
	}

	// Conflict / already-exists → 5.
	var gwExists *gateway.AlreadyExistsError
	var gwConflict *gateway.ConflictError
	if errors.As(err, &gwExists) || errors.As(err, &gwConflict) || errors.Is(err, gatewayconfig.ErrGatewayExists) {
		return ExitConflict
	}

	// Provisioning / lifecycle → 6.
	var provFailed *sandbox.ErrProvisionFailed
	var provTimeout *sandbox.ErrProvisionTimeout
	var lifecycle *sandbox.ErrLifecycle
	var lifecycleTimeout *sandbox.ErrLifecycleTimeout
	var lifecycleEnded *sandbox.ErrLifecycleStreamEnded
	var deleteTimeout *sandbox.ErrDeleteTimeout
	if errors.As(err, &provFailed) || errors.As(err, &provTimeout) ||
		errors.As(err, &lifecycle) || errors.As(err, &lifecycleTimeout) ||
		errors.As(err, &lifecycleEnded) || errors.As(err, &deleteTimeout) {
		return ExitProvision
	}

	// -f URL manifest fetch failures (non-2xx, size cap, redirect downgrade,
	// network/timeout) → 1, closer to a network failure than a usage error.
	// Listed explicitly (rather than only falling into the catch-all below)
	// so a future change to either type is caught by this function's own
	// exhaustive-by-construction test.
	var manifestFetch *manifestFetchError
	var manifestSize *manifestURLSizeError
	if errors.As(err, &manifestFetch) || errors.As(err, &manifestSize) {
		return ExitError
	}

	// Everything else — including gateway.UnavailableError, DeadlineError,
	// and the RPCError catch-all — is a generic/connectivity error → 1.
	return ExitError
}

// refreshHint is the generic "obtain a new token" hint, used whenever the
// problem really is a stale/missing bearer token refresh can fix. It is a
// named value (not just inlined) so root.go's existing message-substring
// suppression (don't repeat advice the error already gives) can be scoped
// to this specific hint rather than every hint hintFor returns.
const refreshHint = "Hint: try `openshellctl token refresh` to obtain a new token."

// hintFor returns an actionable, one-line hint for an error, or "" when none
// applies. It is keyed on error type, not on the resulting exit code, so
// that semantically different problems that happen to share an exit code
// (e.g. an expired token vs. a permission-denied response, both auth-ish)
// get distinct, accurate hints rather than one generic message.
func hintFor(err error) string {
	if err == nil {
		return ""
	}

	// Permission-denied is authorization, not expiry — re-running `token
	// refresh` cannot fix it, so it must never get the refresh hint. Name
	// the two concrete fixes that actually resolve this in practice: a
	// human account needs the openshell-user realm role (openshell-admin
	// also satisfies it); a service account must use its own OIDC client
	// ID, not the shared openshell-cli default.
	var gwPerm *gateway.PermissionDeniedError
	if errors.As(err, &gwPerm) {
		return "Hint: you are authenticated, but the gateway denied this action due to insufficient " +
			"permissions — obtaining a new token will not help. A human account needs the " +
			"\"openshell-user\" realm role (\"openshell-admin\" also satisfies it); a service account " +
			"must use its own OIDC client ID, not the shared \"openshell-cli\" default. Ask an " +
			"administrator to grant the required role or provision a dedicated client."
	}

	// ErrNoCredentials means nothing was ever configured — the generic
	// refresh hint would just send the user in a circle back to this exact
	// error (`token refresh` resolves auth the same way every other command
	// does, so it has nothing to refresh either). Point at what actually
	// fixes it instead: provide the credential.
	var noCreds *auth.ErrNoCredentials
	if errors.As(err, &noCreds) {
		return "Hint: no credentials are configured for this gateway. For a service account, export " +
			"OPENSHELL_OIDC_CLIENT_SECRET (or pass --client-secret-file); for a human, run " +
			"`openshellctl login` (add -g <name> for a specific gateway) to authenticate via browser. " +
			"Alternatively, point --vault-kv-mount/--vault-kv-path at a Vault secret holding these values."
	}

	// vaultconfig.ErrNoToken and ErrFieldNotString already explain the full
	// problem in their own Error() message (which path to fix, which field is
	// bad) — a separate hint would just repeat it.
	var vaultNoToken *vaultconfig.ErrNoToken
	var vaultFieldNotString *vaultconfig.ErrFieldNotString
	if errors.As(err, &vaultNoToken) || errors.As(err, &vaultFieldNotString) {
		return ""
	}

	var vaultAddrNotSet *vaultconfig.ErrVaultAddrNotSet
	if errors.As(err, &vaultAddrNotSet) {
		return "Hint: set VAULT_ADDR to your Vault server's address before using " +
			"--vault-kv-mount/--vault-kv-path."
	}

	// A 403 reading the Vault secret is an authorization problem, not an
	// expired token — re-running `token refresh` (or `vault login`) cannot
	// fix a missing policy grant.
	var vaultForbidden *vaultconfig.ErrForbidden
	if errors.As(err, &vaultForbidden) {
		return fmt.Sprintf("Hint: you are authenticated to Vault, but not authorized to read this secret — "+
			"grant a policy with read access to %s/data/%s.", vaultForbidden.Mount, vaultForbidden.Path)
	}

	var vaultNotFound *vaultconfig.ErrSecretNotFound
	if errors.As(err, &vaultNotFound) {
		return fmt.Sprintf("Hint: no secret exists at %s/%s. Double-check --vault-kv-mount/--vault-kv-path, "+
			"and confirm the mount is actually a KV v2 engine (a KV v1 mount also 404s here).",
			vaultNotFound.Mount, vaultNotFound.Path)
	}

	// ErrNothingToRefresh's own message already explains the problem fully
	// (e.g. "nothing to refresh: no credentials configured") — suggesting
	// `token refresh` on an error raised by `token refresh` itself would be
	// circular and unhelpful.
	var nothingToRefresh *auth.ErrNothingToRefresh
	if errors.As(err, &nothingToRefresh) {
		return ""
	}

	// gateway.UnauthenticatedError carries the gateway's own rejection
	// reason verbatim (e.g. "invalid token: InvalidAudience" / "...
	// InvalidIssuer" / "... ExpiredSignature"). An expired signature is
	// exactly what `token refresh` fixes; a wrong audience or issuer is a
	// configuration mismatch that refreshing the same misconfigured token
	// won't touch, so give each its own hint instead of one generic line.
	var gwUnauth *gateway.UnauthenticatedError
	if errors.As(err, &gwUnauth) {
		switch {
		case strings.Contains(gwUnauth.Message, "InvalidAudience"):
			return "Hint: the token's audience does not match what the gateway expects. Check " +
				"--oidc-audience / metadata.oidc_audience against the gateway's /auth/oidc-config " +
				"before retrying — `openshellctl token refresh` will reuse the same wrong audience."
		case strings.Contains(gwUnauth.Message, "InvalidIssuer"):
			return "Hint: the token's issuer does not match what the gateway expects. Check " +
				"--oidc-issuer / metadata.oidc_issuer before retrying — `openshellctl token refresh` " +
				"will reuse the same wrong issuer."
		default:
			return refreshHint
		}
	}

	// Everything else that's "your credentials are stale or couldn't be
	// obtained" benefits from the same refresh hint.
	var expired *auth.ErrTokenExpired
	var exchange *auth.ExchangeError
	var oidcMissing *auth.ErrOIDCConfigMissing
	if errors.As(err, &expired) || errors.As(err, &exchange) || errors.As(err, &oidcMissing) {
		return refreshHint
	}

	return ""
}
