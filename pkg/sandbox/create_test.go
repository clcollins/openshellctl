package sandbox

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	types "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"
	dm "github.com/NVIDIA/OpenShell/sdk/go/proto/datamodelv1"
	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"
	"go.uber.org/mock/gomock"

	"github.com/openshift-online/openshellctl/pkg/api/v1alpha1"
	"github.com/openshift-online/openshellctl/pkg/gateway"
	"github.com/openshift-online/openshellctl/pkg/gateway/mock"
)

func TestCreate_NoProviders(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gw.EXPECT().CreateSandbox(gomock.Any(), "default", "sb", gomock.Any(), gomock.Any()).
		Return(&types.Sandbox{ID: "id-1", Name: "sb"}, nil)

	req := &CreateRequest{Workspace: "default", Name: "sb", Image: "img"}
	res, err := Create(context.Background(), CreateDeps{GW: gw}, req, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Sandbox.Name != "sb" {
		t.Errorf("sandbox = %+v", res.Sandbox)
	}
}

func TestCreate_ProviderResolvesExisting(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gw.EXPECT().ListProviders(gomock.Any(), "default", gomock.Any()).
		Return([]*types.Provider{{Name: "my-openai", Type: "openai"}}, nil)
	gw.EXPECT().CreateSandbox(gomock.Any(), "default", "sb", gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _, _ string, spec *types.SandboxSpec, _ map[string]string) (*types.Sandbox, error) {
			if len(spec.Providers) != 1 || spec.Providers[0] != "my-openai" {
				t.Errorf("spec.Providers = %v, want [my-openai]", spec.Providers)
			}
			return &types.Sandbox{Name: "sb"}, nil
		})

	req := &CreateRequest{Workspace: "default", Name: "sb", Providers: []string{"my-openai"}}
	if _, err := Create(context.Background(), CreateDeps{GW: gw}, req, false); err != nil {
		t.Fatal(err)
	}
}

func TestCreate_MissingProviderTypeErrors(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gw.EXPECT().ListProviders(gomock.Any(), "default", gomock.Any()).Return(nil, nil)

	req := &CreateRequest{Workspace: "default", Name: "sb", Providers: []string{"anthropic"}}
	_, err := Create(context.Background(), CreateDeps{GW: gw}, req, false)
	var e *ErrAutoProviderUnsupported
	if !errors.As(err, &e) {
		t.Fatalf("err = %v, want ErrAutoProviderUnsupported", err)
	}
}

func TestCreate_MissingProviderSkippedWithNoAutoProviders(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gw.EXPECT().ListProviders(gomock.Any(), "default", gomock.Any()).Return(nil, nil)
	gw.EXPECT().CreateSandbox(gomock.Any(), "default", "sb", gomock.Any(), gomock.Any()).
		Return(&types.Sandbox{Name: "sb"}, nil)

	no := false
	req := &CreateRequest{Workspace: "default", Name: "sb", Providers: []string{"anthropic"}, AutoProviders: &no}
	if _, err := Create(context.Background(), CreateDeps{GW: gw}, req, false); err != nil {
		t.Fatalf("skip path should succeed: %v", err)
	}
}

func TestErrorMessages_Sandbox(t *testing.T) {
	if got := (&ErrLocalBuildUnsupported{}).Error(); !strings.Contains(got, "local image builds are not supported") {
		t.Errorf("ErrLocalBuildUnsupported = %q", got)
	}
	if got := (&ErrNoDockerfile{}).Error(); got != "No Dockerfile found" {
		t.Errorf("ErrNoDockerfile = %q", got)
	}
	if got := (&ErrLocalPathMissing{Path: "/x"}).Error(); !strings.Contains(got, "/x") {
		t.Errorf("ErrLocalPathMissing = %q", got)
	}
	if got := (&ErrProviderNotFound{Name: "z"}).Error(); !strings.Contains(got, "z") {
		t.Errorf("ErrProviderNotFound = %q", got)
	}
	if got := (&ErrAutoProviderUnsupported{Type: "anthropic"}).Error(); !strings.Contains(got, "openshellctl cannot auto-create") {
		t.Errorf("ErrAutoProviderUnsupported = %q", got)
	}
	if got := SkipProviderMessage("anthropic"); !strings.Contains(got, "Skipping provider 'anthropic'") {
		t.Errorf("SkipProviderMessage = %q", got)
	}
	if got := (&ErrLifecycle{Target: "Ready"}).Error(); !strings.Contains(got, "Ready") {
		t.Errorf("ErrLifecycle = %q", got)
	}
	if got := (&ErrDeleteTimeout{Name: "sb", After: 0}).Error(); !strings.Contains(got, "sb") {
		t.Errorf("ErrDeleteTimeout = %q", got)
	}
}

