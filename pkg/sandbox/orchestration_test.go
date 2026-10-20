package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	types "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"
	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"
	"go.uber.org/mock/gomock"

	"github.com/openshift-online/openshellctl/pkg/gateway"
	"github.com/openshift-online/openshellctl/pkg/gateway/mock"
)

// scriptedStream is a gateway.Stream that replays a fixed set of events then EOF.
type scriptedStream struct {
	events []*pb.SandboxStreamEvent
	i      int
}

func (s *scriptedStream) Recv() (*pb.SandboxStreamEvent, error) {
	if s.i >= len(s.events) {
		return nil, io.EOF
	}
	ev := s.events[s.i]
	s.i++
	return ev, nil
}
func (s *scriptedStream) Close() {}

func phaseEvent(p pb.SandboxPhase) *pb.SandboxStreamEvent {
	return &pb.SandboxStreamEvent{Payload: &pb.SandboxStreamEvent_Sandbox{
		Sandbox: &pb.Sandbox{Status: &pb.SandboxStatus{Phase: p}},
	}}
}

// watchReqMatcher is a gomock.Matcher that asserts a *pb.WatchSandboxRequest
// carries the expected WorkspaceScope and Sandbox name.
type watchReqMatcher struct {
	workspace string
	sandbox   string
}

func (m watchReqMatcher) Matches(x interface{}) bool {
	req, ok := x.(*pb.WatchSandboxRequest)
	if !ok {
		return false
	}
	if req.WorkspaceScope == nil || req.WorkspaceScope.GetWorkspace() != m.workspace {
		return false
	}
	return req.Sandbox == m.sandbox
}

func (m watchReqMatcher) String() string {
	return fmt.Sprintf("WatchSandboxRequest{workspace=%q, sandbox=%q}", m.workspace, m.sandbox)
}

func TestDelete_ByName(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gw.EXPECT().DeleteSandbox(gomock.Any(), "default", "sb-1").Return(true, nil)

	var outcomes []DeleteOutcome
	err := Delete(context.Background(), gw, nil, "", DeleteRequest{Workspace: "default", Names: []string{"sb-1"}},
		func(o DeleteOutcome) { outcomes = append(outcomes, o) })
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 1 || !outcomes[0].Deleted {
		t.Errorf("outcomes = %+v", outcomes)
	}
}

func TestDelete_NotFoundReportsFalse(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gw.EXPECT().DeleteSandbox(gomock.Any(), "default", "ghost").Return(false, nil)

	var outcomes []DeleteOutcome
	err := Delete(context.Background(), gw, nil, "", DeleteRequest{Workspace: "default", Names: []string{"ghost"}},
		func(o DeleteOutcome) { outcomes = append(outcomes, o) })
	if err != nil {
		t.Fatal(err)
	}
	if outcomes[0].Deleted {
		t.Error("ghost should report Deleted=false")
	}
}

func TestDelete_All(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gw.EXPECT().ListSandboxes(gomock.Any(), "default", gomock.Any()).Return([]*types.Sandbox{
		{Name: "a"}, {Name: "b"},
	}, nil)
	gw.EXPECT().DeleteSandbox(gomock.Any(), "default", "a").Return(true, nil)
	gw.EXPECT().DeleteSandbox(gomock.Any(), "default", "b").Return(true, nil)

	var names []string
	err := Delete(context.Background(), gw, nil, "", DeleteRequest{Workspace: "default", All: true},
		func(o DeleteOutcome) { names = append(names, o.Name) })
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 {
		t.Errorf("deleted %v, want 2", names)
	}
}

func TestDelete_AllEmpty(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gw.EXPECT().ListSandboxes(gomock.Any(), "default", gomock.Any()).Return(nil, nil)

	err := Delete(context.Background(), gw, nil, "", DeleteRequest{Workspace: "default", All: true}, nil)
	if !errors.Is(err, ErrNothingToDelete) {
		t.Fatalf("err = %v, want ErrNothingToDelete", err)
	}
}

func TestDelete_FirstErrorAborts(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gw.EXPECT().DeleteSandbox(gomock.Any(), "default", "a").Return(false, errors.New("rpc down"))
	// "b" must not be called.

	err := Delete(context.Background(), gw, nil, "", DeleteRequest{Workspace: "default", Names: []string{"a", "b"}}, nil)
	if err == nil {
		t.Fatal("expected abort on first error")
	}
}

func TestStop_WatchesUntilStopped(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gw.EXPECT().StopSandbox(gomock.Any(), "default", "sb").Return(&types.Sandbox{ID: "id-1"}, nil)
	gw.EXPECT().WatchSandbox(gomock.Any(), watchReqMatcher{workspace: "default", sandbox: "sb"}).Return(&scriptedStream{events: []*pb.SandboxStreamEvent{
		phaseEvent(pb.SandboxPhase_SANDBOX_PHASE_STOPPING),
		phaseEvent(pb.SandboxPhase_SANDBOX_PHASE_STOPPED),
	}}, nil)

	sb, err := Stop(context.Background(), gw, "default", "sb", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if sb.GetStatus().GetPhase() != pb.SandboxPhase_SANDBOX_PHASE_STOPPED {
		t.Errorf("phase = %v", sb.GetStatus().GetPhase())
	}
}

func TestStart_WatchesUntilReady(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gw.EXPECT().StartSandbox(gomock.Any(), "default", "sb").Return(&types.Sandbox{ID: "id-1"}, nil)
	gw.EXPECT().WatchSandbox(gomock.Any(), watchReqMatcher{workspace: "default", sandbox: "sb"}).Return(&scriptedStream{events: []*pb.SandboxStreamEvent{
		phaseEvent(pb.SandboxPhase_SANDBOX_PHASE_STARTING),
		phaseEvent(pb.SandboxPhase_SANDBOX_PHASE_READY),
	}}, nil)

	sb, err := Start(context.Background(), gw, "default", "sb", time.Minute)
	if err != nil || sb.GetStatus().GetPhase() != pb.SandboxPhase_SANDBOX_PHASE_READY {
		t.Fatalf("sb=%v err=%v", sb, err)
	}
}

func TestLifecycle_ErrorPhase(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gw.EXPECT().StopSandbox(gomock.Any(), "default", "sb").Return(&types.Sandbox{ID: "id-1"}, nil)
	gw.EXPECT().WatchSandbox(gomock.Any(), watchReqMatcher{workspace: "default", sandbox: "sb"}).Return(&scriptedStream{events: []*pb.SandboxStreamEvent{
		phaseEvent(pb.SandboxPhase_SANDBOX_PHASE_ERROR),
	}}, nil)

	_, err := Stop(context.Background(), gw, "default", "sb", time.Minute)
	var le *ErrLifecycle
	if !errors.As(err, &le) {
		t.Fatalf("err = %v, want ErrLifecycle", err)
	}
}

func TestLifecycle_StreamEnded(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gw.EXPECT().StartSandbox(gomock.Any(), "default", "sb").Return(&types.Sandbox{ID: "id-1"}, nil)
	gw.EXPECT().WatchSandbox(gomock.Any(), watchReqMatcher{workspace: "default", sandbox: "sb"}).Return(&scriptedStream{}, nil)

	_, err := Start(context.Background(), gw, "default", "sb", time.Minute)
	var se *ErrLifecycleStreamEnded
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want ErrLifecycleStreamEnded", err)
	}
}

// Ensure the mock satisfies gateway.Gateway (compile-time).
var _ gateway.Gateway = (*mock.MockGateway)(nil)
