package cli

import (
	"context"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	types "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"

	"github.com/openshift-online/openshellctl/pkg/auth"
	"github.com/openshift-online/openshellctl/pkg/doctor"
	"github.com/openshift-online/openshellctl/pkg/gateway"
	"github.com/openshift-online/openshellctl/pkg/gatewayconfig"
	"github.com/openshift-online/openshellctl/pkg/output"
	"github.com/openshift-online/openshellctl/pkg/sandbox"
)

// newDoctorCommand wires pkg/doctor.Run to the real world: resolveAuth/
// dialOrInjected (the same Feature 0 seam every other auth-resolving command
// uses — this is how `doctor` honors cliDeps injection in tests without
// pkg/doctor ever importing internal/cli), and the real DNS resolver/HTTP
// client for the two network checks.
func newDoctorCommand() *cobra.Command {
	var outputFormat string
	var providerFlag []string
	var manifestFile string
	c := &cobra.Command{
		Use:   "doctor",
		Short: "Run connectivity/auth preflight checks",
		Long: "Checks endpoint reachability, DNS, credentials, token audience/roles/expiry, and OIDC config drift — " +
			"one line per check, with the exact next command to run for any failure. --provider/-f additionally " +
			"checks that requested providers exist on the gateway.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Resolved once; Deps.ResolveAuth below returns these same
			// values rather than re-resolving (resolveAuth has no side
			// effects beyond this, but there's no reason to do the work
			// twice).
			src, target, resolveErr := resolveAuth(cmd)

			requested, err := requestedProviders(cmd, providerFlag, manifestFile)
			if err != nil {
				return err
			}

			d := doctor.Deps{
				ResolveAuth: func(context.Context) (auth.TokenSource, *gatewayconfig.Target, error) {
					return src, target, resolveErr
				},
				Dial: func(_ context.Context, target *gatewayconfig.Target, src auth.TokenSource) (gateway.Gateway, io.Closer, error) {
					return dialOrInjected(cmd, target, src)
				},
				CurrentUser: func(ctx context.Context, gw gateway.Gateway) (*types.CurrentUser, error) {
					return gw.CurrentUser(ctx)
				},
				ListProviders:      sandbox.ListAllProviders,
				LookupIP:           realLookupIP,
				HTTPGet:            realHTTPGet,
				WantAudience:       expectedAudience(target),
				RequestedProviders: requested,
				Workspace:          viper.GetString("workspace"),
			}

			results := doctor.Run(cmd.Context(), d)

			checks := make([]output.DoctorCheck, len(results))
			anyFail := false
			for i, r := range results {
				checks[i] = output.DoctorCheck{Name: r.Name, Status: string(r.Status), Detail: r.Detail, NextStep: r.NextStep}
				if r.Status == doctor.StatusFail {
					anyFail = true
				}
			}
			if err := output.RenderDoctor(cmd.OutOrStdout(), checks, output.Format(outputFormat)); err != nil {
				return err
			}

			if anyFail {
				return &doctor.ErrChecksFailed{Results: results}
			}
			return nil
		},
	}
	c.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format: table|json|yaml")
	c.Flags().StringSliceVar(&providerFlag, "provider", nil, "provider name/type to check exists on the gateway (repeatable)")
	c.Flags().StringVarP(&manifestFile, "file", "f", "", "manifest file to read providerRefs from, when --provider is not given (- for stdin)")
	return c
}

// requestedProviders resolves the Providers check's input: --provider wins
// outright when given (matching sandbox create's own flag-overrides-manifest
// precedence, pkg/sandbox/merge.go); otherwise, with -f given, every
// manifest providerRef's name. Reuses loadManifest (internal/cli/
// sandbox_create.go) — the same decode+validate path `sandbox create -f`
// uses — rather than a second manifest reader.
func requestedProviders(cmd *cobra.Command, providerFlag []string, manifestFile string) ([]string, error) {
	if len(providerFlag) > 0 {
		return providerFlag, nil
	}
	if manifestFile == "" {
		return nil, nil
	}
	m, err := loadManifest(cmd, manifestFile)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(m.Spec.ProviderRefs))
	for i, p := range m.Spec.ProviderRefs {
		names[i] = p.Name
	}
	return names, nil
}

// realLookupIP adapts net.DefaultResolver.LookupIPAddr to LookupIPFunc's
// shape. A literal IP (as a test's httptest.Server address is) resolves
// without any real DNS query — Go's resolver recognizes it immediately —
// so this is safe to wire unconditionally rather than needing its own
// injection seam for CLI tests.
func realLookupIP(ctx context.Context, host string) ([]net.IP, error) {
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, len(addrs))
	for i, a := range addrs {
		ips[i] = a.IP
	}
	return ips, nil
}

// realHTTPGet performs a real GET, sharing http.DefaultTransport so
// --gateway-insecure (applyGatewayInsecureTransport, root.go) is honored the
// same way oidcConfigFetcher already is — one global toggle, not a second
// one for doctor specifically.
func realHTTPGet(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	return client.Do(req)
}