func TestCoerceJSON(t *testing.T) {
	in := map[string]any{"driver": map[string]any{"count": 5}}
	out := coerceJSON(in)
	// After JSON round-trip, integers become float64.
	inner := out["driver"].(map[string]any)
	if _, ok := inner["count"].(float64); !ok {
		t.Errorf("count not coerced to float64: %T", inner["count"])
	}
}

func TestToSDKSpec_DriverConfigCoerced(t *testing.T) {
	req := &CreateRequest{DriverConfig: map[string]any{"d": map[string]any{"n": 1}}}
	spec := ToSDKSpec(req, false)
	if spec.Template == nil || spec.Template.DriverConfig == nil {
		t.Fatal("driver config should populate template")
	}
}

func TestCreate_CredentialWarnings(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gw.EXPECT().CreateSandbox(gomock.Any(), "default", "sb", gomock.Any(), gomock.Any()).
		Return(&types.Sandbox{Name: "sb"}, nil)

	var stderr strings.Builder
	req := &CreateRequest{Workspace: "default", Name: "sb", Env: map[string]string{"MY_SECRET_TOKEN": "x"}}
	if _, err := Create(context.Background(), CreateDeps{GW: gw, Stderr: &stderr}, req, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "MY_SECRET_TOKEN looks like a credential") {
		t.Errorf("expected credential warning, got: %q", stderr.String())
	}
}

func TestCreate_BareGPURawPath(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gw.EXPECT().CreateSandboxRaw(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, req *pb.CreateSandboxRequest) (*pb.Sandbox, error) {
			if req.Spec.ResourceRequirements == nil || req.Spec.ResourceRequirements.Gpu == nil {
				t.Errorf("raw request missing GPU resource requirement: %+v", req.Spec)
			}
			if req.Spec.ResourceRequirements.Gpu.Count != nil {
				t.Error("bare --gpu should send Count=nil")
			}
			return &pb.Sandbox{Metadata: &dm.ObjectMeta{Name: "sb"}, Status: &pb.SandboxStatus{Phase: pb.SandboxPhase_SANDBOX_PHASE_PROVISIONING}}, nil
		})

	req := &CreateRequest{Workspace: "default", Name: "sb", GPU: &v1alpha1.GPU{}}
	res, err := Create(context.Background(), CreateDeps{GW: gw}, req, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Sandbox.Name != "sb" {
		t.Errorf("sandbox = %+v", res.Sandbox)
	}
}

func TestCreate_BareGPUWithPolicyRejected(t *testing.T) {
	ctrl := gomock.NewController(t)
	req := &CreateRequest{
		Workspace: "default", Name: "sb",
		GPU:    &v1alpha1.GPU{},
		Policy: &types.SandboxPolicy{Version: 1},
	}
	_, err := Create(context.Background(), CreateDeps{GW: mock.NewMockGateway(ctrl)}, req, false)
	if !errors.Is(err, ErrRawGPUWithPolicy) {
		t.Fatalf("err = %v, want ErrRawGPUWithPolicy", err)
	}
}

