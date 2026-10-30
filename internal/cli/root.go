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
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/openshift-online/openshellctl/internal/version"
	"github.com/openshift-online/openshellctl/pkg/selfupdate"
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
	noUpdateCheck   bool

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

// noticeResult is sent by the background notice goroutine.
type noticeResult struct {
	text      string
	latestTag string
}

// noticeChKey is the context key for the notice result channel.
type noticeChKey struct{}

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
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		applyGatewayInsecureTransport()
		startNoticeGoroutine(cmd, pf)
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
		newUpdateCommand(),
	)

	return root
}

// startNoticeGoroutine launches the background update-check if all gating
// conditions are met. The result is sent to a buffered channel stored on the
// command's context so Execute can collect it after the command returns.
//
// The function has a single shared body. Tests inject an env override and a
// fetcher via cliDeps; production code leaves both nil and the defaults are
// used. Command and NoUpdateCheck always come from the real cobra invocation
// so those code paths remain under test.
func startNoticeGoroutine(cmd *cobra.Command, pf *persistentFlags) {
	deps, _ := depsFrom(cmd.Context())

	info := version.Get()
	current := selfupdate.ParseCurrent(info.Version)

	cachePath := filepath.Join(selfupdate.CacheDir(os.Getenv), "update-check.json")
	if deps.NoticeCachePath != "" {
		cachePath = deps.NoticeCachePath
	}
	cache, cacheErr := selfupdate.LoadCache(cachePath)
	var cacheAge time.Duration
	if cacheErr == nil {
		cacheAge = time.Since(cache.CheckedAt)
	} else {
		cacheAge = selfupdate.NoticeCacheTTL + 1 // treat missing/corrupt as expired
	}

	// Build inputs from the real cobra invocation. Command comes from cmd.Name()
	// and NoUpdateCheck from the --no-update-check flag so those gating paths
	// are always exercised through the actual CLI plumbing.
	in := selfupdate.CheckInputs{
		Command:        cmd.Name(),
		NoUpdateCheck:  pf.noUpdateCheck || os.Getenv("OPENSHELL_NO_UPDATE_CHECK") != "",
		StdoutIsTTY:    isTerminal(os.Stdout),
		StderrIsTTY:    isTerminal(os.Stderr),
		CI:             os.Getenv("CI"),
		CurrentVersion: current,
		// CacheAge is fixed above the TTL so ShouldCheck evaluates only the
		// TTY/CI/command/no-update-check gates; the 24 h network-call gate is
		// owned by the fresh-cache short-circuit below.
		CacheAge: selfupdate.NoticeCacheTTL + 1,
	}
	// Override only the environment-derived fields so tests can control TTY
	// state, CI, current version, and cache age without process-level tricks.
	// Command and NoUpdateCheck are intentionally excluded from the override.
	if o := deps.NoticeEnvOverride; o != nil {
		in.StdoutIsTTY = o.StdoutIsTTY
		in.StderrIsTTY = o.StderrIsTTY
		in.CI = o.CI
		in.CurrentVersion = o.CurrentVersion
		in.CacheAge = o.CacheAge
	}

	if !selfupdate.ShouldCheck(in) {
		return
	}

	// If cache is fresh and already has a newer tag, send the notice immediately
	// without a network call — same pattern as the GitHub CLI.
	if cacheErr == nil && cacheAge < selfupdate.NoticeCacheTTL && cache.LatestTag != "" {
		text := selfupdate.NoticeText(in.CurrentVersion.Tag, cache.LatestTag, pf.noColor)
		if text != "" {
			ch := make(chan noticeResult, 1)
			root := cmd.Root()
			root.SetContext(context.WithValue(root.Context(), noticeChKey{}, ch))
			ch <- noticeResult{text: text, latestTag: cache.LatestTag}
		}
		return
	}

	// Cache is stale — start background goroutine to fetch from the API.
	// Store the channel on the ROOT command's context because collectNotice
	// reads root.Context(); cmd here is the subcommand, not the root.
	ch := make(chan noticeResult, 1)
	root := cmd.Root()
	ctx := root.Context()
	root.SetContext(context.WithValue(ctx, noticeChKey{}, ch))

	noticeBudget := 2 * time.Second
	go func() {
		noticeCtx, cancel := context.WithTimeout(ctx, noticeBudget)
		defer cancel()

		f := deps.NoticeFetcher
		if f == nil {
			f = &selfupdate.GitHubFetcher{
				Client: &http.Client{
					Timeout:   noticeBudget,
					Transport: freshTransport(),
				},
				Owner:   "openshift-online",
				Repo:    "openshellctl",
				Version: info.Version,
				// No Token — must not spend or leak credentials.
			}
		}
		rels, err := f.ListReleases(noticeCtx)
		if err != nil {
			ch <- noticeResult{}
			return
		}
		rel, ok := selfupdate.SelectRelease(rels, in.CurrentVersion.IsPrerelease)
		if !ok {
			ch <- noticeResult{}
			return
		}
		latestTag := rel.TagName
		// Update cache so the next invocation doesn't repeat the network call.
		_ = selfupdate.SaveCache(cachePath, selfupdate.Cache{
			CheckedAt: time.Now().UTC(),
			LatestTag: latestTag,
		})
		text := selfupdate.NoticeText(in.CurrentVersion.Tag, latestTag, pf.noColor)
		ch <- noticeResult{text: text, latestTag: latestTag}
	}()
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
	f.BoolVar(&pf.noUpdateCheck, "no-update-check", false, "skip the background update availability check (env OPENSHELL_NO_UPDATE_CHECK)")

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

// noticePrintGrace is how long Execute waits for the background notice after
// the command returns before giving up.
const noticePrintGrace = 250 * time.Millisecond

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
	runErr := root.Execute()

	// Collect the background notice result after the command has fully returned
	// so it can never interleave with raw-mode SSH output.
	collectNotice(root, os.Stderr)

	if runErr != nil {
		// A remote (exec/connect/create-attach) non-zero exit is propagated as the
		// process status without an "Error:" message — the remote command already
		// wrote its own output.
		var remote *RemoteExitError
		if !errors.As(runErr, &remote) && !errors.Is(runErr, context.Canceled) {
			fmt.Fprintf(os.Stderr, "Error: %s\n", runErr.Error())
		}
		code := exitCodeFor(runErr)
		if hint := hintFor(runErr); hint != "" {
			// The "don't repeat yourself" suppression only makes sense for the
			// generic refresh hint — e.g. ErrTokenExpired's own message
			// sometimes already names `openshellctl login` as its remediation.
			// Other hints (permission-denied, no-credentials, audience/issuer
			// mismatches) are never redundant with the error text itself, so
			// they must not be silently dropped by the same check.
			suppress := hint == refreshHint
			if suppress {
				msg := runErr.Error()
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

// collectNotice does a short non-blocking wait for the background notice
// goroutine, prints the notice to w if one arrived in time, and discards
// anything that is not ready.
func collectNotice(root *cobra.Command, w interface{ Write([]byte) (int, error) }) {
	ch, ok := root.Context().Value(noticeChKey{}).(chan noticeResult)
	if !ok || ch == nil {
		return
	}
	select {
	case res := <-ch:
		if res.text != "" {
			fmt.Fprintln(w, res.text)
		}
	case <-time.After(noticePrintGrace):
		// Grace period expired; drop the notice to avoid delaying the user.
	}
}
