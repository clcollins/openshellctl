package cli

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/spf13/viper"

	"github.com/openshift-online/openshellctl/pkg/gatewayconfig"
	"github.com/openshift-online/openshellctl/pkg/vaultconfig"
)

// vaultDeps holds a test-injected VaultReader, mirroring cliDeps (deps.go) but
// kept separate: cliDeps is explicitly documented as read in exactly one
// place (resolveAuth), and applyVaultAuthSource is called from several
// call sites that don't go through resolveAuth at all (runLogin,
// runGatewayAdd, gateway logout, sandbox ssh-config) — overloading cliDeps
// here would make that doc comment wrong for every one of them.
type vaultDeps struct {
	Reader vaultconfig.VaultReader
}

type vaultDepsCtxKey struct{}

func withVaultDeps(ctx context.Context, d vaultDeps) context.Context {
	return context.WithValue(ctx, vaultDepsCtxKey{}, d)
}

func vaultDepsFrom(ctx context.Context) (vaultDeps, bool) {
	d, ok := ctx.Value(vaultDepsCtxKey{}).(vaultDeps)
	return d, ok
}

// applyVaultAuthSource fills missing gateway/OIDC viper keys from a Vault KV
// secret, when --vault-kv-mount/--vault-kv-path (or their env vars, or a
// persisted vault_auth.json for the invocation's resolved gateway) are set.
// It is a complete no-op otherwise.
//
// Called individually from every command that reads one of the affected
// viper keys (resolveTokenSource, runLogin, gateway logout, sandbox
// ssh-config) rather than from root's PersistentPreRunE, so a command with no
// auth dependency at all (version, policy lint, ...) never needs a live
// Vault session just because OPENSHELL_VAULT_KV_MOUNT/PATH happen to be set
// in the environment or persisted for the active gateway.
//
// `gateway add` does NOT use this — see applyVaultAuthSourceForNewGateway.
func applyVaultAuthSource(ctx context.Context) error {
	return applyVaultAuthSourceMemoized(ctx, true)
}

// applyVaultAuthSourceForNewGateway is applyVaultAuthSource, but never falls
// back to a persisted vault_auth.json for whatever gateway this invocation
// would otherwise resolve to (active_gateway, or an existing -g/endpoint
// match) — used only by `gateway add`'s very first step, which runs BEFORE
// the gateway being registered exists.
//
// Without this distinction, a plain `gateway add <endpoint>` with no
// --vault-kv-* flags of its own would silently read whatever OTHER gateway
// happens to be active's persisted Vault secret — via the exact same
// fallback that makes persistence convenient for every other command — and
// leak its oidc-client-id/oidc-client-secret into the brand-new gateway's
// metadata.json and authentication (ROSAENG-74343 review). `gateway add`
// registering gateway B must never depend on, or be contaminated by,
// whatever gateway A happens to be active at the time.
//
// Explicit --vault-kv-mount/--vault-kv-path flags passed to `gateway add`
// itself are unaffected by this — they're read directly, not through the
// persisted-fallback branch this disables, so the documented "pass Vault
// flags at gateway add time" flow works exactly as before.
func applyVaultAuthSourceForNewGateway(ctx context.Context) error {
	return applyVaultAuthSourceMemoized(ctx, false)
}

// vaultAuthOnce/vaultAuthResult memoize applyVaultAuthSource for the lifetime
// of the process. Without this, a single command whose flow reaches more than
// one of the call sites in applyVaultAuthSource's doc comment — e.g.
// `gateway add` with a Vault-sourced client secret, which calls it directly
// and then again via authenticateNewGateway -> resolveAuth -> resolveTokenSource
// — issues a second full Vault KV read for the exact same mount/path/prefix
// within one process. That's wasteful at best; at worst, with a single-use or
// response-wrapped Vault token (a common pattern for short-lived CI
// credentials — exactly what this feature targets), the first read consumes
// the token and the second one fails outright. The flags driving mount/path/
// prefix are parsed once per process and never change, so caching the result
// for the process lifetime is always correct, not just an optimization.
//
// This also means allowPersistedFallback is only honored on the FIRST call
// in a process: `gateway add`'s initial call (false) always wins over a
// later call from within the same add flow (true, via
// authenticateNewGateway -> resolveTokenSource) for that same invocation,
// which is exactly the desired behavior — once an add has decided not to
// consult ambient persisted config, that decision holds for its whole run.
var (
	vaultAuthOnce   sync.Once
	vaultAuthResult error
)