func TestCreate_PolicyFlowsToGateway(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	pol := &types.SandboxPolicy{
		Version:    1,
		Filesystem: &types.FilesystemPolicy{IncludeWorkdir: true, ReadOnly: []string{"/etc"}},
	}
	gw.EXPECT().CreateSandbox(gomock.Any(), "default", "sb", gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _, _ string, spec *types.SandboxSpec, _ map[string]string) (*types.Sandbox, error) {
			if spec.Policy == nil {
				t.Fatal("spec.Policy is nil — policy was not propagated to gateway")
			}
			if spec.Policy.Version != 1 {
				t.Errorf("spec.Policy.Version = %d, want 1", spec.Policy.Version)
			}
			if spec.Policy.Filesystem == nil || !spec.Policy.Filesystem.IncludeWorkdir {
				t.Error("spec.Policy.Filesystem not propagated")
			}
			if len(spec.Policy.Filesystem.ReadOnly) != 1 || spec.Policy.Filesystem.ReadOnly[0] != "/etc" {
				t.Errorf("spec.Policy.Filesystem.ReadOnly = %v", spec.Policy.Filesystem.ReadOnly)
			}
			return &types.Sandbox{Name: "sb"}, nil
		})

	req := &CreateRequest{Workspace: "default", Name: "sb", Policy: pol}
	res, err := Create(context.Background(), CreateDeps{GW: gw}, req, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Sandbox.Name != "sb" {
		t.Errorf("sandbox = %+v", res.Sandbox)
	}
}

func TestCreate_PolicyWithExplicitGPUCount(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	pol := &types.SandboxPolicy{Version: 1}
	cnt := uint32(2)
	gw.EXPECT().CreateSandbox(gomock.Any(), "default", "sb", gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _, _ string, spec *types.SandboxSpec, _ map[string]string) (*types.Sandbox, error) {
			if spec.Policy == nil {
				t.Fatal("policy should be present with explicit GPU count")
			}
			if spec.GPUCount == nil || *spec.GPUCount != 2 {
				t.Errorf("GPUCount = %v", spec.GPUCount)
			}
			return &types.Sandbox{Name: "sb"}, nil
		})

	req := &CreateRequest{
		Workspace: "default", Name: "sb",
		Policy: pol,
		GPU:    &v1alpha1.GPU{Count: &cnt},
	}
	if _, err := Create(context.Background(), CreateDeps{GW: gw}, req, false); err != nil {
		t.Fatal(err)
	}
}

func TestMerge_GPUFromManifest(t *testing.T) {
	cnt := uint32(4)
	m := &v1alpha1.Sandbox{Spec: v1alpha1.SandboxSpec{Resources: &v1alpha1.Resources{GPU: &v1alpha1.GPU{Count: &cnt}}}}
	r, _ := MergeManifestAndFlags(m, CreateFlags{})
	if r.GPU == nil || r.GPU.Count == nil || *r.GPU.Count != 4 {
		t.Errorf("GPU not merged from manifest: %+v", r.GPU)
	}
}

// TestCreate_Replace is the ticket's explicit "ordered-expectation test"
// (Get, Delete, Get NotFound, Create): with Replace set, Create must check
// for an existing sandbox, delete it, confirm it's gone, and only then
// create the new one — in that exact order.
func TestCreate_AwaitMainProcessAttachment_SetsRawField(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gw.EXPECT().CreateSandboxRaw(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, req *pb.CreateSandboxRequest) (*pb.Sandbox, error) {
			if !req.AwaitMainProcessAttachment {
				t.Error("AwaitMainProcessAttachment should be true")
			}
			// RetentionEphemeral was false, so no retention annotation.
			if got := req.Annotations["openshell.nvidia.com/retention"]; got != "" {
				t.Errorf("retention annotation = %q, want empty when RetentionEphemeral=false", got)
			}
			// GPU was not requested, so no GPU resource requirements.
			if req.Spec.ResourceRequirements != nil {
				t.Errorf("unexpected GPU resource requirements: %+v", req.Spec.ResourceRequirements)
			}
			return &pb.Sandbox{Metadata: &dm.ObjectMeta{Name: "sb"}, Status: &pb.SandboxStatus{Phase: pb.SandboxPhase_SANDBOX_PHASE_PROVISIONING}}, nil
		})

	r := &CreateRequest{Workspace: "default", Name: "sb", AwaitMainProcessAttachment: true}
	if _, err := Create(context.Background(), CreateDeps{GW: gw}, r, false); err != nil {
		t.Fatal(err)
	}
}

