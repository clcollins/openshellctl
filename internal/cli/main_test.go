package cli

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "openshellctl-test-*")
	if err != nil {
		panic(err)
	}

	for k, v := range map[string]string{"HOME": tmp, "XDG_CONFIG_HOME": tmp} {
		if err := os.Setenv(k, v); err != nil {
			panic(err)
		}
	}
	// Scrub every OPENSHELL_*/VAULT_* variable by prefix rather than by a
	// hand-maintained list (see envNamesToScrub, main_env_test.go, and the
	// exhaustive regression check in TestEnvScrub_CoversEveryEnvBackedInput,
	// main_envscrub_test.go): a hard-coded list silently stops covering new
	// env-backed flags or os.Getenv reads.
	for _, k := range envNamesToScrub(os.Environ(), []string{"OPENSHELL_", "VAULT_"}) {
		if err := os.Unsetenv(k); err != nil {
			panic(err)
		}
	}

	code := m.Run()
	_ = os.RemoveAll(tmp)
	os.Exit(code)
}
