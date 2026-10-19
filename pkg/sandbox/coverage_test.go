package sandbox

import (
	"context"
	"testing"
	"time"

	types "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"
	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"
	"go.uber.org/mock/gomock"

	"github.com/openshift-online/openshellctl/pkg/gateway"
	"github.com/openshift-online/openshellctl/pkg/gateway/mock"
)

func TestCreate_WithWatch(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gw.EXPECT().CreateSandbox(gomock.Any(), "default", "sb", gomock.Any(), gomock.Any()).
		Return(&types.Sandbox{ID: "id-1", Name: "sb", Status: types.SandboxStatus{Phase: types.SandboxProvisioning}}, nil)
	gw.EXPECT().WatchSandbox(gomock.Any(), gomock.Any()).Return(&scriptedStream{events: []*pb.SandboxStreamEvent{
		phaseEvent(pb.SandboxPhase_SANDBOX_PHASE_PROVISIONING),
		phaseEvent(pb.SandboxPhase_SANDBOX_PHASE_READY),
	}}, nil)
	gw.EXPECT().GetSandbox(gomock.Any(), "default", "sb").
		Return(&types.Sandbox{Name: "sb", Status: types.SandboxStatus{Phase: types.SandboxReady}}, nil)

	req := &CreateRequest{Workspace: "default", Name: "sb", Image: "img"}
	res, err := Create(context.Background(), CreateDeps{GW: gw, Watch: true, IdleTimeout: time.Minute}, req, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Sandbox.Status.Phase != types.SandboxReady {
		t.Errorf("phase = %v, want Ready", res.Sandbox.Status.Phase)
	}
}

func TestPhaseMappingRoundTrip(t *testing.T) {
	cases := []struct {
		proto pb.SandboxPhase
		str   types.SandboxPhase
	}{
		{pb.SandboxPhase_SANDBOX_PHASE_PROVISIONING, types.SandboxProvisioning},
		{pb.SandboxPhase_SANDBOX_PHASE_READY, types.SandboxReady},
		{pb.SandboxPhase_SANDBOX_PHASE_ERROR, types.SandboxError},
		{pb.SandboxPhase_SANDBOX_PHASE_DELETING, types.SandboxDeleting},
		{pb.SandboxPhase_SANDBOX_PHASE_STOPPING, types.SandboxStopping},
		{pb.SandboxPhase_SANDBOX_PHASE_STOPPED, types.SandboxStopped},
		{pb.SandboxPhase_SANDBOX_PHASE_STARTING, types.SandboxStarting},
		{pb.SandboxPhase_SANDBOX_PHASE_COMPLETED, types.SandboxCompleted},
		{pb.SandboxPhase_SANDBOX_PHASE_UNKNOWN, types.SandboxUnknown},
	}
	for _, tc := range cases {
		if got := phaseName(tc.proto); got != tc.str {
			t.Errorf("phaseName(%v) = %v, want %v", tc.proto, got, tc.str)
		}
	}
	// phaseFromStatus covers the subset used for the initial watch phase.
	if phaseFromStatus(&types.Sandbox{Status: types.SandboxStatus{Phase: types.SandboxReady}}) != pb.SandboxPhase_SANDBOX_PHASE_READY {
		t.Error("phaseFromStatus Ready mismatch")
	}
	if phaseFromStatus(&types.Sandbox{Status: types.SandboxStatus{Phase: types.SandboxError}}) != pb.SandboxPhase_SANDBOX_PHASE_ERROR {
		t.Error("phaseFromStatus Error mismatch")
	}
	if phaseFromStatus(&types.Sandbox{}) != pb.SandboxPhase_SANDBOX_PHASE_UNSPECIFIED {
		t.Error("phaseFromStatus default should be UNSPECIFIED")
	}
}

func TestNopSink(t *testing.T) {
	s := NopSink()
	s.Header("x")
	s.StepDone("x", time.Second)
	s.StepActive("x", "y")
	s.Warning("w")
	s.Error("e")
}

func TestRawTemplate(t *testing.T) {
	if rawTemplate(&CreateRequest{}) != nil {
		t.Error("empty request → nil template")
	}
	tmpl := rawTemplate(&CreateRequest{Image: "img", CPU: "2", DriverConfig: map[string]any{"d": map[string]any{"n": 1}}})
	if tmpl == nil || tmpl.Image != "img" {
		t.Fatalf("template = %+v", tmpl)
	}
	if tmpl.Resources == nil || tmpl.DriverConfig == nil {
		t.Error("resources/driverconfig should be populated")
	}
}

func TestLifecycleErrorMessages(t *testing.T) {
	if got := (&ErrLifecycleTimeout{Target: "Ready", After: 300 * time.Second}).Error(); got == "" {
		t.Error("empty timeout message")
	}
	if got := (&ErrLifecycleStreamEnded{Target: "Stopped"}).Error(); got == "" {
		t.Error("empty stream-ended message")
	}
}

func TestDelete_Wait(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gw.EXPECT().DeleteSandbox(gomock.Any(), "default", "sb").Return(true, nil)
	// First GetSandbox returns NotFound → deletion confirmed immediately.
	gw.EXPECT().GetSandbox(gomock.Any(), "default", "sb").Return(nil, &gateway.NotFoundError{Resource: "sandbox", Name: "sb"})

	err := Delete(context.Background(), gw, nil, "", DeleteRequest{
		Workspace: "default", Names: []string{"sb"}, Wait: true, WaitTimeout: time.Minute,
	}, nil)
	if err != nil {
		t.Fatalf("Delete --wait: %v", err)
	}
}

func TestResetsIdle(t *testing.T) {
	if !resetsIdle(&pb.PlatformEvent{Metadata: map[string]string{metaCompleteStep: "x"}}) {
		t.Error("complete_step should reset")
	}
	if !resetsIdle(&pb.PlatformEvent{Source: "vm"}) {
		t.Error("vm source should reset")
	}
	if resetsIdle(&pb.PlatformEvent{Source: "other"}) {
		t.Error("non-progress event should not reset")
	}
}

func TestImageHelpers(t *testing.T) {
	// Cover looksLikeLocalPath branches and joinPath trailing slash.
	if !looksLikeLocalPath("~/foo") {
		t.Error("~/ should look like a local path")
	}
	if !looksLikeLocalPath("..") {
		t.Error(".. should look like a local path")
	}
	if joinPath("dir/", "f") != "dir/f" {
		t.Error("joinPath trailing slash")
	}
}
