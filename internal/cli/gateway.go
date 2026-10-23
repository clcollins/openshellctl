package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/openshift-online/openshellctl/pkg/auth"
	"github.com/openshift-online/openshellctl/pkg/gatewayconfig"
	"github.com/openshift-online/openshellctl/pkg/output"
)

// newGatewayCommand builds `gateway` (alias `gw`) and its subcommands.
func newGatewayCommand() *cobra.Command {
	g := &cobra.Command{
		Use:     "gateway",
		Aliases: []string{"gw"},
		Short:   "Manage gateway registrations",
	}
	g.AddCommand(
		newGatewayAddCommand(),
		newGatewayListCommand(),
		newGatewaySelectCommand(),
		newGatewayRemoveCommand(),
		newGatewayLogoutCommand(),
		newGatewayLoginCommand(),
	)
	return g
}

// newGatewayLoginCommand is a thin alias of the root login command, under
// `gateway` for discoverability. A *cobra.Command can't be added to two
// parents, so this is a separate instance sharing runLogin (login.go).
func newGatewayLoginCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Log in to a gateway via OIDC browser flow",
		Long:  "Open a browser for OIDC authentication and persist the token to disk.",
		Args:  cobra.NoArgs,
		RunE:  runLogin,
	}
}

// newGatewayListCommand builds `gateway list`.
func newGatewayListCommand() *cobra.Command {
	var format string
	c := &cobra.Command{
		Use:   "list",
		Short: "List registered gateways",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			env, err := gatewayconfig.NewOSEnv()
			if err != nil {
				return err
			}
			gateways, err := gatewayconfig.ListDetailed(env)
			if err != nil {
				return err
			}
			return output.RenderGatewayList(cmd.OutOrStdout(), gateways, output.Format(format))
		},
	}
	c.Flags().StringVarP(&format, "output", "o", "table", "output format: table|json|yaml")
	return c
}

// newGatewaySelectCommand builds `gateway select <name>`.
func newGatewaySelectCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "select <name>",
		Short: "Set the active gateway",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			env, err := gatewayconfig.NewOSEnv()
			if err != nil {
				return err
			}
			if _, err := gatewayconfig.Load(env, name); err != nil {
				return err
			}
			w, err := gatewayconfig.NewOSWriter()
			if err != nil {
				return err
			}
			if err := gatewayconfig.SetActive(w, name); err != nil {
				return err
			}
			cmd.Printf("✓ Active gateway set to '%s'\n", name)
			if warning := gatewayEnvOverrideWarning(name, os.Getenv); warning != "" {
				cmd.PrintErrf("⚠ %s\n", warning)
			}
			return nil
		},
	}
}

// gatewayEnvOverrideWarning reports whether OPENSHELL_GATEWAY will silently
// override the selection just made — selectedName is what `gateway select`
// just wrote as active, but config resolution always prefers an explicit
// OPENSHELL_GATEWAY env var over it. Returns "" when no warning applies
// (the env var is unset, empty, or already matches the selection). Matches
// upstream's gateway_env_override_warning verbatim (gateway.rs:490-499).
func gatewayEnvOverrideWarning(selectedName string, getenv func(string) string) string {
	envName := getenv("OPENSHELL_GATEWAY")
	if envName == "" || envName == selectedName {
		return ""
	}
	return fmt.Sprintf(
		"OPENSHELL_GATEWAY=%s is set and will override this selection.\n  Unset it or run: export OPENSHELL_GATEWAY=%s",
		envName, selectedName,
	)
}

// newGatewayRemoveCommand builds `gateway remove <name>`.
func newGatewayRemoveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a gateway registration",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			env, err := gatewayconfig.NewOSEnv()
			if err != nil {
				return err
			}
			exists, err := gatewayconfig.Exists(env, name)
			if err != nil {
				return err
			}
			if !exists {
				return &gatewayconfig.GatewayNotFoundError{Name: name}
			}
			w, err := gatewayconfig.NewOSWriter()
			if err != nil {
				return err
			}
			if err := gatewayconfig.RemoveGateway(w, name); err != nil {
				return err
			}
			if err := gatewayconfig.ClearActiveIfMatches(w, name); err != nil {
				return err
			}
			cmd.Printf("✓ Gateway registration '%s' removed.\n", name)
			return nil
		},
	}
}

