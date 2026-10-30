package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/mock/gomock"

	"github.com/openshift-online/openshellctl/pkg/auth"
	"github.com/openshift-online/openshellctl/pkg/gateway/mock"
	"github.com/openshift-online/openshellctl/pkg/gatewayconfig"
)

func TestClientSecretProvider_None(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	if got := clientSecretProvider(""); got != nil {
		t.Error("expected nil provider when no secret configured")
	}
}

func TestClientSecretProvider_FromFile(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")
	if err := os.WriteFile(path, []byte("  s3cr3t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := clientSecretProvider(path)
	if p == nil {
		t.Fatal("expected a provider for a file path")
	}
	got, err := p(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "s3cr3t" {
		t.Errorf("secret = %q, want trimmed s3cr3t", got)
	}
}

func TestClientSecretProvider_EnvWins(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.SetEnvPrefix("OPENSHELL")
	viper.AutomaticEnv()
	viper.SetEnvKeyReplacer(envKeyReplacer())
	t.Setenv("OPENSHELL_OIDC_CLIENT_SECRET", "from-env")

	p := clientSecretProvider("/nonexistent/file")
	if p == nil {
		t.Fatal("expected a provider from env")
	}
	got, err := p(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "from-env" {
		t.Errorf("secret = %q, want from-env", got)
	}
}

// cmdWithContext returns a bare *cobra.Command carrying ctx, enough to
// exercise resolveAuth/dialOrInjected without building a full command tree.
func cmdWithContext(ctx context.Context) *cobra.Command {
	c := &cobra.Command{}
	c.SetContext(ctx)
	return c
}

// TestResolveAuth_TokenSourceOnlyInjected locks in the fix for the seam
// inconsistency flagged in review: previously withGatewayTarget keyed only on
// deps.Gateway and resolveAuth only on deps.TokenSource, so a TokenSource-only
// injection was silently ignored by withGatewayTarget (it fell through to a
// real resolveTokenSource call instead of using the injected source). Now
// resolveAuth treats either field as "this call is test-injected" and returns
// the injected TokenSource unchanged.
func TestResolveAuth_TokenSourceOnlyInjected(t *testing.T) {
	want := auth.NewNoAuthSource("probe")
	ctx := withDeps(context.Background(), cliDeps{TokenSource: want})
	src, target, err := resolveAuth(cmdWithContext(ctx))
	if err != nil {
		t.Fatalf("resolveAuth: %v", err)
	}
	if src != auth.TokenSource(want) {
		t.Errorf("resolveAuth returned a different TokenSource than the one injected")
	}
	if target != injectedTarget {
		t.Errorf("target = %+v, want the injectedTarget default (deps.Target was nil)", target)
	}
}

// TestResolveAuth_GatewayOnlyInjected covers the complementary case: a
// Gateway injected with no TokenSource must not panic callers that call
// src.Token() (e.g. token show) — resolveAuth substitutes a NoAuthSource
// rather than returning nil.
func TestResolveAuth_GatewayOnlyInjected(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	ctx := withDeps(context.Background(), cliDeps{Gateway: gw})
	src, target, err := resolveAuth(cmdWithContext(ctx))
	if err != nil {
		t.Fatalf("resolveAuth: %v", err)
	}
	if src == nil {
		t.Fatal("resolveAuth returned a nil TokenSource for a Gateway-only injection")
	}
	tok, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("src.Token() on the substituted source: %v", err)
	}
	if tok.Source != auth.SourceNone {
		t.Errorf("substituted source = %+v, want SourceNone", tok)
	}
	if target != injectedTarget {
		t.Errorf("target = %+v, want the injectedTarget default", target)
	}
}

// TestResolveAuth_ExplicitTargetPreserved confirms an explicitly-injected
// Target is passed through unchanged, not overridden by injectedTarget.
func TestResolveAuth_ExplicitTargetPreserved(t *testing.T) {
	want := &gatewayconfig.Target{Name: "explicit"}
	ctx := withDeps(context.Background(), cliDeps{TokenSource: auth.NewNoAuthSource(""), Target: want})
	_, target, err := resolveAuth(cmdWithContext(ctx))
	if err != nil {
		t.Fatalf("resolveAuth: %v", err)
	}
	if target != want {
		t.Errorf("target = %+v, want the explicitly injected %+v", target, want)
	}
}

// TestResolveAuth_NoDepsFallsThrough confirms the absence of any injected
// cliDeps still reaches the real resolveTokenSource (here observed indirectly:
// it returns an error, since no gateway/credentials are configured in this
// test's environment, rather than silently succeeding via some injected
// value it was never given).
func TestResolveAuth_NoDepsFallsThrough(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, _, err := resolveAuth(cmdWithContext(context.Background()))
	if err == nil {
		t.Fatal("expected an error from the real resolveTokenSource path (no gateway configured)")
	}
}

// TestDialOrInjected_ErrorPathCloserIsNilInterface confirms that when
// dialGateway returns an error, dialOrInjected returns a true nil io.Closer
// (not a non-nil interface wrapping a nil *gateway.Conn). A typed-nil
// *gateway.Conn assigned to io.Closer is non-nil, so the "if gwCloser != nil"
// guard in doctor/run.go would pass and Close() would panic on a nil receiver.
func TestDialOrInjected_ErrorPathCloserIsNilInterface(t *testing.T) {
	// http:// endpoint with a bearer token → ErrPlaintextWithAuth, a clean
	// synchronous error that doesn't require network access.
	target := &gatewayconfig.Target{Endpoint: "http://127.0.0.1:0"}
	src := auth.NewStaticSource("bearer-token", func() time.Time { return time.Time{} })
	_, closer, err := dialOrInjected(cmdWithContext(context.Background()), target, src)
	if err == nil {
		t.Fatal("expected an error from dialGateway (ErrPlaintextWithAuth)")
	}
	if closer != nil {
		t.Errorf("closer = %v (%T), want a true nil interface on the error path", closer, closer)
	}
}

// TestDialOrInjected_GatewayInjected confirms dialOrInjected returns the
// injected gateway and a no-op closer, without dialing.
func TestDialOrInjected_GatewayInjected(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockGW := mock.NewMockGateway(ctrl)
	ctx := withDeps(context.Background(), cliDeps{Gateway: mockGW})
	gw, closer, err := dialOrInjected(cmdWithContext(ctx), nil, nil)
	if err != nil {
		t.Fatalf("dialOrInjected: %v", err)
	}
	if gw != mockGW {
		t.Error("dialOrInjected did not return the injected gateway")
	}
	if err := closer.Close(); err != nil {
		t.Errorf("no-op closer returned an error: %v", err)
	}
}
