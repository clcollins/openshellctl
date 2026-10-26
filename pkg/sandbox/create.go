package sandbox

import (
	"context"
	"errors"
	"io"
	"time"

	types "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"
	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/openshift-online/openshellctl/pkg/gateway"
)

// ErrRawGPUWithPolicy is returned when bare --gpu (raw create path) is combined
// with a policy. The raw path sends a proto CreateSandboxRequest which cannot
// carry the SDK SandboxPolicy; use an explicit --gpu N (SDK path) instead.
var ErrRawGPUWithPolicy = errors.New("bare --gpu combined with a policy is not yet supported; use --gpu <count> with a policy")

// ErrRawAttachWithPolicy is returned when AwaitMainProcessAttachment or
// RetentionEphemeral forces the raw create path while a policy is also set.
// The proto CreateSandboxRequest has no policy field; drop the policy or use
// the SDK path (no attach/ephemeral flags).
var ErrRawAttachWithPolicy = errors.New("--policy cannot be combined with await-main-process-attachment or ephemeral retention; the proto create path does not carry SDK policies")

// CreateDeps are the dependencies for Create.
type CreateDeps struct {
	GW     gateway.Gateway
	Env    func(string) string
	Stderr io.Writer    // credential warnings / skip messages (nil → discard)
	Sink   ProgressSink // provisioning progress (nil → nop)
	Clock  Clock

	// Watch controls whether Create waits for the sandbox to reach Ready.
	Watch       bool
	IdleTimeout time.Duration
}

// CreateResult is the outcome of Create.
type CreateResult struct {
	Sandbox *types.Sandbox
}

// Create resolves providers, prints credential warnings, builds the spec, and
// creates the sandbox (SDK, or the raw path for bare --gpu). When Watch is set
// it then waits for Ready via WatchUntilReady. This is the create-without-
// transfer path (no upload/forward/attach); the CLI decides output/attach on the
// returned sandbox. Steps mirror run.rs (§5.5).
func Create(ctx context.Context, d CreateDeps, r *CreateRequest, ttyResolved bool) (*CreateResult, error) {
	if r.Workspace == "" {
		return nil, ErrEmptyWorkspace
	}
	// (0) --replace: delete and wait for any existing same-named sandbox
	// first, so the create below never races a deletion still in flight.
	if r.Replace {
		if err := replaceExisting(ctx, d.GW, r.Workspace, r.Name, r.ReplaceTimeout, d.Clock, d.Stderr); err != nil {
			return nil, err
		}
	}

	// (1) resolve providers (explicit only; command-based inference removed in MCS-1410).
	if err := resolveRequestProviders(ctx, d.GW, r, nil); err != nil {
		return nil, err
	}

	// (3) credential warnings.
	if !r.NoCredentialWarnings && d.Stderr != nil {
		for _, w := range CredentialLikeKeys(r.Env) {
			_, _ = io.WriteString(d.Stderr, FormatCredentialWarning(w))
		}
	}

	// (4) create (SDK, or raw for bare --gpu).
	sb, err := createSandbox(ctx, d.GW, r, ttyResolved)
	if err != nil {
		return nil, err
	}

	// (7) watch to Ready when requested.
	if d.Watch {
		sink := d.Sink
		if sink == nil {
			sink = nopSink{}
		}
		sink.Header(sb.Name)
		if _, werr := WatchUntilReady(ctx, d.GW, r.Workspace, sb.Name, phaseFromStatus(sb), d.IdleTimeout, r.GPU != nil, sink, d.Clock); werr != nil {
			return nil, werr
		}
		if fresh, gerr := d.GW.GetSandbox(ctx, r.Workspace, sb.Name); gerr == nil {
			sb = fresh
		}
	}

	return &CreateResult{Sandbox: sb}, nil
}

// createSandbox dispatches to the SDK or raw create path. The raw path is used
// for bare --gpu (GPU resource requirements), AwaitMainProcessAttachment, and
// RetentionEphemeral — all three require proto fields that the SDK create path
// does not expose.
func createSandbox(ctx context.Context, gw gateway.Gateway, r *CreateRequest, ttyResolved bool) (*types.Sandbox, error) {
	if r.UsesRawGPU() || r.AwaitMainProcessAttachment || r.RetentionEphemeral {
		return createSandboxRaw(ctx, gw, r, ttyResolved)
	}
	spec := ToSDKSpec(r, ttyResolved)
	return gw.CreateSandbox(ctx, r.Workspace, r.Name, spec, r.Labels)
}

// createSandboxRaw builds a raw CreateSandboxRequest for the bare --gpu case
// (ResourceRequirements{Gpu:{Count:nil}}), then converts the returned proto to
// a types.Sandbox shell carrying id/name/phase for the caller.
func createSandboxRaw(ctx context.Context, gw gateway.Gateway, r *CreateRequest, ttyResolved bool) (*types.Sandbox, error) {
	rawSpec := &pb.SandboxSpec{
		Environment: r.Env,
		Providers:   r.Providers,
		Command:     commandOrDefault(r.Command),
		Tty:         ttyResolved,
	}
	// GPU requirements only apply when the caller explicitly requested bare GPU;
	// AwaitMainProcessAttachment and RetentionEphemeral also reach this path but
	// must not inject a GPU request the user never asked for.
	if r.UsesRawGPU() {
		rawSpec.ResourceRequirements = &pb.ResourceRequirements{Gpu: &pb.GpuResourceRequirements{Count: nil}}
	}
	if tmpl := rawTemplate(r); tmpl != nil {
		rawSpec.Template = tmpl
	}
	if r.Policy != nil {
		if r.UsesRawGPU() {
			return nil, ErrRawGPUWithPolicy
		}
		return nil, ErrRawAttachWithPolicy
	}

	req := &pb.CreateSandboxRequest{
		Spec:                       rawSpec,
		Name:                       r.Name,
		Labels:                     r.Labels,
		WorkspaceScope:             workspaceScope(r.Workspace),
		AwaitMainProcessAttachment: r.AwaitMainProcessAttachment,
	}
	if r.RetentionEphemeral {
		req.Annotations = map[string]string{"openshell.nvidia.com/retention": "ephemeral"}
	}
	sb, err := gw.CreateSandboxRaw(ctx, req)
	if err != nil {
		return nil, err
	}
	return sandboxFromProto(sb), nil
}