// newGatewayLogoutCommand builds `gateway logout` — defaults to the active
// gateway (or -g/--gateway) like login does, via the same Resolve call.
func newGatewayLogoutCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Remove the cached token for a gateway, keeping its registration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := applyVaultAuthSource(cmd.Context()); err != nil {
				return err
			}

			env, err := gatewayconfig.NewOSEnv()
			if err != nil {
				return err
			}
			target, err := gatewayconfig.Resolve(env, gatewayconfig.ResolveInput{
				Endpoint: viper.GetString("gateway-endpoint"),
				Name:     viper.GetString("gateway"),
			})
			if err != nil {
				return err
			}
			// Resolve's endpoint-only fallback can set target.Name to the raw
			// endpoint when no registered gateway matches it — reject that
			// here with a clear not-found error, rather than either failing
			// with a confusing "invalid gateway name" (when the endpoint
			// contains characters ValidateGatewayName rejects) or silently
			// "succeeding" at logging out of a gateway that was never
			// registered (when it doesn't).
			if _, err := gatewayconfig.Load(env, target.Name); err != nil {
				return err
			}
			w, err := gatewayconfig.NewOSWriter()
			if err != nil {
				return err
			}
			if err := gatewayconfig.Logout(w, target.Name); err != nil {
				return err
			}
			cmd.Printf("✓ Logged out of gateway '%s'\n", target.Name)
			return nil
		},
	}
}

// newGatewayAddCommand builds `gateway add <endpoint>`. It only registers
// OIDC gateways — mTLS and non-OIDC ("edge") gateways are out of scope for
// this epic (ROSAENG-68825) and return typed errors from NewMetadata.
func newGatewayAddCommand() *cobra.Command {
	var name string
	var force bool
	c := &cobra.Command{
		Use:   "add <endpoint>",
		Short: "Register an OIDC gateway",
		Long: "Register a gateway's metadata.json without the upstream Rust binary. " +
			"Without --oidc-issuer, the issuer/audience are discovered from " +
			"<endpoint>/auth/oidc-config; a gateway that doesn't answer that " +
			"endpoint is not supported (pass --oidc-issuer explicitly if it is " +
			"actually OIDC-configured).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGatewayAdd(cmd, args[0], name, force)
		},
	}
	c.Flags().StringVar(&name, "name", "", "gateway name (default: derived from the endpoint host)")
	c.Flags().BoolVar(&force, "force", false, "overwrite an existing registration under the same name")
	return c
}

