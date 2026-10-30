package doctor

import (
	"strings"
	"testing"
	"time"

	types "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"

	"github.com/openshift-online/openshellctl/pkg/auth"
	"github.com/openshift-online/openshellctl/pkg/sandbox"
)

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func TestCheckEndpointURL(t *testing.T) {
	tests := []struct {
		name       string
		endpoint   string
		wantStatus Status
		wantSub    string
	}{
		{"valid https endpoint", "https://gw.example.com:443", StatusPass, "gw.example.com"},
		{"missing scheme still normalizes", "gw.example.com", StatusPass, "gw.example.com"},
		{"empty endpoint fails", "", StatusFail, "no gateway endpoint"},
		{"unparseable endpoint fails", "https://gw.example.com:notaport", StatusFail, "notaport"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CheckEndpointURL(tt.endpoint)
			if got.Status != tt.wantStatus {
				t.Errorf("Status = %v, want %v (detail: %s)", got.Status, tt.wantStatus, got.Detail)
			}
			if !contains(got.Detail, tt.wantSub) {
				t.Errorf("Detail = %q, want it to contain %q", got.Detail, tt.wantSub)
			}
			if tt.wantStatus == StatusFail && got.NextStep == "" {
				t.Error("expected a NextStep for a failed check")
			}
		})
	}
}

func TestCheckAudience(t *testing.T) {
	tests := []struct {
		name         string
		tok          *auth.Token
		wantAudience string
		wantStatus   Status
	}{
		{"no expected audience configured -> skip", &auth.Token{Audience: []string{"whatever"}}, "", StatusSkip},
		{"audience present -> pass", &auth.Token{Audience: []string{"a", "openshell-cli", "b"}}, "openshell-cli", StatusPass},
		{"audience missing -> fail", &auth.Token{Audience: []string{"other"}}, "openshell-cli", StatusFail},
		{"nil token -> fail", nil, "openshell-cli", StatusFail},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CheckAudience(tt.tok, tt.wantAudience)
			if got.Status != tt.wantStatus {
				t.Errorf("Status = %v, want %v (detail: %s)", got.Status, tt.wantStatus, got.Detail)
			}
			if tt.wantStatus == StatusFail && got.NextStep == "" {
				t.Error("expected a NextStep for a failed check")
			}
		})
	}
}

func TestCheckRolesGateway(t *testing.T) {
	tests := []struct {
		name       string
		user       *types.CurrentUser
		wantStatus Status
		wantSub    string
	}{
		{
			"gateway reports openshell-user -> pass",
			&types.CurrentUser{Roles: []string{"openshell-user", "openshell-admin"}},
			StatusPass, "openshell-user",
		},
		{
			"gateway reports only openshell-user -> pass",
			&types.CurrentUser{Roles: []string{"openshell-user"}},
			StatusPass, "openshell-user",
		},
		{
			"gateway reports only openshell-admin -> pass",
			&types.CurrentUser{Roles: []string{"openshell-admin"}},
			StatusPass, "openshell-admin",
		},
		{
			"gateway reports no gateway roles -> fail",
			&types.CurrentUser{Roles: []string{}},
			StatusFail, "gateway roles",
		},
		{
			"gateway reports nil roles -> fail",
			&types.CurrentUser{Roles: nil},
			StatusFail, "gateway roles",
		},
		{
			"nil user -> fail",
			nil,
			StatusFail, "no current user",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CheckRolesGateway(tt.user)
			if got.Status != tt.wantStatus {
				t.Errorf("Status = %v, want %v (detail: %s)", got.Status, tt.wantStatus, got.Detail)
			}
			if !contains(got.Detail, tt.wantSub) {
				t.Errorf("Detail = %q, want it to contain %q", got.Detail, tt.wantSub)
			}
			if tt.wantStatus == StatusFail && got.NextStep == "" {
				t.Error("expected a NextStep for a failed check")
			}
		})
	}
}

func TestCheckRolesToken(t *testing.T) {
	tests := []struct {
		name       string
		tok        *auth.Token
		wantStatus Status
		wantSub    string
	}{
		{
			"claims have openshell-user -> pass (reliable positive)",
			&auth.Token{Roles: []string{"openshell-user"}},
			StatusPass, "openshell-user",
		},
		{
			"claims have openshell-admin -> pass (reliable positive)",
			&auth.Token{Roles: []string{"openshell-admin"}},
			StatusPass, "openshell-admin",
		},
		{
			"claims have no gateway roles -> skip (advisory only)",
			&auth.Token{Roles: []string{"default-roles-rosa", "offline_access"}},
			StatusSkip, "advisory only",
		},
		{
			"claims empty -> skip (advisory only, exact hyp2 case)",
			&auth.Token{Roles: []string{}},
			StatusSkip, "advisory only",
		},
		{
			"nil token -> skip (advisory only)",
			nil,
			StatusSkip, "advisory only",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CheckRolesToken(tt.tok)
			if got.Status != tt.wantStatus {
				t.Errorf("Status = %v, want %v (detail: %s)", got.Status, tt.wantStatus, got.Detail)
			}
			if !contains(got.Detail, tt.wantSub) {
				t.Errorf("Detail = %q, want it to contain %q", got.Detail, tt.wantSub)
			}
			if got.Status == StatusFail {
				t.Error("CheckRolesToken must never StatusFail — gateway-unreachable fallback")
			}
		})
	}
}

func TestCheckExpiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tests := []struct {
		name       string
		tok        *auth.Token
		wantStatus Status
	}{
		{"future expiry -> pass", &auth.Token{Expiry: now.Add(time.Hour)}, StatusPass},
		{"zero expiry (unknown) -> fail", &auth.Token{}, StatusFail},
		{"already expired -> fail", &auth.Token{Expiry: now.Add(-time.Minute)}, StatusFail},
		{"nil token -> fail", nil, StatusFail},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CheckExpiry(tt.tok, now)
			if got.Status != tt.wantStatus {
				t.Errorf("Status = %v, want %v (detail: %s)", got.Status, tt.wantStatus, got.Detail)
			}
			if tt.wantStatus == StatusFail && got.NextStep == "" {
				t.Error("expected a NextStep for a failed check")
			}
		})
	}
}

func TestCheckOIDCConfigMatch(t *testing.T) {
	tests := []struct {
		name                                 string
		registeredIssuer, registeredAudience string
		discoveredIssuer, discoveredAudience string
		wantStatus                           Status
	}{
		{"nothing discovered -> skip", "https://i", "aud", "", "", StatusSkip},
		{"nothing registered -> skip", "", "", "https://i", "aud", StatusSkip},
		{"issuer and audience match -> pass", "https://i", "aud", "https://i", "aud", StatusPass},
		{"issuer mismatch -> fail", "https://old-issuer", "aud", "https://new-issuer", "aud", StatusFail},
		{"audience mismatch -> fail", "https://i", "old-aud", "https://i", "new-aud", StatusFail},
		{"registered audience empty, not compared -> pass", "https://i", "", "https://i", "whatever", StatusPass},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CheckOIDCConfigMatch(tt.registeredIssuer, tt.registeredAudience, tt.discoveredIssuer, tt.discoveredAudience)
			if got.Status != tt.wantStatus {
				t.Errorf("Status = %v, want %v (detail: %s)", got.Status, tt.wantStatus, got.Detail)
			}
			if tt.wantStatus == StatusFail && got.NextStep == "" {
				t.Error("expected a NextStep for a failed check")
			}
		})
	}
}

func TestCheckProviders(t *testing.T) {
	known := []*types.Provider{
		{Name: "my-openai", Type: "openai"},
	}
	tests := []struct {
		name       string
		known      []*types.Provider
		requested  []string
		wantStatus Status
	}{
		{"no providers requested -> skip", known, nil, StatusSkip},
		{"requested name exists -> pass", known, []string{"my-openai"}, StatusPass},
		{"requested recognized type, not created -> fail", known, []string{"anthropic"}, StatusFail},
		{"requested unrecognized name/type -> fail", known, []string{"not-a-real-provider"}, StatusFail},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CheckProviders(tt.known, tt.requested)
			if got.Status != tt.wantStatus {
				t.Errorf("Status = %v, want %v (detail: %s)", got.Status, tt.wantStatus, got.Detail)
			}
			if tt.wantStatus == StatusFail && got.NextStep == "" {
				t.Error("expected a NextStep for a failed check")
			}
			if tt.wantStatus == StatusFail && contains(got.NextStep, "sandbox provider create") {
				t.Errorf("NextStep must not name a command openshellctl does not implement: %q", got.NextStep)
			}
		})
	}
}

// TestCheckProviders_MissingType_ReusesErrAutoProviderUnsupported pins the
// exact review finding this fixes: a recognized-but-not-yet-created provider
// type's NextStep used to fabricate `openshellctl sandbox provider create`,
// a command `newProviderCommand` (internal/cli/sandbox.go) never registers
// (only list/attach/detach) — and one openshellctl could never honor anyway,
// since it has no local credential access to actually create a provider.
// The hint must instead be sandbox.ErrAutoProviderUnsupported's own message,
// the same one `sandbox create` already gives for this exact case.
func TestCheckProviders_MissingType_ReusesErrAutoProviderUnsupported(t *testing.T) {
	got := CheckProviders(nil, []string{"anthropic"})
	if got.Status != StatusFail {
		t.Fatalf("Status = %v, want fail", got.Status)
	}
	want := (&sandbox.ErrAutoProviderUnsupported{Type: "anthropic"}).Error()
	if got.NextStep != want {
		t.Errorf("NextStep = %q, want %q", got.NextStep, want)
	}
}
