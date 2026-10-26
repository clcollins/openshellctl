package sandbox

import (
	"fmt"
	"time"

	types "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"

	"github.com/openshift-online/openshellctl/pkg/api/v1alpha1"
)

// CreateRequest is the fully-resolved, validated create input (flags + manifest
// merged). Policy is already loaded into the SDK type.
type CreateRequest struct {
	Workspace, Name      string
	Labels               map[string]string
	Image                string
	Command              []string
	TTY                  *bool
	Env                  map[string]string
	Providers            []string
	AutoProviders        *bool
	CPU, Memory          string
	GPU                  *v1alpha1.GPU
	DriverConfig         map[string]any
	Policy               *types.SandboxPolicy
	ApprovalMode         string // "" == manual
	NoCredentialWarnings bool

	Uploads        []v1alpha1.Upload
	Keep           bool
	Detach         bool
	Forward        *ForwardSpec
	Output         string // table|json|yaml
	Editor         string
	Replace        bool
	ReplaceTimeout time.Duration

	// AwaitMainProcessAttachment tells the gateway to wait for the client to
	// attach before the sandbox starts executing its main process. Set when
	// the CLI will attach immediately after creation via ConnectMainProcess.
	AwaitMainProcessAttachment bool
	// RetentionEphemeral marks the sandbox for automatic deletion when its
	// main process exits. Set when --no-keep is in effect so the gateway
	// cleans up instead of requiring an explicit delete call.
	RetentionEphemeral bool
}

// CreateFlags is the flag-side create input (before merging with a manifest).
// Slices/maps are empty when the flag was not given.
type CreateFlags struct {
	Workspace, Name      string
	Labels               map[string]string
	Image                string
	Command              []string
	TTY                  *bool
	Env                  map[string]string
	Providers            []string
	AutoProviders        *bool
	CPU, Memory          string
	GPU                  *v1alpha1.GPU
	DriverConfig         map[string]any
	Policy               *types.SandboxPolicy
	ApprovalMode         string
	NoCredentialWarnings bool

	Uploads        []v1alpha1.Upload
	Keep           *bool // nil = default keep(true); false = --no-keep
	Detach         bool
	Forward        *ForwardSpec
	Output         string
	Editor         string
	Replace        bool          // --replace (no "unset" value, same asymmetry as Detach)
	ReplaceTimeout time.Duration // 0 = not given; a real default is applied in MergeManifestAndFlags
}

