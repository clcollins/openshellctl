package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/openshift-online/openshellctl/internal/version"
)

// manifestURLSizeLimit caps how many bytes a -f URL manifest fetch will read,
// so an unbounded or misconfigured URL can't exhaust memory. 1 MiB is far
// larger than any real sandbox manifest.
const manifestURLSizeLimit = 1 << 20 // 1 MiB

// manifestFetchTimeout bounds a -f URL manifest fetch. Reuses
// doctor.go:realHTTPGet's own 10s default rather than inventing a second
// budget for what is, at the HTTP layer, the same kind of request.
const manifestFetchTimeout = 10 * time.Second

// manifestSourceKind classifies a -f/--file path argument. It is the pure
// decision behind readManifestBytes' I/O branch below.
type manifestSourceKind int

const (
	sourceFile manifestSourceKind = iota
	sourceStdin
	sourceURL
)

// classifyManifestSource is a pure function: "-" is stdin, an http(s):// URL
// (detected via url.Parse + Scheme, never a regex, so case variations like
// "HTTPS://" and other odd inputs behave predictably) is a URL, anything
// else is a local filesystem path. No I/O, no fetch — just the decision.
//
// Two refinements on top of a bare url.Parse+Scheme check, both found in
// correctness review:
//
//  1. u.Opaque != "" excludes an opaque URI like "http:foo.yaml" — a scheme
//     followed by ":" with no "//" is also a legal POSIX filename
//     containing a colon, and url.Parse happily assigns it Scheme="http"/
//     Opaque="foo.yaml". Without this check, that real local file would be
//     misrouted to fetchManifestURL instead of os.ReadFile — a regression
//     from the pre-URL-support behavior (unconditional os.ReadFile for
//     anything but "-").
//  2. looksLikeHTTPURL is a fallback for a string url.Parse rejects
//     outright (e.g. one containing a raw control character) but which is
//     unambiguously URL-shaped by its prefix. Without it, such a string
//     falls through to sourceFile, os.ReadFile fails with a confusing "no
//     such file or directory", and fetchManifestURL's own clear "invalid
//     manifest URL" error (which does the real, authoritative url.Parse)
//     never gets a chance to fire.
func classifyManifestSource(path string) manifestSourceKind {
	if path == "-" {
		return sourceStdin
	}
	if u, err := url.Parse(path); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Opaque == "" {
		return sourceURL
	}
	if looksLikeHTTPURL(path) {
		return sourceURL
	}
	return sourceFile
}

// looksLikeHTTPURL is a narrow, case-insensitive prefix check — not a
// general regex-based detector, and not the primary classification
// mechanism (url.Parse+Scheme above is). It exists solely to route a string
// that is unambiguously URL-shaped by prefix, but that url.Parse itself
// rejects (e.g. a raw control character later in the string), to
// fetchManifestURL rather than silently treating it as a local path.
func looksLikeHTTPURL(path string) bool {
	lower := strings.ToLower(path)
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
}

// redactURL is a pure function: it prints only scheme://host/path for use in
// error messages and logs, stripping query parameters and userinfo. This
// protects presigned S3/GCS-style URLs (whose credentials often live in the
// query string, e.g. "?X-Amz-Signature=...") and "?token=..." manifest URLs
// from ever appearing in CLI output.
func redactURL(u *url.URL) string {
	redacted := url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}
	return redacted.String()
}

// redactRawURLBestEffort is used only when url.Parse itself fails on a -f
// URL, so there is no *url.URL to hand to redactURL. It is a conservative,
// best-effort scrub, not a parser: drop everything from the first '?'
// onward (the query string, where presigned-URL credentials typically
// live), then drop whatever sits between "://" and the next '@' (userinfo,
// e.g. "user:pass@") if both are present. Unlike a naive "truncate at the
// first '@'" — which would keep the "user:pass" portion and discard the
// host/path instead — this removes the credential itself and keeps the
// scheme/host/path for diagnostics. A malformed URL that still contains a
// credential or token (e.g. "https://user:pass@%zzbadhost/x") can never be
// echoed verbatim into an error message or log, even on this parse-failure
// path. Security review (ROSAENG-74491): every error naming the URL must
// use a redacted form, no exceptions.
func redactRawURLBestEffort(raw string) string {
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		raw = raw[:i]
	}
	if schemeEnd := strings.Index(raw, "://"); schemeEnd >= 0 {
		afterScheme := schemeEnd + len("://")
		if at := strings.IndexByte(raw[afterScheme:], '@'); at >= 0 {
			raw = raw[:afterScheme] + raw[afterScheme+at+1:]
		}
	}
	return raw
}

// errManifestRedirectDowngrade is returned by rejectHTTPSDowngrade, and
// matched with errors.Is after http.Client.Do wraps it in a *url.Error.
var errManifestRedirectDowngrade = errors.New("refusing to follow redirect from https to http")

// rejectHTTPSDowngrade is the manifest-fetch http.Client's CheckRedirect. Go's
// default redirect policy follows a scheme downgrade (an https URL can
// redirect to plain http, letting a MITM on the second hop rewrite the
// manifest in flight), so this replaces that default with: follow redirects
// exactly like Go's default otherwise (same host-to-host, same 10-redirect
// cap), but refuse any hop that goes from https back to http. A same-scheme
// or http->https redirect, including to a different host, is unaffected.
func rejectHTTPSDowngrade(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	prev := via[len(via)-1]
	if prev.URL.Scheme == "https" && req.URL.Scheme == "http" {
		return errManifestRedirectDowngrade
	}
	return nil
}