func TestCreate_RetentionEphemeral_SetsAnnotation(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gw.EXPECT().CreateSandboxRaw(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, req *pb.CreateSandboxRequest) (*pb.Sandbox, error) {
			if got := req.Annotations["openshell.nvidia.com/retention"]; got != "ephemeral" {
				t.Errorf("retention annotation = %q, want %q", got, "ephemeral")
			}
			// AwaitMainProcessAttachment was false.
			if req.AwaitMainProcessAttachment {
				t.Error("AwaitMainProcessAttachment should be false when only RetentionEphemeral is set")
			}
			// GPU was not requested, so no GPU resource requirements.
			if req.Spec.ResourceRequirements != nil {
				t.Errorf("unexpected GPU resource requirements: %+v", req.Spec.ResourceRequirements)
			}
			return &pb.Sandbox{Metadata: &dm.ObjectMeta{Name: "sb"}, Status: &pb.SandboxStatus{Phase: pb.SandboxPhase_SANDBOX_PHASE_PROVISIONING}}, nil
		})

	r := &CreateRequest{Workspace: "default", Name: "sb", RetentionEphemeral: true}
	if _, err := Create(context.Background(), CreateDeps{GW: gw}, r, false); err != nil {
		t.Fatal(err)
	}
}

func TestCreate_AwaitAndEphemeral_RoutesThroughRaw(t *testing.T) {
	// Both AwaitMainProcessAttachment and RetentionEphemeral set without GPU
	// must still route through the raw path.
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gw.EXPECT().CreateSandboxRaw(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, req *pb.CreateSandboxRequest) (*pb.Sandbox, error) {
			if !req.AwaitMainProcessAttachment {
				t.Error("AwaitMainProcessAttachment should be true")
			}
			if got := req.Annotations["openshell.nvidia.com/retention"]; got != "ephemeral" {
				t.Errorf("retention annotation = %q, want %q", got, "ephemeral")
			}
			if req.Spec.ResourceRequirements != nil {
				t.Errorf("unexpected GPU resource requirements: %+v", req.Spec.ResourceRequirements)
			}
			return &pb.Sandbox{Metadata: &dm.ObjectMeta{Name: "sb"}, Status: &pb.SandboxStatus{Phase: pb.SandboxPhase_SANDBOX_PHASE_PROVISIONING}}, nil
		})

	r := &CreateRequest{Workspace: "default", Name: "sb", AwaitMainProcessAttachment: true, RetentionEphemeral: true}
	if _, err := Create(context.Background(), CreateDeps{GW: gw}, r, false); err != nil {
		t.Fatal(err)
	}
}

func TestCreate_BareGPU_NoAwaitOrEphemeralFields(t *testing.T) {
	// Bare GPU (UsesRawGPU) must not set AwaitMainProcessAttachment or retention
	// annotations when those fields are not in the request.
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gw.EXPECT().CreateSandboxRaw(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, req *pb.CreateSandboxRequest) (*pb.Sandbox, error) {
			if req.AwaitMainProcessAttachment {
				t.Error("bare GPU should not set AwaitMainProcessAttachment")
			}
			if got := req.Annotations["openshell.nvidia.com/retention"]; got != "" {
				t.Errorf("bare GPU should not set retention annotation, got %q", got)
			}
			if req.Spec.ResourceRequirements == nil || req.Spec.ResourceRequirements.Gpu == nil {
				t.Error("bare GPU should have GPU resource requirements")
			}
			return &pb.Sandbox{Metadata: &dm.ObjectMeta{Name: "sb"}, Status: &pb.SandboxStatus{Phase: pb.SandboxPhase_SANDBOX_PHASE_PROVISIONING}}, nil
		})

	r := &CreateRequest{Workspace: "default", Name: "sb", GPU: &v1alpha1.GPU{}}
	if _, err := Create(context.Background(), CreateDeps{GW: gw}, r, false); err != nil {
		t.Fatal(err)
	}
}

