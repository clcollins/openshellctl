package vaultconfig

import (
	"errors"
	"reflect"
	"testing"
)

func TestNormalizePrefix(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		want   string
	}{
		{"empty stays empty", "", ""},
		{"no trailing dash unchanged", "hypershell", "hypershell"},
		{"single trailing dash stripped", "hypershell-", "hypershell"},
		{"multiple trailing dashes all stripped", "hypershell---", "hypershell"},
		{"only dashes becomes empty", "---", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizePrefix(tt.prefix); got != tt.want {
				t.Errorf("NormalizePrefix(%q) = %q, want %q", tt.prefix, got, tt.want)
			}
		})
	}
}

func TestSecretFieldName(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		suffix string
		want   string
	}{
		{"no prefix", "", "gateway", "gateway"},
		{"with prefix", "hypershell", "oidc-client-id", "hypershell-oidc-client-id"},
		{"prefix with trailing dash normalized", "hypershell-", "oidc-client-id", "hypershell-oidc-client-id"},
		{"prefix with multiple trailing dashes normalized", "hypershell--", "oidc-issuer", "hypershell-oidc-issuer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SecretFieldName(tt.prefix, tt.suffix); got != tt.want {
				t.Errorf("SecretFieldName(%q, %q) = %q, want %q", tt.prefix, tt.suffix, got, tt.want)
			}
		})
	}
}

func TestViperKey(t *testing.T) {
	tests := []struct {
		suffix string
		want   string
	}{
		{"gateway", "gateway"},
		{"gateway-endpoint", "gateway-endpoint"},
		{"oidc-issuer", "oidc-issuer"},
		{"oidc-client-id", "oidc-client-id"},
		{"oidc-client-secret", "oidc.client_secret"},
		{"oidc-audience", "oidc-audience"},
		{"oidc-scopes", "oidc-scopes"},
	}
	for _, tt := range tests {
		t.Run(tt.suffix, func(t *testing.T) {
			if got := ViperKey(tt.suffix); got != tt.want {
				t.Errorf("ViperKey(%q) = %q, want %q", tt.suffix, got, tt.want)
			}
		})
	}
}