// runGatewayAdd normalizes the endpoint, discovers or takes an explicit OIDC
// issuer, builds and writes metadata.json, sets the new gateway active, and
// then attempts to authenticate it (see authenticateNewGateway). If
// authentication fails, the registration is rolled back (metadata.json
// removed, the previously-active gateway restored) so a corrected retry
// doesn't hit GatewayExistsError — see authenticateNewGateway's doc comment.
func runGatewayAdd(cmd *cobra.Command, rawEndpoint, name string, force bool) error {
	// Applied before any viper read below, so a Vault-sourced oidc-issuer/
	// client-id/audience/scopes value flows into the persisted metadata.json
	// exactly the way an OPENSHELL_* env var already does — Vault is treated
	// as just another config source, not special-cased for this one command.
	//
	// Uses the ForNewGateway variant (no persisted-config fallback): this
	// runs before the gateway being registered exists, so falling back to
	// whatever OTHER gateway happens to be active's persisted Vault config
	// would leak that gateway's credentials into this one (ROSAENG-74343
	// review). An explicit --vault-kv-mount/--vault-kv-path on this `gateway
	// add` invocation itself is unaffected — only the ambient fallback is
	// disabled.
	if err := applyVaultAuthSourceForNewGateway(cmd.Context()); err != nil {
		return err
	}

	endpoint, err := gatewayconfig.EnsureScheme(rawEndpoint)
	if err != nil {
		return err
	}

	issuerOverride := viper.GetString("oidc-issuer")
	var discovered *gatewayconfig.DiscoveredOIDC
	var discoveryErr error
	if issuerOverride == "" {
		iss, aud, derr := oidcConfigFetcher(cmd.Context(), endpoint)
		if derr == nil {
			discovered = &gatewayconfig.DiscoveredOIDC{Issuer: iss, Audience: aud}
		} else {
			// Threaded into EdgeGatewayUnsupportedError (via NewMetadata) so
			// a DNS failure, TLS error, or timeout is reported as what it
			// actually is, not collapsed into the same "doesn't look
			// OIDC-configured" message a real non-200 response gets.
			discoveryErr = derr
		}
	}

	m, err := gatewayconfig.NewMetadata(gatewayconfig.AddInput{
		Endpoint:     endpoint,
		Name:         name,
		OIDCIssuer:   issuerOverride,
		OIDCClientID: viper.GetString("oidc-client-id"),
		OIDCAudience: viper.GetString("oidc-audience"),
		OIDCScopes:   viper.GetString("oidc-scopes"),
		Discovered:   discovered,
		DiscoveryErr: discoveryErr,
	})
	if err != nil {
		return err
	}

	if viper.GetString("oidc-client-id") == "" && clientSecretProvider(viper.GetString("client-secret-file")) != nil {
		cmd.PrintErrln("warning: no --oidc-client-id given; using the default 'openshell-cli' — a " +
			"service account typically needs its own client id, or authentication will likely fail.")
	}

	env, err := gatewayconfig.NewOSEnv()
	if err != nil {
		return err
	}
	w, err := gatewayconfig.NewOSWriter()
	if err != nil {
		return err
	}
	previousActive, hadActive := readActiveGateway(env)
	// Snapshot whatever --force is about to overwrite, so a failed auth
	// attempt can restore it instead of rollbackFailedAdd deleting a
	// previously-working registration outright (see rollbackFailedAdd).
	previousMetadata, _ := w.ReadFile(gatewayconfig.GatewayDir(m.Name) + "/metadata.json")
	previousToken, _ := w.ReadFile(gatewayconfig.GatewayDir(m.Name) + "/oidc_token.json")
	previousVaultAuth, _ := w.ReadFile(gatewayconfig.GatewayDir(m.Name) + "/vault_auth.json")
	if err := gatewayconfig.WriteGateway(w, env, m, force); err != nil {
		return err
	}
	if err := gatewayconfig.SetActive(w, m.Name); err != nil {
		return err
	}
	// Persist --vault-kv-mount/--vault-kv-path/--vault-field-prefix (when
	// given) against this gateway, so later commands don't have to repeat
	// them (ROSAENG-74343). Non-fatal: the gateway is already registered and
	// usable via flags/env either way, so a write failure here is a warning,
	// not a reason to fail the whole `gateway add`.
	if perr := persistVaultAuthConfig(m.Name); perr != nil {
		cmd.PrintErrf("warning: could not persist vault auth config: %v\n", perr)
	}
	cmd.Printf("✓ Gateway '%s' added and set as active\n", m.Name)
	cmd.Printf("  Endpoint: %s\n", endpoint)
	cmd.Printf("  Auth: oidc\n")
	cmd.Println()

	if err := authenticateNewGateway(cmd, m.Name); err != nil {
		rollbackFailedAdd(w, m.Name, previousActive, hadActive, previousMetadata, previousToken, previousVaultAuth)
		return err
	}
	return nil
}

// readActiveGateway reads the current active_gateway pointer (user tree
// only — the tree gateway add ever writes to), for rollbackFailedAdd to
// restore if authentication fails. hadActive is false when there was none.
func readActiveGateway(env gatewayconfig.Env) (name string, hadActive bool) {
	active, err := gatewayconfig.ActiveGateway(env)
	if err != nil {
		return "", false
	}
	return active, true
}

