package cli

import (
	"fmt"

	oidc "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/oidc"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/openshift-online/openshellctl/pkg/auth"
	"github.com/openshift-online/openshellctl/pkg/gatewayconfig"
)

func newLoginCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "login",
		Short: "Log in to a gateway via OIDC browser flow",
		Long:  "Open a browser for OIDC authentication and persist the token to disk.",
		Args:  cobra.NoArgs,
		RunE:  runLogin,
	}
	return c
}

// runLogin is newLoginCommand's RunE body, factored out so gateway login
// (internal/cli/gateway.go) can share it — a *cobra.Command can't be added to
// two parents, so the two commands are separate instances with the same RunE.
func runLogin(cmd *cobra.Command, _ []string) error {
	if err := applyVaultAuthSource(cmd.Context()); err != nil {
		return err
	}

	env, err := gatewayconfig.NewOSEnv()
	if err != nil {
		return err
	}

	name := viper.GetString("gateway")
	endpoint := viper.GetString("gateway-endpoint")

	target, err := gatewayconfig.Resolve(env, gatewayconfig.ResolveInput{
		Endpoint: endpoint,
		Name:     name,
	})
	if err != nil {
		return err
	}

	if target.Name == "" {
		return &UsageError{Err: fmt.Errorf("no gateway specified; use -g <name> or set OPENSHELL_GATEWAY")}
	}

	// Persist --vault-kv-mount/--vault-kv-path/--vault-field-prefix (when
	// given) against this gateway, so later commands don't have to repeat
	// them (ROSAENG-74343). Only for an actually-registered gateway
	// (target.Resolved != nil) — an endpoint-only target's Name can be the
	// raw endpoint string (gatewayconfig.Resolve's fallback), which is not a
	// valid gateway name to key a persisted file on. Non-fatal and run
	// before the actual OIDC login attempt below: this only records where
	// the Vault coordinates came from, not whether login succeeds — a
	// failed login shouldn't also discard a correctly-configured Vault
	// pointer the user may fix the real problem (e.g. network) and retry.
	if target.Resolved != nil {
		if perr := persistVaultAuthConfig(target.Name); perr != nil {
			cmd.PrintErrf("warning: could not persist vault auth config: %v\n", perr)
		}
	}

	cmd.PrintErrln("Opening browser for OIDC authentication...")
	return loginAndReport(cmd, target)
}

// loginAndReport runs the OIDC browser login flow for the resolved gateway and
// prints the result. Shared by the login command and the token refresh fallback.
//
// The Go SDK's oidc.Login validates gateway names with a stricter rule than the
// Rust CLI uses when creating directories — the Rust CLI allows spaces in names
// but the Go SDK rejects them. We bypass the SDK's internal gateway resolution
// by passing gatewayName="" and providing issuer/clientID/audience/scopes as
// explicit options from our already-resolved metadata. We then write the token
// back to disk ourselves.
func loginAndReport(cmd *cobra.Command, target *gatewayconfig.Target) error {
	var opts []oidc.LoginOption

	// Prefer metadata from the resolved gateway over viper overrides, since
	// we already loaded the gateway. Viper overrides win when explicitly set.
	if target.Resolved != nil {
		m := target.Resolved.Metadata
		if m.OIDCIssuer != nil && *m.OIDCIssuer != "" {
			opts = append(opts, oidc.WithIssuer(*m.OIDCIssuer))
		}
		opts = append(opts, oidc.WithClientID(m.OIDCClientIDOrDefault()))
		if m.OIDCAudience != nil && *m.OIDCAudience != "" {
			opts = append(opts, oidc.WithAudience(*m.OIDCAudience))
		}
		if m.OIDCScopes != nil && *m.OIDCScopes != "" {
			opts = append(opts, oidc.WithScopes(*m.OIDCScopes))
		}
	}

	// Viper overrides (from env/flags) take precedence.
	if issuer := viper.GetString("oidc-issuer"); issuer != "" {
		opts = append(opts, oidc.WithIssuer(issuer))
	}
	if clientID := viper.GetString("oidc-client-id"); clientID != "" {
		opts = append(opts, oidc.WithClientID(clientID))
	}
	if audience := viper.GetString("oidc-audience"); audience != "" {
		opts = append(opts, oidc.WithAudience(audience))
	}
	if scopes := viper.GetString("oidc-scopes"); scopes != "" {
		opts = append(opts, oidc.WithScopes(scopes))
	}

	// Pass empty gateway name to skip the SDK's internal gateway resolution
	// (which rejects spaces in names). We provide all config via options.
	tok, err := oidc.Login(cmd.Context(), "", opts...)
	if err != nil {
		return err
	}

	// Write the token back to disk in Rust CLI format (oidc_token.json).
	if target.Resolved != nil {
		m := target.Resolved.Metadata
		issuer := ""
		if m.OIDCIssuer != nil {
			issuer = *m.OIDCIssuer
		}
		bundle := auth.DiskBundle{
			AccessToken: tok.AccessToken,
			Issuer:      issuer,
			ClientID:    m.OIDCClientIDOrDefault(),
		}
		if tok.RefreshToken != "" {
			bundle.RefreshToken = &tok.RefreshToken
		}
		if !tok.Expiry.IsZero() {
			exp := tok.Expiry.Unix()
			bundle.ExpiresAt = &exp
		}
		if data, merr := bundle.Marshal(); merr == nil {
			if w, werr := tokenWriterFor(target); werr == nil && w != nil {
				if writeErr := w.WriteFile("oidc_token.json", data, 0o600); writeErr != nil {
					cmd.PrintErrf("warning: could not write token: %v\n", writeErr)
				}
			}
		}
	}

	subject := "unknown"
	if claims, cerr := auth.Inspect(tok.AccessToken); cerr == nil {
		if claims.PreferredUsername != "" {
			subject = claims.PreferredUsername
		} else if claims.Sub != "" {
			subject = claims.Sub
		}
	}

	cmd.PrintErrf("✓ Authenticated to gateway '%s' as %s\n", target.Name, subject)
	return nil
}
