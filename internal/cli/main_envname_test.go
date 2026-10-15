package cli

import (
	"strings"
	"testing"
)

// envNameForFlag returns the OPENSHELL_ environment variable name viper
// derives for a persistent flag named flagName, per bindViper (root.go) and
// envKeyReplacer (root.go): "OPENSHELL_" + the flag name with "-" and "."
// replaced by "_", upper-cased. It reuses envKeyReplacer rather than
// reimplementing the "-"/"." -> "_" mapping, so the two can never drift.
func envNameForFlag(flagName string) string {
	return "OPENSHELL_" + strings.ToUpper(envKeyReplacer().Replace(flagName))
}

func TestEnvNameForFlag(t *testing.T) {
	tests := []struct {
		flagName string
		want     string
	}{
		{"gateway", "OPENSHELL_GATEWAY"},
		{"vault-kv-mount", "OPENSHELL_VAULT_KV_MOUNT"},
		{"oidc.client_secret", "OPENSHELL_OIDC_CLIENT_SECRET"},
		{"gateway-endpoint", "OPENSHELL_GATEWAY_ENDPOINT"},
	}

	for _, tt := range tests {
		t.Run(tt.flagName, func(t *testing.T) {
			if got := envNameForFlag(tt.flagName); got != tt.want {
				t.Errorf("envNameForFlag(%q) = %q, want %q", tt.flagName, got, tt.want)
			}
		})
	}
}