// rollbackFailedAdd undoes a just-written registration after
// authenticateNewGateway fails, restoring the previously-active gateway (or
// clearing active_gateway entirely if there wasn't one). Without this, a
// wrong secret or an unreachable issuer leaves a half-registered, active
// gateway behind, and a corrected retry hits GatewayExistsError until the
// user discovers `gateway remove` themselves.
//
// previousMetadata/previousToken/previousVaultAuth are the exact bytes a
// --force overwrite replaced, or nil when there was nothing to overwrite
// (the normal, non-force path). When non-nil, rollback restores those bytes
// instead of deleting the files outright — otherwise force-overwriting a
// working registration and then failing to authenticate the new one would
// destroy the previous, still-good registration too, which is strictly
// worse than the pre-rollback behavior for that case.
func rollbackFailedAdd(w gatewayconfig.Writer, failedName, previousActive string, hadActive bool, previousMetadata, previousToken, previousVaultAuth []byte) {
	metadataRel := gatewayconfig.GatewayDir(failedName) + "/metadata.json"
	tokenRel := gatewayconfig.GatewayDir(failedName) + "/oidc_token.json"
	vaultAuthRel := gatewayconfig.GatewayDir(failedName) + "/vault_auth.json"
	if previousMetadata != nil {
		_ = w.WriteFile(metadataRel, previousMetadata, 0o600)
	} else {
		_ = w.Remove(metadataRel)
	}
	if previousToken != nil {
		_ = w.WriteFile(tokenRel, previousToken, 0o600)
	} else {
		_ = w.Remove(tokenRel)
	}
	if previousVaultAuth != nil {
		_ = w.WriteFile(vaultAuthRel, previousVaultAuth, 0o600)
	} else {
		_ = w.Remove(vaultAuthRel)
	}
	_ = gatewayconfig.ClearActiveIfMatches(w, failedName)
	if hadActive {
		_ = gatewayconfig.SetActive(w, previousActive)
	}
}

// shouldRegisterOnly is the pure decision authenticateNewGateway's "no
// client secret" branch makes: register the gateway and print a hint rather
// than attempting the interactive browser login flow. Extracted as its own
// function so it has a hermetic unit test (TestShouldRegisterOnly) — the
// alternative, --no-browser=false, drives a real oidc.Login call (network +
// a real browser launch), which must never run inside a test.
func shouldRegisterOnly(hasSecret, noBrowser bool) bool {
	return !hasSecret && noBrowser
}

// authenticateNewGateway attempts to authenticate the just-registered
// gateway:
//   - a client secret is configured: client-credentials exchange, printing
//     "✓ Authenticated via client credentials" and writing oidc_token.json
//     with a real expires_at (the rosa-agent CronJobs' Python token writer
//     omits it; see metadata_marshal_test.go).
//   - no secret, --no-browser/OPENSHELL_NO_BROWSER set: register-only, print
//     a hint, and succeed (exit 0) — the interim behavior ROSAENG-68835 (the
//     device-code login spike) already specifies for this exact case, not a
//     guess.
//   - no secret, browser available: fall back to the existing browser login
//     flow (loginAndReport), matching upstream's own default for a human
//     without a client secret.
//
// Resolves env fresh (not the Env from before WriteGateway wrote the gateway
// dir) — NewOSEnv snapshots whether the user config dir exists at call time,
// so reusing an Env obtained before the directory existed would see a nil
// UserFS even after the write.
func authenticateNewGateway(cmd *cobra.Command, name string) error {
	env, err := gatewayconfig.NewOSEnv()
	if err != nil {
		return err
	}
	target, err := gatewayconfig.Resolve(env, gatewayconfig.ResolveInput{Name: name})
	if err != nil {
		return err
	}

	secretProvider := clientSecretProvider(viper.GetString("client-secret-file"))
	hasSecret := secretProvider != nil
	if !hasSecret {
		if shouldRegisterOnly(hasSecret, viper.GetBool("no-browser")) {
			cmd.PrintErrln("Gateway registered. No client secret found — log in with " +
				"`openshellctl gateway login`, or export OPENSHELL_OIDC_CLIENT_SECRET.")
			return nil
		}
		return loginAndReport(cmd, target)
	}

	// Drive the exact same client-credentials resolution whoami/token show
	// use (resolveAuth -> resolveTokenSource), rather than re-implementing
	// auth.ResolveInput construction here: PR #33 consolidated cliDeps
	// injection into resolveAuth/dialOrInjected specifically so there is one
	// place that decides "is this call test-injected," not several that
	// could disagree. Pointing viper's "gateway" flag at the freshly-written
	// name makes resolveTokenSource resolve the same target we already have.
	viper.Set("gateway", name)
	viper.Set("gateway-endpoint", "")
	src, _, err := resolveAuth(cmd)
	if err != nil {
		return err
	}
	tok, err := src.Token(cmd.Context())
	if err != nil {
		return err
	}
	cmd.Println("✓ Authenticated via client credentials")

	w, err := tokenWriterFor(target)
	if err != nil {
		return err
	}
	if w == nil {
		return nil
	}
	return auth.WriteBundle(w, tok)
}