// manifestHTTPDoer performs a single HTTP request, the same shape as
// *http.Client.Do. Tests inject a fake/httptest-backed doer; production uses
// newManifestHTTPClient, so fetchManifestURL never dials the network in a
// hermetic test.
type manifestHTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// newManifestHTTPClient builds the production doer for -f URL manifest
// fetches. Transport is left nil so it resolves to http.DefaultTransport at
// request time — the same sharing doctor.go's realHTTPGet already relies on
// — so --gateway-insecure (applyGatewayInsecureTransport, root.go) governs
// TLS verification for a manifest fetch exactly as it does for doctor's HTTP
// check, rather than this command inventing a second insecure-transport
// toggle. See README's --gateway-insecure entry: this is a documented
// consequence of that sharing, since the manifest host is not the gateway.
func newManifestHTTPClient() *http.Client {
	return &http.Client{
		Timeout:       manifestFetchTimeout,
		CheckRedirect: rejectHTTPSDowngrade,
	}
}

// manifestFetchError reports a -f URL manifest fetch that failed after the
// request was made: a non-2xx response, a redirect-downgrade refusal, or a
// network/timeout failure. It is never constructed with the raw URL — only
// redactURL's output — so a presigned URL's query string or userinfo can
// never reach an error message. Maps to ExitError (exitcode.go): closer to a
// network failure than a usage error.
type manifestFetchError struct {
	URL    string // already redacted
	Status int    // non-zero for a non-2xx response; zero otherwise
	Err    error  // wrapped cause; nil for a pure non-2xx status
}

func (e *manifestFetchError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("fetching manifest %s: unexpected HTTP status %d", e.URL, e.Status)
	}
	return fmt.Sprintf("fetching manifest %s: %v", e.URL, e.Err)
}

func (e *manifestFetchError) Unwrap() error { return e.Err }

// manifestURLSizeError reports a -f URL manifest response that exceeded
// manifestURLSizeLimit. The body is never fully read once the cap is
// crossed. Maps to ExitError (exitcode.go).
type manifestURLSizeError struct {
	URL   string // already redacted
	Limit int64
}

func (e *manifestURLSizeError) Error() string {
	return fmt.Sprintf("manifest at %s exceeds the %d-byte size limit", e.URL, e.Limit)
}

// manifestUserAgent identifies openshellctl to the server fetching a -f URL
// manifest, per the card's "no custom headers beyond a User-Agent" fetch
// semantics.
func manifestUserAgent() string {
	return "openshellctl/" + version.Get().Version
}

// fetchManifestURL is the thin I/O shell for a -f http(s):// URL: build the
// request, reject anything with userinfo before it is ever sent (net/http
// silently adds a Basic-auth Authorization header when req.URL.User is set,
// which would both contradict "no credential is ever attached to this
// request" and smuggle in the authenticated-URL support this card
// deliberately leaves out of scope), send it, and read the body under the
// size cap. No gateway credential (OIDC bearer token, Vault token) is ever
// read or attached here — this function never has access to one; the only
// header it sets is User-Agent.
func fetchManifestURL(ctx context.Context, doer manifestHTTPDoer, rawURL string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, &UsageError{Err: fmt.Errorf("invalid manifest URL %q: %w", redactRawURLBestEffort(rawURL), err)}
	}
	if u.User != nil {
		return nil, &UsageError{Err: fmt.Errorf(
			"manifest URL must not contain userinfo (credentials in a -f URL are not supported): %s", redactURL(u))}
	}

	// cobra's real ExecuteC always sets a non-nil context, so this only
	// matters for a test that builds a bare &cobra.Command{} and calls
	// cmd.Context() before SetContext — fall back rather than let
	// http.NewRequestWithContext surface net/http's internal "nil Context"
	// message, which is accurate but unrelated to the actual problem a user
	// would hit.
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, &UsageError{Err: fmt.Errorf("invalid manifest URL %q: %w", redactURL(u), err)}
	}
	req.Header.Set("User-Agent", manifestUserAgent())

	resp, err := doer.Do(req)
	if err != nil {
		return nil, &manifestFetchError{URL: redactURL(u), Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &manifestFetchError{URL: redactURL(u), Status: resp.StatusCode}
	}

	limited := io.LimitReader(resp.Body, manifestURLSizeLimit+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, &manifestFetchError{URL: redactURL(u), Err: err}
	}
	if int64(len(data)) > manifestURLSizeLimit {
		return nil, &manifestURLSizeError{URL: redactURL(u), Limit: manifestURLSizeLimit}
	}
	return data, nil
}

// readManifestBytes is the single manifest-byte-reading boundary shared by
// every -f/--file consumer: loadManifest (get/connect/upload/download/
// ssh-config/logs/stop/start/delete/provider/doctor, via namesFromManifest
// and requestedProviders) and sandbox create's own loadManifestWithParams
// templating path (sandbox_create_params.go). "-" reads stdin, an
// http(s):// URL is fetched via doer, anything else is a local path via
// os.ReadFile — one branch, in one place, so URL support reaches every -f
// command uniformly rather than being added per command.
//
// This is a thin I/O shell, not a second orchestrator: it makes no decision
// readManifestBytes' own callers didn't already make (classifyManifestSource
// decided stdin/URL/file; this just performs the corresponding read). Its
// one extra level below loadManifest/loadManifestWithParams is a deliberate,
// documented exception to AGENTS.md's N+1 depth rule, existing solely so
// those two orchestrators share one manifest-byte-reading boundary instead
// of duplicating the branch.
func readManifestBytes(cmd *cobra.Command, doer manifestHTTPDoer, path string) ([]byte, error) {
	switch classifyManifestSource(path) {
	case sourceStdin:
		return readAll(cmd.InOrStdin())
	case sourceURL:
		return fetchManifestURL(cmd.Context(), doer, path)
	default:
		return os.ReadFile(path)
	}
}