func TestCreate_Replace(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gomock.InOrder(
		gw.EXPECT().GetSandbox(gomock.Any(), "default", "sb").Return(&types.Sandbox{Name: "sb"}, nil),
		gw.EXPECT().DeleteSandbox(gomock.Any(), "default", "sb").Return(true, nil),
		gw.EXPECT().GetSandbox(gomock.Any(), "default", "sb").
			Return((*types.Sandbox)(nil), &gateway.NotFoundError{Resource: "sandbox", Name: "sb"}),
		gw.EXPECT().CreateSandbox(gomock.Any(), "default", "sb", gomock.Any(), gomock.Any()).
			Return(&types.Sandbox{ID: "id-2", Name: "sb"}, nil),
	)

	req := &CreateRequest{Workspace: "default", Name: "sb", Image: "img", Replace: true, ReplaceTimeout: time.Minute}
	var stderr bytes.Buffer
	res, err := Create(context.Background(), CreateDeps{GW: gw, Stderr: &stderr}, req, false)
	if err != nil {
		t.Fatalf("Create --replace: %v", err)
	}
	if res.Sandbox.ID != "id-2" {
		t.Errorf("sandbox = %+v, want the freshly-created one", res.Sandbox)
	}
	if !strings.Contains(stderr.String(), "Replacing existing sandbox sb") {
		t.Errorf("stderr = %q, want a replacing-sandbox progress message", stderr.String())
	}
}

// TestCreate_ReplaceNotFound_SkipsDeleteGoesStraightToCreate confirms
// Replace with nothing to replace against doesn't attempt a Delete at all.
func TestCreate_ReplaceNotFound_SkipsDeleteGoesStraightToCreate(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gw.EXPECT().GetSandbox(gomock.Any(), "default", "sb").
		Return((*types.Sandbox)(nil), &gateway.NotFoundError{Resource: "sandbox", Name: "sb"})
	gw.EXPECT().CreateSandbox(gomock.Any(), "default", "sb", gomock.Any(), gomock.Any()).
		Return(&types.Sandbox{ID: "id-1", Name: "sb"}, nil)

	req := &CreateRequest{Workspace: "default", Name: "sb", Image: "img", Replace: true, ReplaceTimeout: time.Minute}
	if _, err := Create(context.Background(), CreateDeps{GW: gw}, req, false); err != nil {
		t.Fatalf("Create --replace (nothing to replace): %v", err)
	}
}

// TestCreate_ReplaceDeleteRPCError_AbortsBeforeCreate is the ticket's second
// acceptance criterion end to end through Create: a delete RPC failure
// during replace must abort — Create is never called, and the real error
// propagates (not a fake success).
func TestCreate_ReplaceDeleteRPCError_AbortsBeforeCreate(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	wantErr := &gateway.UnauthenticatedError{Message: "token expired"}
	gomock.InOrder(
		gw.EXPECT().GetSandbox(gomock.Any(), "default", "sb").Return(&types.Sandbox{Name: "sb"}, nil),
		gw.EXPECT().DeleteSandbox(gomock.Any(), "default", "sb").Return(false, wantErr),
	)
	// No CreateSandbox expectation — gomock fails the test if it's called.

	req := &CreateRequest{Workspace: "default", Name: "sb", Image: "img", Replace: true, ReplaceTimeout: time.Minute}
	_, err := Create(context.Background(), CreateDeps{GW: gw}, req, false)
	if err != wantErr { //nolint:errorlint // direct identity: Create must surface DeleteSandbox's error unwrapped
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}
