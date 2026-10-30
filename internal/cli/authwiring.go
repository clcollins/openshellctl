package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/openshift-online/openshellctl/internal/version"
	"github.com/openshift-online/openshellctl/pkg/auth"
	"github.com/openshift-online/openshellctl/pkg/gateway"
	"github.com/openshift-online/openshellctl/pkg/gatewayconfig"
)

// clientSecretProvider returns a secret-reading closure, or nil when no secret
// source is configured. Precedence: OPENSHELL_OIDC_CLIENT_SECRET env (via viper)
// > --client-secret-file. The env value is never logged.
func clientSecretProvider(clientSecretFile string) func(ctx context.Context) (string, error) {
	if secret := viper.GetString("oidc.client_secret"); secret != "" {
		return func(context.Context) (string, error) { return secret, nil }
	}
	if clientSecretFile != "" {
		return func(context.Context) (string, error) {
			b, err := os.ReadFile(clientSecretFile)
			if err != nil {
				return "", err
			}
			return strings.TrimSpace(string(b)), nil
		}
	}
	return nil
}

// authWriterAdapter adapts a *gatewayconfig.OSWriter to auth.Writer, rooting
// writes at the gateway's directory so a bare "oidc_token.json" relPath lands in
// gateways/<name>/oidc_token.json on the user tree.
type authWriterAdapter struct {
	w           *gatewayconfig.OSWriter
	gatewayName string
}

func (a authWriterAdapter) WriteFile(relPath string, data []byte, perm fs.FileMode) error {
	return a.w.WriteFile(gatewayconfig.GatewayDir(a.gatewayName)+"/"+relPath, data, perm)
}

// tokenWriterFor builds an auth.Writer that persists to the resolved gateway's
// directory on the user tree, or (nil, nil) when there is no named gateway to
// write to (e.g. endpoint-only invocation).
func tokenWriterFor(target *gatewayconfig.Target) (auth.Writer, error) {
	if target == nil || target.Name == "" || target.Resolved == nil {
		return nil, nil
	}
	w, err := gatewayconfig.NewOSWriter()
	if err != nil {
		return nil, err
	}
	return authWriterAdapter{w: w, gatewayName: target.Name}, nil
}

// requireTokenWriter is tokenWriterFor, but returns a typed usage error
// instead of a silent (nil, nil) when there's no named, registered gateway
// to write to. Callers where --write (or similar) is an explicit request to
// persist something must use this, not tokenWriterFor directly — silently
// no-op'ing an explicit --write and still exiting 0 is exactly the
// swallowed-warning bug this exists to close.
func requireTokenWriter(target *gatewayconfig.Target) (auth.Writer, error) {
	w, err := tokenWriterFor(target)
	if err != nil {
		return nil, err
	}
	if w == nil {
		return nil, &UsageError{Err: fmt.Errorf(
			"--write requires a named, registered gateway; register one first: " +
				"openshellctl gateway add <endpoint> --name <name>")}
	}
	return w, nil
}