func applyVaultAuthSourceMemoized(ctx context.Context, allowPersistedFallback bool) error {
	vaultAuthOnce.Do(func() {
		vaultAuthResult = resolveVaultAuthSource(ctx, allowPersistedFallback)
	})
	return vaultAuthResult
}

func resolveVaultAuthSource(ctx context.Context, allowPersistedFallback bool) error {
	// --no-vault/OPENSHELL_NO_VAULT is a full escape hatch: once a gateway
	// has a persisted vault_auth.json, every command resolving auth against
	// it needs a live Vault session by default (that's the whole point of
	// persistence) — this is the one way to opt out for a single invocation
	// without deleting the file (ROSAENG-74343 review). It disables explicit
	// --vault-kv-mount/--vault-kv-path too, not just the persisted fallback:
	// it means "don't touch Vault at all right now," not "ignore only the
	// remembered config."
	if viper.GetBool("no-vault") {
		return nil
	}

	mount := viper.GetString("vault-kv-mount")
	path := viper.GetString("vault-kv-path")
	prefix := viper.GetString("vault-field-prefix")

	// Neither flag/env set: fall back to whatever mount/path/prefix a prior
	// `gateway add`/`login` persisted for the gateway this invocation already
	// resolves to (see persistVaultAuthConfig and
	// gatewayconfig.VaultAuthConfig) — ROSAENG-74343. Explicit flag/env still
	// wins outright: this only runs when BOTH are empty, same precedence
	// level "no Vault source configured" already occupied, just no longer
	// requiring the flags on every single invocation. Skipped entirely when
	// allowPersistedFallback is false (see applyVaultAuthSourceForNewGateway).
	if allowPersistedFallback && mount == "" && path == "" {
		if cfg, ok := persistedVaultAuthConfig(); ok {
			mount = cfg.Mount
			path = cfg.Path
			if prefix == "" {
				prefix = cfg.FieldPrefix
			}
		}
	}

	if mount == "" && path == "" {
		if prefix != "" {
			return &UsageError{Err: fmt.Errorf("--vault-field-prefix has no effect without --vault-kv-mount and --vault-kv-path")}
		}
		return nil
	}
	if mount == "" || path == "" {
		return &UsageError{Err: fmt.Errorf("--vault-kv-mount and --vault-kv-path must be set together")}
	}

	reader, err := vaultReaderFor(ctx)
	if err != nil {
		return err
	}

	data, err := reader.ReadSecret(ctx, mount, path)
	if err != nil {
		return err
	}

	fields, err := vaultconfig.ExtractFields(data, prefix)
	if err != nil {
		return err
	}

	overrides := vaultconfig.Overrides{
		Gateway:            viper.GetString("gateway") != "",
		GatewayEndpoint:    viper.GetString("gateway-endpoint") != "",
		OIDCClientID:       viper.GetString("oidc-client-id") != "",
		ClientSecretSource: viper.GetString("oidc.client_secret") != "" || viper.GetString("client-secret-file") != "",
	}
	for key, val := range vaultconfig.ToViperDefaults(fields, overrides) {
		viper.SetDefault(key, val)
	}
	return nil
}

