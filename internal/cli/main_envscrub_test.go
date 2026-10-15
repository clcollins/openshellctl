package cli

import (
	"os"
	"testing"

	"github.com/spf13/pflag"
)

// TestEnvScrub_CoversEveryEnvBackedInput is the exhaustive-by-construction
// regression test for TestMain's env scrub: every persistent flag's derived
// OPENSHELL_ env name, every name in factEightDirectEnvNames, and VAULT_ADDR/
// VAULT_TOKEN must be unset by the time any test body runs. A new
// env-backed persistent flag is covered automatically via VisitAll; a new
// direct os.Getenv read must be added to factEightDirectEnvNames below.
func TestEnvScrub_CoversEveryEnvBackedInput(t *testing.T) {
	// factEightDirectEnvNames is Verified fact 8 from the ticket: the
	// complete set of OPENSHELL_ names read directly via os.Getenv in
	// production code (internal/cli, pkg/*) outside of viper/persistent-flag
	// binding, as of commit 6d0275f. A new direct os.Getenv read must be
	// added here (see AGENTS.md "Hermetic tests").
	factEightDirectEnvNames := []string{
		"OPENSHELL_GATEWAY",
		"OPENSHELL_GATEWAY_ENDPOINT",
		"OPENSHELL_GATEWAY_INSECURE",
		"OPENSHELL_LIFECYCLE_TIMEOUT",
		"OPENSHELL_NO_BROWSER",
		"OPENSHELL_OIDC_CLIENT_ID",
		"OPENSHELL_OIDC_CLIENT_SECRET",
		"OPENSHELL_PROVISION_TIMEOUT",
		"OPENSHELL_SANDBOX_POLICY",
		"OPENSHELL_SYSTEM_GATEWAY_DIR",
		"OPENSHELL_TOKEN",
		"OPENSHELL_VAULT_FIELD_PREFIX",
		"OPENSHELL_VAULT_KV_MOUNT",
		"OPENSHELL_VAULT_KV_PATH",
		"OPENSHELL_WORKSPACE",
	}

	root := NewRootCommand()

	var names []string
	root.PersistentFlags().VisitAll(func(f *pflag.Flag) {
		names = append(names, envNameForFlag(f.Name))
	})
	names = append(names, factEightDirectEnvNames...)
	names = append(names, "VAULT_ADDR", "VAULT_TOKEN")

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			// Report only the variable name, never its value: several of
			// these (OPENSHELL_TOKEN, OPENSHELL_OIDC_CLIENT_SECRET,
			// VAULT_TOKEN) hold secrets that must never be printed or
			// persisted (AGENTS.md).
			if os.Getenv(name) != "" {
				t.Errorf("%s is set; TestMain must scrub it before tests run", name)
			}
		})
	}
}
