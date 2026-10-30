package doctor

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	types "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"

	"github.com/openshift-online/openshellctl/pkg/auth"
	"github.com/openshift-online/openshellctl/pkg/gateway"
	"github.com/openshift-online/openshellctl/pkg/gatewayconfig"
)

// Deps is Run's full injection seam. ResolveAuth and Dial are thin closures
// over internal/cli's existing resolveAuth/dialOrInjected (wired in
// internal/cli/doctor.go) — pkg/doctor never imports internal/cli or knows
// about cobra/cliDeps; this is how the Feature 0 CLI test seam reaches these
// checks. LookupIP/HTTPGet/ListProviders are the three checks' own,
// independent injection points for their network calls. Every field may be
// left nil; Run treats that as "not wired" and fails/skips the checks that
// need it rather than panicking (TestRun_NilDeps).
type Deps struct {
	ResolveAuth   func(ctx context.Context) (auth.TokenSource, *gatewayconfig.Target, error)
	Dial          func(ctx context.Context, target *gatewayconfig.Target, src auth.TokenSource) (gateway.Gateway, io.Closer, error)
	CurrentUser   func(ctx context.Context, gw gateway.Gateway) (*types.CurrentUser, error)
	ListProviders func(ctx context.Context, gw gateway.Gateway, workspace string) ([]*types.Provider, error)

	LookupIP LookupIPFunc
	HTTPGet  HTTPGetFunc
	Now      func() time.Time // nil defaults to time.Now

	// WantAudience is the expected token audience (see internal/cli/
	// token.go's expectedAudience for the priority this should already
	// have been resolved with: --oidc-audience > metadata.oidc_audience >
	// "openshell-cli").
	WantAudience string
	// RequestedProviders is --provider/-f's resolved provider names/types.
	// Empty means the Providers check is not applicable and is skipped.
	RequestedProviders []string
	// Workspace scopes the ListProviders call (mirrors sandbox create's
	// provider listing).
	Workspace string
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// hostOf extracts the bare hostname from endpoint (defaulting a missing
// scheme to https, matching EnsureScheme/NormalizeEndpoint's own
// convention) for CheckDNS, which wants a hostname, not a full URL. Returns
// "" for an endpoint that doesn't parse at all.
func hostOf(endpoint string) string {
	raw := endpoint
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// skipResult builds a StatusSkip CheckResult explaining why, in the same
// "skipped: <reason>" shape for every skip Run produces itself (as opposed
// to a skip a pure check decided for its own "not applicable" reason, e.g.
// CheckProviders' "no --provider/-f given").
func skipResult(name, reason string) CheckResult {
	return CheckResult{Name: name, Status: StatusSkip, Detail: "skipped: " + reason}
}

// Run executes the fixed, 9-check sequence (ROSAENG-68830) and returns one
// CheckResult per check, in order — every check always produces a line,
// pass/fail/skip, never an early return, so the caller can render the full
// table regardless of where something failed.
//
// Sequence and skip semantics:
//  1. Endpoint URL (pure) — gates DNS.
//  2. DNS — gates HTTP reachability.
//  3. HTTP reachability — its discovered OIDC config feeds check 8.
//  4. Credentials (mints a real token) — runs independently of 2-3's
//     outcome (a DNS/HTTP failure against the discovery endpoint doesn't
//     necessarily mean token minting fails too, and showing both
//     independently is more informative than cascading one failure across
//     checks it isn't actually related to) — but its own failure gates 5-7
//     and, when something was requested, 9.
//  5. Audience, 6. Roles, 7. Expiry — all need the minted token.
//  8. OIDC config match — pure function already skips when either side has
//     nothing to compare, so it's called unconditionally.
//  9. Providers — only dials/lists when something was actually requested
//     AND Credentials succeeded (no token, no dial).
func Run(ctx context.Context, d Deps) []CheckResult {
	var results []CheckResult
	add := func(r CheckResult) { results = append(results, r) }

	var target *gatewayconfig.Target
	var src auth.TokenSource
	var resolveErr error
	if d.ResolveAuth != nil {
		src, target, resolveErr = d.ResolveAuth(ctx)
	} else {
		resolveErr = errNotWired
	}

	endpoint := ""
	if target != nil {
		endpoint = target.Endpoint
	}

	// 1. Endpoint URL
	epResult := CheckEndpointURL(endpoint)
	add(epResult)

	// 2. DNS
	switch {
	case epResult.Status != StatusPass:
		add(skipResult("DNS", "Endpoint URL check failed"))
	case d.LookupIP == nil:
		add(skipResult("DNS", "not wired"))
	default:
		add(CheckDNS(ctx, hostOf(endpoint), d.LookupIP))
	}

	// 3. HTTP reachability
	var oidcCfg OIDCConfig
	dnsResult := results[len(results)-1]
	switch {
	case dnsResult.Status != StatusPass:
		add(skipResult("HTTP reachability", dnsResult.Name+" check failed"))
	case d.HTTPGet == nil:
		add(skipResult("HTTP reachability", "not wired"))
	default:
		var httpResult CheckResult
		httpResult, oidcCfg = CheckHTTP(ctx, endpoint, d.HTTPGet)
		add(httpResult)
	}

	// 4. Credentials — independent of 2-3 (see doc comment above).
	var tok *auth.Token
	if resolveErr == nil && src != nil {
		tok, resolveErr = src.Token(ctx)
	}
	credResult := CheckCredentials(tok, resolveErr)
	add(credResult)

	// 5-7: need the minted token. Additionally, attempt a single gateway dial
	// here (when Dial and CurrentUser are wired) so the Roles check asks the
	// gateway — the real enforcement point — rather than inspecting raw JWT
	// claims that hsctl-managed gateways don't use. The same open connection
	// is reused for the Providers check (check 9) if requested, and closed
	// once at the end of Run.
	var gw gateway.Gateway
	var gwCloser io.Closer
	gwDialed := false // true once a successful Dial call has been made
	if credResult.Status != StatusPass {
		add(skipResult("Audience", "Credentials check failed"))
		add(skipResult("Roles", "Credentials check failed"))
		add(skipResult("Expiry", "Credentials check failed"))
	} else {
		add(CheckAudience(tok, d.WantAudience))

		// Roles: gateway-authoritative when reachable, token-claims fallback otherwise.
		switch {
		case d.Dial == nil || d.CurrentUser == nil:
			add(CheckRolesToken(tok))
		default:
			var dialErr error
			gw, gwCloser, dialErr = d.Dial(ctx, target, src)
			if dialErr != nil {
				add(CheckRolesToken(tok))
			} else {
				gwDialed = true
				user, cuErr := d.CurrentUser(ctx, gw)
				if cuErr != nil {
					add(CheckRolesToken(tok))
				} else {
					add(CheckRolesGateway(user))
				}
			}
		}

		add(CheckExpiry(tok, d.now()))
	}

	// 8. OIDC config match — always called; the pure function itself skips
	// when there's nothing on either side to compare.
	registeredIssuer, registeredAudience := "", ""
	if target != nil && target.Resolved != nil {
		m := target.Resolved.Metadata
		if m.OIDCIssuer != nil {
			registeredIssuer = *m.OIDCIssuer
		}
		registeredAudience = m.OIDCAudienceOrDefault()
	}
	add(CheckOIDCConfigMatch(registeredIssuer, registeredAudience, oidcCfg.Issuer, oidcCfg.Audience))

	// 9. Providers — CheckProviders itself skips when nothing was requested.
	// Reuses the connection dialed for the Roles check when available,
	// dialing fresh only when Roles used the token-claims fallback.
	switch {
	case len(d.RequestedProviders) == 0:
		add(CheckProviders(nil, nil))
	case credResult.Status != StatusPass:
		add(skipResult("Providers", "Credentials check failed"))
	case d.Dial == nil:
		add(skipResult("Providers", "not wired"))
	default:
		if !gwDialed {
			// Roles used the token-claims fallback (dial failed or deps not
			// wired); attempt a fresh dial now for Providers specifically.
			// Close any closer the failed Roles dial may have returned before
			// overwriting gwCloser with the new one.
			if gwCloser != nil {
				_ = gwCloser.Close()
				gwCloser = nil
			}
			var err error
			gw, gwCloser, err = d.Dial(ctx, target, src)
			if err != nil {
				add(CheckResult{
					Name: "Providers", Status: StatusFail,
					Detail:   fmt.Sprintf("could not dial gateway: %v", err),
					NextStep: "resolve the connectivity error above, then re-run `openshellctl doctor`",
				})
				return results
			}
		}
		add(runProviderCheck(ctx, gw, d))
	}

	if gwCloser != nil {
		_ = gwCloser.Close()
	}
	return results
}

// runProviderCheck lists providers and delegates to the pure CheckProviders.
// The caller owns gwCloser; this function only uses gw.
func runProviderCheck(ctx context.Context, gw gateway.Gateway, d Deps) CheckResult {
	if d.ListProviders == nil {
		return skipResult("Providers", "not wired")
	}
	known, err := d.ListProviders(ctx, gw, d.Workspace)
	if err != nil {
		return CheckResult{
			Name: "Providers", Status: StatusFail,
			Detail:   fmt.Sprintf("could not list providers: %v", err),
			NextStep: "resolve the connectivity error above, then re-run `openshellctl doctor`",
		}
	}
	return CheckProviders(known, d.RequestedProviders)
}