// persistedVaultAuthConfig looks up a previously-persisted Vault mount/path/
// prefix (gatewayconfig.VaultAuthConfig) for the gateway this invocation
// already resolves to via signals that exist *before* any Vault involvement.
// It resolves that gateway through gatewayconfig.Resolve — the exact same
// function and inputs (--gateway-endpoint, --gateway/OPENSHELL_GATEWAY, else
// active_gateway) every real call site uses — rather than reimplementing a
// partial version of that logic, so this can never disagree with which
// gateway the invocation is actually about. An earlier version of this
// function only checked --gateway/active_gateway and ignored
// --gateway-endpoint entirely, which could load gateway A's persisted config
// for a command actually targeting gateway B by endpoint (ROSAENG-74343
// review).
//
// It deliberately never considers a Vault-sourced "gateway"/"gateway-endpoint"
// field: this function only runs when neither --vault-kv-mount nor
// --vault-kv-path is set, so there would be no mount/path yet to resolve such
// a value with — resolving the gateway via Vault here would be circular.
// gatewayconfig.Resolve itself has no notion of Vault, so this is safe by
// construction. An endpoint-only invocation matching no registered gateway,
// or one with no resolvable gateway at all, simply has nothing to look up
// and returns ok=false, falling through to "no Vault source configured"
// exactly as before this feature existed.
func persistedVaultAuthConfig() (gatewayconfig.VaultAuthConfig, bool) {
	env, err := gatewayconfig.NewOSEnv()
	if err != nil {
		return gatewayconfig.VaultAuthConfig{}, false
	}
	target, err := gatewayconfig.Resolve(env, gatewayconfig.ResolveInput{
		Endpoint: viper.GetString("gateway-endpoint"),
		Name:     viper.GetString("gateway"),
	})
	if err != nil || target.Resolved == nil {
		return gatewayconfig.VaultAuthConfig{}, false
	}
	return gatewayconfig.LoadVaultAuthConfig(target.Resolved)
}

// persistVaultAuthConfig saves the --vault-kv-mount/--vault-kv-path/
// --vault-field-prefix this invocation was given to gatewayName's
// vault_auth.json (gatewayconfig.SaveVaultAuthConfig), so a later command
// against the same gateway doesn't have to repeat them.
//
// A no-op when mount/path aren't both currently set in viper or gatewayName
// is empty. Note this reads viper directly rather than taking the
// already-resolved mount/path as parameters: resolveVaultAuthSource leaves
// vault-kv-mount/vault-kv-path untouched in viper even when it fell back to
// a persisted value (see resolveVaultAuthSource), so an invocation that
// merely *reused* a persisted config — rather than being given explicit
// flags/env of its own — sees mount=="" here and correctly skips rewriting
// the file with the same bytes.
func persistVaultAuthConfig(gatewayName string) error {
	mount := viper.GetString("vault-kv-mount")
	path := viper.GetString("vault-kv-path")
	if viper.GetBool("no-vault") || mount == "" || path == "" || gatewayName == "" {
		return nil
	}
	w, err := gatewayconfig.NewOSWriter()
	if err != nil {
		return err
	}
	return gatewayconfig.SaveVaultAuthConfig(w, gatewayName, gatewayconfig.VaultAuthConfig{
		Mount:       mount,
		Path:        path,
		FieldPrefix: viper.GetString("vault-field-prefix"),
	})
}

// vaultReaderFor returns the context-injected VaultReader for tests, or
// builds the real Vault-API-backed one.
func vaultReaderFor(ctx context.Context) (vaultconfig.VaultReader, error) {
	if d, ok := vaultDepsFrom(ctx); ok && d.Reader != nil {
		return d.Reader, nil
	}
	return realVaultReader()
}

// realVaultReader is the production I/O shell: build a Vault client (failing
// fast if VAULT_ADDR is unset), resolve a token if the SDK didn't already
// pick one up from VAULT_TOKEN, and wrap the client as a VaultReader.
// openshellctl never performs `vault login` itself.
func realVaultReader() (vaultconfig.VaultReader, error) {
	client, err := vaultconfig.NewAPIClient(os.Getenv)
	if err != nil {
		return nil, err
	}
	if client.Token() == "" {
		token, err := vaultconfig.ResolveToken(vaultconfig.TokenEnv{
			Getenv:   os.Getenv,
			ReadFile: os.ReadFile,
			HomeDir:  os.UserHomeDir,
		})
		if err != nil {
			return nil, err
		}
		client.SetToken(token)
	}
	return vaultconfig.NewAPIReader(client), nil
}
