// Package cli implements the openshellctl cobra/viper command surface. Command
// files hold only flag wiring and thin adapters; all logic lives in pkg/*.
package cli

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// envKeyReplacer maps viper config keys to OPENSHELL_ env var names: "-" and
// "." both become "_" (e.g. gateway-endpoint -> OPENSHELL_GATEWAY_ENDPOINT,
// oidc.client_secret -> OPENSHELL_OIDC_CLIENT_SECRET), per §5.9.
func envKeyReplacer() *strings.Replacer {
	return strings.NewReplacer("-", "_", ".", "_")
}

// persistentFlags holds the values bound to the root persistent flags. Command
// implementations read these (via the *cobra.Command tree) rather than package
// globals; the struct is attached to the root command's context in later commits.
type persistentFlags struct {
	gateway         string
	gatewayEndpoint string
	gatewayInsecure bool
	workspace       string
	verbosity       int
	configFile      string
	noColor         bool

	// auth
	token            string
	clientSecretFile string
	oidcIssuer       string
	oidcClientID     string
	oidcAudience     string
	oidcScopes       string
	tokenLeeway      string
	writeToken       bool
	noBrowser        bool

	// vault auth source
	vaultKVMount     string
	vaultKVPath      string
	vaultFieldPrefix string
	noVault          bool
}

// NewRootCommand builds the full command tree. It is exported so tests (parity,
// execution) can construct a fresh tree without global state.
func NewRootCommand() *cobra.Command {
	pf := &persistentFlags{}

	root := &cobra.Command{
		Use:           "openshellctl",
		Short:         "Lightweight Go client for OpenShell",
		SilenceUsage:  true,
		SilenceErrors: true,
		// DisableFlagsInUseLine keeps the usage line clean (spec §5.9).
		DisableFlagsInUseLine: true,
	}

	// Flag parse errors map to exit code 2 (usage).
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &UsageError{Err: err}
	})

	registerPersistentFlags(root, pf)
	bindViper(root)

	// Runs once, after flags are parsed but before any subcommand's RunE.
	root.PersistentPreRunE = func(_ *cobra.Command, _ []string) error {
		applyGatewayInsecureTransport()
		return nil
	}

	root.AddCommand(
		newSandboxCommand(),
		newLogsCommand(),
		newTokenCommand(),
		newPolicyCommand(),
		newVersionCommand(),
		newWhoamiCommand(),
		newLoginCommand(),
		newGatewayCommand(),
		newDoctorCommand(),
		newProviderCommand(),
	)

	return root
}

func registerPersistentFlags(root *cobra.Command, pf *persistentFlags) {
	f := root.PersistentFlags()
	f.StringVarP(&pf.gateway, "gateway", "g", "", "gateway name (env OPENSHELL_GATEWAY)")
	f.StringVar(&pf.gatewayEndpoint, "gateway-endpoint", "", "gateway endpoint URL (env OPENSHELL_GATEWAY_ENDPOINT)")
	f.BoolVar(&pf.gatewayInsecure, "gateway-insecure", false, "skip TLS verification (env OPENSHELL_GATEWAY_INSECURE)")
	f.StringVar(&pf.workspace, "workspace", "default", "workspace (env OPENSHELL_WORKSPACE)")
	f.CountVarP(&pf.verbosity, "verbose", "v", "increase verbosity")
	f.StringVar(&pf.configFile, "config", "", "config file (default $XDG_CONFIG_HOME/openshellctl/config.yaml)")
	f.BoolVar(&pf.noColor, "no-color", false, "disable colored output")

	f.StringVar(&pf.token, "token", "", "bearer token (env OPENSHELL_TOKEN)")
	f.StringVar(&pf.clientSecretFile, "client-secret-file", "", "file containing the OIDC client secret")
	f.StringVar(&pf.oidcIssuer, "oidc-issuer", "", "OIDC issuer URL override")
	f.StringVar(&pf.oidcClientID, "oidc-client-id", "", "OIDC client id override")
	f.StringVar(&pf.oidcAudience, "oidc-audience", "", "OIDC audience override")
	f.StringVar(&pf.oidcScopes, "oidc-scopes", "", "OIDC scopes override (space-separated)")
	f.StringVar(&pf.tokenLeeway, "token-leeway", "30s", "token expiry leeway")
	f.BoolVar(&pf.writeToken, "write-token", false, "write refreshed tokens back to disk (Rust CLI schema)")
	f.BoolVar(&pf.noBrowser, "no-browser", false, "skip browser login when no client secret is available, e.g. for service accounts (env OPENSHELL_NO_BROWSER)")

	f.StringVar(&pf.vaultKVMount, "vault-kv-mount", "", "Vault KV v2 mount holding auth config, e.g. osd-sre (env OPENSHELL_VAULT_KV_MOUNT; must be set together with --vault-kv-path)")
	f.StringVar(&pf.vaultKVPath, "vault-kv-path", "", "path within --vault-kv-mount holding auth config, e.g. rosa-agent (env OPENSHELL_VAULT_KV_PATH)")
	f.StringVar(&pf.vaultFieldPrefix, "vault-field-prefix", "", "prefix prepended to each field name read from the Vault secret, e.g. hypershell (env OPENSHELL_VAULT_FIELD_PREFIX)")
	f.BoolVar(&pf.noVault, "no-vault", false, "disable the Vault auth source entirely for this invocation — ignores --vault-kv-mount/--vault-kv-path/--vault-field-prefix and any persisted vault_auth.json for the resolved gateway (env OPENSHELL_NO_VAULT; ROSAENG-74343)")
}