func rawTemplate(r *CreateRequest) *pb.SandboxTemplate {
	if r.Image == "" && r.CPU == "" && r.Memory == "" && len(r.DriverConfig) == 0 {
		return nil
	}
	tmpl := &pb.SandboxTemplate{Image: r.Image}
	if res := ResourcesStruct(r.CPU, r.Memory); res != nil {
		if s, err := structpb.NewStruct(res); err == nil {
			tmpl.Resources = s
		}
	}
	if len(r.DriverConfig) > 0 {
		if s, err := structpb.NewStruct(coerceJSON(r.DriverConfig)); err == nil {
			tmpl.DriverConfig = s
		}
	}
	return tmpl
}

func commandOrDefault(cmd []string) []string {
	if len(cmd) == 0 {
		return append([]string(nil), DefaultCommand...)
	}
	return cmd
}

// sandboxFromProto builds a minimal types.Sandbox from the raw create response.
func sandboxFromProto(sb *pb.Sandbox) *types.Sandbox {
	out := &types.Sandbox{}
	if md := sb.GetMetadata(); md != nil {
		out.ID = md.GetId()
		out.Name = md.GetName()
		out.Workspace = md.GetWorkspace()
		out.Labels = md.GetLabels()
	}
	out.Status.Phase = phaseName(sb.GetStatus().GetPhase())
	return out
}

// phaseFromStatus maps a types.Sandbox phase back to the proto enum for the watch.
func phaseFromStatus(sb *types.Sandbox) pb.SandboxPhase {
	switch sb.Status.Phase {
	case types.SandboxProvisioning:
		return pb.SandboxPhase_SANDBOX_PHASE_PROVISIONING
	case types.SandboxReady:
		return pb.SandboxPhase_SANDBOX_PHASE_READY
	case types.SandboxError:
		return pb.SandboxPhase_SANDBOX_PHASE_ERROR
	case types.SandboxStopped:
		return pb.SandboxPhase_SANDBOX_PHASE_STOPPED
	case types.SandboxCompleted:
		return pb.SandboxPhase_SANDBOX_PHASE_COMPLETED
	default:
		return pb.SandboxPhase_SANDBOX_PHASE_UNSPECIFIED
	}
}

// phaseName maps a proto phase enum to the SDK string phase.
func phaseName(p pb.SandboxPhase) types.SandboxPhase {
	switch p {
	case pb.SandboxPhase_SANDBOX_PHASE_PROVISIONING:
		return types.SandboxProvisioning
	case pb.SandboxPhase_SANDBOX_PHASE_READY:
		return types.SandboxReady
	case pb.SandboxPhase_SANDBOX_PHASE_ERROR:
		return types.SandboxError
	case pb.SandboxPhase_SANDBOX_PHASE_DELETING:
		return types.SandboxDeleting
	case pb.SandboxPhase_SANDBOX_PHASE_STOPPING:
		return types.SandboxStopping
	case pb.SandboxPhase_SANDBOX_PHASE_STOPPED:
		return types.SandboxStopped
	case pb.SandboxPhase_SANDBOX_PHASE_STARTING:
		return types.SandboxStarting
	case pb.SandboxPhase_SANDBOX_PHASE_COMPLETED:
		return types.SandboxCompleted
	default:
		return types.SandboxUnknown
	}
}

// resolveRequestProviders resolves explicit + inferred providers against the
// gateway; recognised-but-missing types → ErrAutoProviderUnsupported unless
// --no-auto-providers, in which case they are skipped (message to stderr).
func resolveRequestProviders(ctx context.Context, gw gateway.Gateway, r *CreateRequest, inferred []string) error {
	if len(r.Providers) == 0 && len(inferred) == 0 {
		return nil
	}
	known, err := ListAllProviders(ctx, gw, r.Workspace)
	if err != nil {
		return err
	}
	res, err := ResolveProviders(known, r.Providers, inferred)
	if err != nil {
		return err
	}
	if len(res.MissingTypes) > 0 {
		if r.AutoProviders != nil && !*r.AutoProviders {
			r.Providers = res.Names
			return nil
		}
		return &ErrAutoProviderUnsupported{Type: res.MissingTypes[0]}
	}
	r.Providers = res.Names
	return nil
}

// ListAllProviders fetches all providers in a workspace. The gateway's
// ListProviders already calls the SDK's ListAll, which handles pagination
// internally. Exported for reuse by `doctor`'s provider check
// (internal/cli/doctor.go).
func ListAllProviders(ctx context.Context, gw gateway.Gateway, workspace string) ([]*types.Provider, error) {
	return gw.ListProviders(ctx, workspace, types.ListOptions{})
}