// MergeManifestAndFlags merges a manifest (nil ok) with flags: a flag wins per
// field; maps merge (flag key wins); lists replace when the flag is non-empty.
func MergeManifestAndFlags(m *v1alpha1.Sandbox, f CreateFlags) (*CreateRequest, error) {
	r := &CreateRequest{}

	// Start from the manifest, then let flags override.
	if m != nil {
		r.Workspace = m.Metadata.Workspace
		r.Name = m.Metadata.Name
		r.Labels = copyMap(m.Metadata.Labels)
		r.Image = m.Spec.Image
		r.Command = m.Spec.Command
		r.TTY = m.Spec.TTY
		r.Env = copyMap(m.Spec.Env)
		for _, p := range m.Spec.ProviderRefs {
			r.Providers = append(r.Providers, p.Name)
		}
		r.AutoProviders = m.Spec.AutoProviders
		if m.Spec.Resources != nil {
			r.CPU = m.Spec.Resources.CPU
			r.Memory = m.Spec.Resources.Memory
			r.GPU = m.Spec.Resources.GPU
		}
		r.DriverConfig = m.Spec.DriverConfig
		r.NoCredentialWarnings = m.Spec.NoCredentialWarnings
		r.Uploads = m.Spec.Upload

		//nolint:staticcheck // intentional backward-compat reads of deprecated fields
		r.ApprovalMode = m.Spec.ApprovalMode
		if m.Spec.Keep != nil { //nolint:staticcheck
			r.Keep = *m.Spec.Keep //nolint:staticcheck
		} else {
			r.Keep = true
		}
		r.Detach = m.Spec.Detach //nolint:staticcheck

		if m.Spec.Forward != "" { //nolint:staticcheck
			spec, err := ParseForwardSpec(m.Spec.Forward) //nolint:staticcheck
			if err != nil {
				return nil, fmt.Errorf("spec.forward: %w", err)
			}
			r.Forward = &spec
		}

		if so := m.Spec.SessionOpts; so != nil {
			if so.NoKeep {
				r.Keep = false
			}
			if so.Detach {
				r.Detach = true
			}
			if so.ApprovalMode != "" {
				r.ApprovalMode = so.ApprovalMode
			}
			if so.Output != "" {
				r.Output = so.Output
			}
			if so.Forward != "" {
				spec, err := ParseForwardSpec(so.Forward)
				if err != nil {
					return nil, fmt.Errorf("spec.sessionOpts.forward: %w", err)
				}
				r.Forward = &spec
			}
			if so.Replace {
				r.Replace = true
			}
			if so.ReplaceTimeout != "" {
				d, err := time.ParseDuration(so.ReplaceTimeout)
				if err != nil {
					return nil, fmt.Errorf("spec.sessionOpts.replaceTimeout: %w", err)
				}
				r.ReplaceTimeout = d
			}
		}
	} else {
		r.Keep = true
	}

	// Flag overrides.
	if f.Workspace != "" {
		r.Workspace = f.Workspace
	}
	if r.Workspace == "" {
		r.Workspace = "default"
	}
	if f.Name != "" {
		r.Name = f.Name
	}
	r.Labels = mergeMap(r.Labels, f.Labels)
	if f.Image != "" {
		r.Image = f.Image
	}
	if len(f.Command) > 0 {
		r.Command = f.Command
	}
	if f.TTY != nil {
		r.TTY = f.TTY
	}
	r.Env = mergeMap(r.Env, f.Env)
	if len(f.Providers) > 0 {
		r.Providers = f.Providers
	}
	if f.AutoProviders != nil {
		r.AutoProviders = f.AutoProviders
	}
	if f.CPU != "" {
		r.CPU = f.CPU
	}
	if f.Memory != "" {
		r.Memory = f.Memory
	}
	if f.GPU != nil {
		r.GPU = f.GPU
	}
	if len(f.DriverConfig) > 0 {
		r.DriverConfig = f.DriverConfig
	}
	if f.Policy != nil {
		r.Policy = f.Policy
	}
	if f.ApprovalMode != "" {
		r.ApprovalMode = f.ApprovalMode
	}
	if f.NoCredentialWarnings {
		r.NoCredentialWarnings = true
	}
	if len(f.Uploads) > 0 {
		r.Uploads = f.Uploads
	}
	if f.Keep != nil {
		r.Keep = *f.Keep
	}
	if f.Detach {
		r.Detach = true
	}
	if f.Forward != nil {
		r.Forward = f.Forward
	}
	if f.Output != "" {
		r.Output = f.Output
	}
	if r.Output == "" {
		r.Output = "table"
	}
	if f.Editor != "" {
		r.Editor = f.Editor
	}
	if f.Replace {
		r.Replace = true
	}
	if f.ReplaceTimeout != 0 {
		r.ReplaceTimeout = f.ReplaceTimeout
	}
	if r.Replace && r.ReplaceTimeout == 0 {
		// Matches `sandbox delete --wait-timeout`'s own default.
		r.ReplaceTimeout = 5 * time.Minute
	}

	return r, nil
}

func copyMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// mergeMap merges override into base (override key wins). Returns a new map.
func mergeMap(base, override map[string]string) map[string]string {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	out := make(map[string]string)
	for k, v := range base {
		out[k] = v
	}
	for k, v := range override {
		out[k] = v
	}
	return out
}