// oidcConfigFetcher fetches {issuer, audience} from GET <endpoint>/auth/oidc-config.
// Uses http.DefaultTransport (no Transport override of its own), so it
// automatically honors --gateway-insecure once applyGatewayInsecureTransport
// (root.go) has run — the same global override the SDK's own OIDC HTTP
// client needs regardless, since it has no per-call injection point at all.
// Keeping a single place that decides "skip verification" avoids this call
// and the SDK's calls disagreeing about whether --gateway-insecure applies.
func oidcConfigFetcher(ctx context.Context, endpoint string) (string, string, error) {
	url := strings.TrimSuffix(endpoint, "/") + "/auth/oidc-config"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("/auth/oidc-config returned %s", resp.Status)
	}
	var body struct {
		Issuer   string `json:"issuer"`
		Audience string `json:"audience"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", "", err
	}
	return body.Issuer, body.Audience, nil
}

// resolveTokenSource builds a TokenSource from the current flags/env. It resolves
// the gateway (best-effort — endpoint-only is allowed) and then the auth source.
// When --write-token is set and a gateway is resolved, a write-back Writer is
// wired so a disk-bundle refresh persists (Rust CLI schema).
func resolveTokenSource(cmd *cobra.Command) (auth.TokenSource, *gatewayconfig.Target, error) {
	if err := applyVaultAuthSource(cmd.Context()); err != nil {
		return nil, nil, err
	}

	env, err := gatewayconfig.NewOSEnv()
	if err != nil {
		return nil, nil, err
	}

	name := viper.GetString("gateway")
	endpoint := viper.GetString("gateway-endpoint")

	target, err := gatewayconfig.Resolve(env, gatewayconfig.ResolveInput{Endpoint: endpoint, Name: name})
	if err != nil {
		return nil, nil, err
	}

	in := auth.ResolveInput{
		StaticToken:       viper.GetString("token"),
		ClientSecret:      clientSecretProvider(viper.GetString("client-secret-file")),
		Issuer:            viper.GetString("oidc-issuer"),
		ClientID:          viper.GetString("oidc-client-id"),
		Audience:          viper.GetString("oidc-audience"),
		Gateway:           target.Resolved,
		GatewayEndpoint:   target.Endpoint,
		TLSPresent:        target.Resolved != nil && gatewayconfig.TLSMaterialFor(target.Resolved).Present,
		OIDCConfigFetcher: oidcConfigFetcher,
	}
	if scopes := viper.GetString("oidc-scopes"); scopes != "" {
		in.Scopes = strings.Fields(scopes)
	}

	// Wire write-back when requested and a named gateway is resolved. Uses
	// requireTokenWriter, not tokenWriterFor, so an explicit --write-token
	// against an endpoint-only target (no named, registered gateway) fails
	// loudly instead of silently no-oping — the same swallowed-warning shape
	// `token refresh --write` used to have, on every other command that
	// resolves auth (sandbox list, exec, ...), not just `token refresh`.
	if viper.GetBool("write-token") {
		w, werr := requireTokenWriter(target)
		if werr != nil {
			return nil, target, werr
		}
		in.TokenWriter = w
	}

	src, err := auth.Resolve(cmd.Context(), in, auth.NewSDKExchanger())
	if err != nil {
		return nil, target, err
	}
	return src, target, nil
}

// injectedTarget is the Target substituted for a nil deps.Target on the
// injected path, so callers that read target.Name/target.Endpoint without a
// nil-guard get a well-defined test value instead of a nil-pointer panic.
// Resolved stays nil, matching the normal "endpoint-only" state elsewhere in
// this package.
var injectedTarget = &gatewayconfig.Target{Name: "test", Endpoint: "https://test.invalid"}

// resolveAuth is resolveTokenSource, but honors cliDeps (deps.go) injected on
// the command's context: if either Gateway or TokenSource was injected, it
// returns the injected TokenSource (or, when only Gateway was injected, a
// auth.NoAuthSource stand-in — never nil, so a caller that calls src.Token()
// doesn't panic) and the injected Target (defaulted via injectedTarget when
// nil), skipping the real resolveTokenSource entirely. Checking Gateway here
// too (not just TokenSource) keeps this in lockstep with dialOrInjected/
// withGatewayTarget, which key off the same two fields — see the package doc
// on cliDeps for why a single command must never see one seam honor an
// injected dep that another seam on the same call ignores.
//
// Production code never sets cliDeps, so this always falls through to the
// real resolveTokenSource there.
func resolveAuth(cmd *cobra.Command) (auth.TokenSource, *gatewayconfig.Target, error) {
	deps, ok := depsFrom(cmd.Context())
	if !ok || (deps.TokenSource == nil && deps.Gateway == nil) {
		return resolveTokenSource(cmd)
	}
	target := deps.Target
	if target == nil {
		target = injectedTarget
	}
	src := deps.TokenSource
	if src == nil {
		src = auth.NewNoAuthSource("test gateway injected, no token source injected")
	}
	return src, target, nil
}

// noopCloser is an io.Closer that does nothing, used by dialOrInjected to
// stand in for the real *gateway.Conn when the gateway was test-injected and
// there is nothing to close.
type noopCloser struct{}

func (noopCloser) Close() error { return nil }

// dialOrInjected is dialGateway, but returns a Gateway injected via cliDeps
// (deps.go) on the command's context when present, skipping the real dial.
// The returned io.Closer is a no-op in that case; *gateway.Conn (dialGateway's
// real return) already satisfies io.Closer, so callers need no other change.
func dialOrInjected(cmd *cobra.Command, target *gatewayconfig.Target, src auth.TokenSource) (gateway.Gateway, io.Closer, error) {
	if deps, ok := depsFrom(cmd.Context()); ok && deps.Gateway != nil {
		return deps.Gateway, noopCloser{}, nil
	}
	gw, conn, err := dialGateway(target, src)
	if err != nil {
		return nil, nil, err
	}
	return gw, conn, nil
}

// dialGateway builds a gateway.Gateway from a resolved target and token source.
// The caller is responsible for closing the returned Conn.
func dialGateway(target *gatewayconfig.Target, src auth.TokenSource) (gateway.Gateway, *gateway.Conn, error) {
	dc := gateway.DialConfig{
		Endpoint:  target.Endpoint,
		Auth:      src,
		Insecure:  viper.GetBool("gateway-insecure"),
		UserAgent: userAgent(),
	}
	if target.Resolved != nil {
		dc.TLS = gatewayconfig.TLSMaterialFor(target.Resolved)
		dc.TLSRoot = target.Resolved.Dir
	}
	conn, err := gateway.Dial(dc)
	if err != nil {
		return nil, nil, err
	}
	return gateway.New(conn, src), conn, nil
}

func userAgent() string {
	v := version.Get()
	return fmt.Sprintf("openshellctl/%s (openshell-pin %s)", v.Version, v.OpenShellPin)
}