// bindViper wires the OPENSHELL_* environment prefix and flag binding, per §5.9.
func bindViper(root *cobra.Command) {
	viper.SetEnvPrefix("OPENSHELL")
	viper.AutomaticEnv()
	viper.SetEnvKeyReplacer(envKeyReplacer())
	// Persistent flags are bound to viper so env vars back them.
	_ = viper.BindPFlags(root.PersistentFlags())
}

// applyGatewayInsecureTransport overrides the process-wide http.DefaultTransport
// to skip TLS certificate verification when --gateway-insecure/
// OPENSHELL_GATEWAY_INSECURE is set. This is a deliberately broad,
// process-global side effect — there is no narrower lever available: the
// OpenShell SDK's own OIDC discovery and token-endpoint HTTP calls
// (github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/oidc, used by the
// client-credentials exchange and the browser login flow) go through an
// unexported package-level *http.Client with no Transport override of its
// own — meaning it falls back to http.DefaultTransport — and the SDK exposes
// no LoginOption to inject a custom client or skip verification (an upstream
// WithHTTPClient option would remove the need for this entirely). Without
// this, --gateway-insecure would cover the real gRPC dial (pkg/gateway/
// dial.go) and openshellctl's own /auth/oidc-config discovery probe
// (oidcConfigFetcher, authwiring.go) but not the SDK's own issuer discovery,
// which is exactly the gap a staging/internal-CA OIDC issuer hits. Matches
// upstream's own gateway_insecure handling, which applies to its OIDC HTTP
// client too (oidc_auth.rs: danger_accept_invalid_certs) — parity, not an
// extension.
//
// Clones the real default transport rather than replacing it with a bare
// &http.Transport{}: a bare one silently drops proxy support (HTTPS_PROXY/
// NO_PROXY), dial/handshake timeouts, keepalives, and HTTP/2 — a regression
// that would hit every HTTP call in the process, not just the TLS
// verification this is meant to relax. The user has already explicitly
// opted into "skip TLS verification" via the flag, accepting that scope;
// this is a no-op when the flag is unset.
func applyGatewayInsecureTransport() {
	if !viper.GetBool("gateway-insecure") {
		return
	}
	var tr *http.Transport
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		tr = base.Clone()
	} else {
		tr = &http.Transport{Proxy: http.ProxyFromEnvironment}
	}
	if tr.TLSClientConfig == nil {
		tr.TLSClientConfig = &tls.Config{}
	}
	tr.TLSClientConfig.InsecureSkipVerify = true //nolint:gosec // explicit opt-in via --gateway-insecure
	http.DefaultTransport = tr
}

// Execute builds and runs the root command, returning a process exit code.
// It prints "Error: <msg>" to stderr for failures (root has SilenceErrors set).
// SIGINT and SIGTERM cancel the command context so non-interactive operations
// (watch, upload, gRPC calls) clean up promptly. During raw-mode SSH sessions,
// Ctrl-C (0x03) flows through stdin to the remote process — the terminal
// driver does not convert it to SIGINT while in raw mode.
func Execute() int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	root := NewRootCommand()
	root.SetContext(ctx)
	if err := root.Execute(); err != nil {
		// A remote (exec/connect/create-attach) non-zero exit is propagated as the
		// process status without an "Error:" message — the remote command already
		// wrote its own output.
		var remote *RemoteExitError
		if !errors.As(err, &remote) && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "Error: %s\n", err.Error())
		}
		code := exitCodeFor(err)
		if hint := hintFor(err); hint != "" {
			// The "don't repeat yourself" suppression only makes sense for the
			// generic refresh hint — e.g. ErrTokenExpired's own message
			// sometimes already names `openshellctl login` as its remediation.
			// Other hints (permission-denied, no-credentials, audience/issuer
			// mismatches) are never redundant with the error text itself, so
			// they must not be silently dropped by the same check.
			suppress := hint == refreshHint
			if suppress {
				msg := err.Error()
				suppress = strings.Contains(msg, "openshellctl token refresh") || strings.Contains(msg, "openshellctl login")
			}
			if !suppress {
				fmt.Fprintf(os.Stderr, "%s\n", hint)
			}
		}
		return code
	}
	return ExitOK
}
