package cli

import (
	"context"

	"github.com/openshift-online/openshellctl/pkg/auth"
	"github.com/openshift-online/openshellctl/pkg/gateway"
	"github.com/openshift-online/openshellctl/pkg/gatewayconfig"
	"github.com/openshift-online/openshellctl/pkg/selfupdate"
	"github.com/openshift-online/openshellctl/pkg/transfer"
)

// cliDeps holds test-injected replacements for the gateway connection,
// auth resolution, and update-notice fetcher that commands normally obtain
// via the real dial/resolve/GitHubFetcher paths. Production code never
// constructs a non-zero cliDeps; it exists so tests can run commands
// end-to-end through root.Execute() without real network calls.
//
// A single call reads this struct in exactly one place per concern:
// resolveAuth for Gateway/Target/TokenSource, and startNoticeGoroutine
// for NoticeEnvOverride/NoticeFetcher. Nothing else inspects cliDeps directly,
// so there is one decision point per seam, not several that could disagree.
type cliDeps struct {
	Gateway     gateway.Gateway
	Target      *gatewayconfig.Target
	TokenSource auth.TokenSource
	Transfer    transfer.Client

	// NoticeEnvOverride, when non-nil, overrides the environment-derived fields
	// of the CheckInputs built by startNoticeGoroutine: StdoutIsTTY,
	// StderrIsTTY, CI, CurrentVersion, and CacheAge. Command and NoUpdateCheck
	// are always derived from the real cobra invocation (cmd.Name() and the
	// --no-update-check flag) so those code paths remain under test.
	// Only for in-package tests; never set in production.
	NoticeEnvOverride *selfupdate.CheckInputs
	// NoticeFetcher, when non-nil, replaces the GitHubFetcher that
	// startNoticeGoroutine creates. Only for in-package tests; never set in
	// production.
	NoticeFetcher selfupdate.Fetcher
	// NoticeCachePath, when non-empty, overrides the XDG cache path used by
	// startNoticeGoroutine to read and write the update-check cache. Allows
	// tests to use t.TempDir() instead of touching the real XDG directory.
	// Only for in-package tests; never set in production.
	NoticeCachePath string
}

// depsCtxKey is the unexported context key type under which a cliDeps is
// stored. Being unexported and zero-sized, it cannot collide with context
// keys from other packages.
type depsCtxKey struct{}

// withDeps returns a copy of ctx carrying d, retrievable via depsFrom.
func withDeps(ctx context.Context, d cliDeps) context.Context {
	return context.WithValue(ctx, depsCtxKey{}, d)
}

// depsFrom returns the cliDeps stored in ctx by withDeps, if any. ok is false
// when ctx carries no value under depsCtxKey, or when the stored value is not
// a cliDeps — depsFrom never panics.
func depsFrom(ctx context.Context) (cliDeps, bool) {
	d, ok := ctx.Value(depsCtxKey{}).(cliDeps)
	return d, ok
}
