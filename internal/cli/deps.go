package cli

import (
	"context"

	"github.com/openshift-online/openshellctl/pkg/auth"
	"github.com/openshift-online/openshellctl/pkg/gateway"
	"github.com/openshift-online/openshellctl/pkg/gatewayconfig"
	"github.com/openshift-online/openshellctl/pkg/transfer"
)

// cliDeps holds test-injected replacements for the gateway connection and
// auth resolution that commands normally obtain via the real dial/resolve
// path in authwiring.go. Production code never constructs a non-zero
// cliDeps; it exists so tests can run a command end-to-end through
// root.Execute() against a mock gateway instead of a real dial.
//
// A single call reads this struct in exactly one place: resolveAuth, which
// treats either field being non-nil as "this call is test-injected" and
// returns both the (possibly defaulted) TokenSource and Target; its result
// then flows into dialOrInjected, which substitutes Gateway when present.
// Nothing else inspects cliDeps directly, so there is one decision point, not
// several that could disagree about whether a given field was injected.
type cliDeps struct {
	Gateway     gateway.Gateway
	Target      *gatewayconfig.Target
	TokenSource auth.TokenSource
	Transfer    transfer.Client
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