func TestExtractFields(t *testing.T) {
	tests := []struct {
		name    string
		data    map[string]any
		prefix  string
		want    map[string]string
		wantErr error
	}{
		{
			name:   "empty secret yields empty fields",
			data:   map[string]any{},
			prefix: "",
			want:   map[string]string{},
		},
		{
			name:   "missing fields are skipped, not errors",
			data:   map[string]any{"oidc-client-id": "abc"},
			prefix: "",
			want:   map[string]string{"oidc-client-id": "abc"},
		},
		{
			name:   "prefix applied to lookups",
			data:   map[string]any{"hypershell-oidc-client-id": "abc", "hypershell-oidc-client-secret": "shh"},
			prefix: "hypershell",
			want:   map[string]string{"oidc-client-id": "abc", "oidc-client-secret": "shh"},
		},
		{
			name:   "trailing dash on prefix does not double up",
			data:   map[string]any{"hypershell-oidc-issuer": "https://issuer"},
			prefix: "hypershell-",
			want:   map[string]string{"oidc-issuer": "https://issuer"},
		},
		{
			name:   "unknown keys in the secret are ignored",
			data:   map[string]any{"random-key": "value", "oidc-issuer": "https://x"},
			prefix: "",
			want:   map[string]string{"oidc-issuer": "https://x"},
		},
		{
			name:   "token field is not a recognized suffix and is ignored",
			data:   map[string]any{"token": "should-not-appear"},
			prefix: "",
			want:   map[string]string{},
		},
		{
			name:   "all seven fields present",
			prefix: "",
			data: map[string]any{
				"gateway":            "rosa",
				"gateway-endpoint":   "https://gw.example.com",
				"oidc-issuer":        "https://issuer",
				"oidc-client-id":     "abc",
				"oidc-client-secret": "shh",
				"oidc-audience":      "openshell-cli",
				"oidc-scopes":        "openid profile",
			},
			want: map[string]string{
				"gateway":            "rosa",
				"gateway-endpoint":   "https://gw.example.com",
				"oidc-issuer":        "https://issuer",
				"oidc-client-id":     "abc",
				"oidc-client-secret": "shh",
				"oidc-audience":      "openshell-cli",
				"oidc-scopes":        "openid profile",
			},
		},
		{
			name:    "non-string field value is an error",
			data:    map[string]any{"oidc-client-id": 123},
			prefix:  "",
			wantErr: &ErrFieldNotString{Field: "oidc-client-id"},
		},
		{
			name:    "non-string field value with prefix reports the prefixed field name",
			data:    map[string]any{"hypershell-oidc-audience": []string{"nope"}},
			prefix:  "hypershell",
			wantErr: &ErrFieldNotString{Field: "hypershell-oidc-audience"},
		},
		{
			name:   "empty string value is preserved, not treated as missing",
			data:   map[string]any{"oidc-scopes": ""},
			prefix: "",
			want:   map[string]string{"oidc-scopes": ""},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractFields(tt.data, tt.prefix)
			if tt.wantErr != nil {
				if err == nil {
					t.Fatalf("expected error %v, got nil", tt.wantErr)
				}
				var fieldErr *ErrFieldNotString
				if !errors.As(err, &fieldErr) {
					t.Fatalf("expected *ErrFieldNotString, got %T: %v", err, err)
				}
				wantField := tt.wantErr.(*ErrFieldNotString).Field
				if fieldErr.Field != wantField {
					t.Errorf("ErrFieldNotString.Field = %q, want %q", fieldErr.Field, wantField)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ExtractFields() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestToViperDefaults(t *testing.T) {
	allFields := map[string]string{
		"gateway":            "rosa",
		"gateway-endpoint":   "https://gw.example.com",
		"oidc-issuer":        "https://issuer",
		"oidc-client-id":     "abc",
		"oidc-client-secret": "shh",
		"oidc-audience":      "openshell-cli",
		"oidc-scopes":        "openid profile",
	}

	tests := []struct {
		name   string
		fields map[string]string
		ov     Overrides
		want   map[string]string
	}{
		{
			name:   "no overrides maps every field to its viper key",
			fields: allFields,
			ov:     Overrides{},
			want: map[string]string{
				"gateway":            "rosa",
				"gateway-endpoint":   "https://gw.example.com",
				"oidc-issuer":        "https://issuer",
				"oidc-client-id":     "abc",
				"oidc.client_secret": "shh",
				"oidc-audience":      "openshell-cli",
				"oidc-scopes":        "openid profile",
			},
		},
		{
			name:   "empty-string field values are skipped",
			fields: map[string]string{"oidc-issuer": "", "oidc-audience": "openshell-cli"},
			ov:     Overrides{},
			want:   map[string]string{"oidc-audience": "openshell-cli"},
		},
		{
			name:   "gateway override excludes both gateway and gateway-endpoint",
			fields: allFields,
			ov:     Overrides{Gateway: true},
			want: map[string]string{
				"oidc-issuer":        "https://issuer",
				"oidc-client-id":     "abc",
				"oidc.client_secret": "shh",
				"oidc-audience":      "openshell-cli",
				"oidc-scopes":        "openid profile",
			},
		},
		{
			name:   "gateway-endpoint override also excludes both halves of the pair",
			fields: allFields,
			ov:     Overrides{GatewayEndpoint: true},
			want: map[string]string{
				"oidc-issuer":        "https://issuer",
				"oidc-client-id":     "abc",
				"oidc.client_secret": "shh",
				"oidc-audience":      "openshell-cli",
				"oidc-scopes":        "openid profile",
			},
		},
		{
			name:   "oidc client id override excludes all cluster fields including scopes",
			fields: allFields,
			ov:     Overrides{OIDCClientID: true},
			want: map[string]string{
				"gateway":          "rosa",
				"gateway-endpoint": "https://gw.example.com",
			},
		},
		{
			name:   "client secret source override excludes all cluster fields including scopes",
			fields: allFields,
			ov:     Overrides{ClientSecretSource: true},
			want: map[string]string{
				"gateway":          "rosa",
				"gateway-endpoint": "https://gw.example.com",
			},
		},
		{
			name:   "both pairs overridden leaves nothing (all cluster fields blocked, gateway pair blocked)",
			fields: allFields,
			ov:     Overrides{Gateway: true, OIDCClientID: true},
			want:   map[string]string{},
		},
		{
			name:   "client pair override also excludes oidc-issuer, oidc-audience, and oidc-scopes",
			fields: allFields,
			ov:     Overrides{OIDCClientID: true},
			want: map[string]string{
				"gateway":          "rosa",
				"gateway-endpoint": "https://gw.example.com",
			},
		},
		{
			name:   "client secret source override also excludes oidc-issuer, oidc-audience, and oidc-scopes",
			fields: allFields,
			ov:     Overrides{ClientSecretSource: true},
			want: map[string]string{
				"gateway":          "rosa",
				"gateway-endpoint": "https://gw.example.com",
			},
		},
		{
			name:   "explicit oidc-issuer override prevents vault from setting oidc-issuer",
			fields: allFields,
			ov:     Overrides{OIDCIssuer: true},
			want: map[string]string{
				"gateway":            "rosa",
				"gateway-endpoint":   "https://gw.example.com",
				"oidc-client-id":     "abc",
				"oidc.client_secret": "shh",
				"oidc-audience":      "openshell-cli",
				"oidc-scopes":        "openid profile",
			},
		},
		{
			name:   "explicit oidc-audience override prevents vault from setting oidc-audience",
			fields: allFields,
			ov:     Overrides{OIDCAudience: true},
			want: map[string]string{
				"gateway":            "rosa",
				"gateway-endpoint":   "https://gw.example.com",
				"oidc-issuer":        "https://issuer",
				"oidc-client-id":     "abc",
				"oidc.client_secret": "shh",
				"oidc-scopes":        "openid profile",
			},
		},
		{
			name:   "gateway override alone does not exclude oidc-issuer or oidc-audience",
			fields: allFields,
			ov:     Overrides{Gateway: true},
			want: map[string]string{
				"oidc-issuer":        "https://issuer",
				"oidc-client-id":     "abc",
				"oidc.client_secret": "shh",
				"oidc-audience":      "openshell-cli",
				"oidc-scopes":        "openid profile",
			},
		},
		{
			name:   "partial secret with no overrides passes through untouched",
			fields: map[string]string{"oidc-client-id": "abc", "oidc-client-secret": "shh"},
			ov:     Overrides{},
			want:   map[string]string{"oidc-client-id": "abc", "oidc.client_secret": "shh"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToViperDefaults(tt.fields, tt.ov)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ToViperDefaults() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
