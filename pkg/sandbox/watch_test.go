package sandbox

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"
	"go.uber.org/mock/gomock"

	"github.com/openshift-online/openshellctl/pkg/gateway/mock"
)

func eventProgress() *pb.SandboxStreamEvent {
	return &pb.SandboxStreamEvent{Payload: &pb.SandboxStreamEvent_Event{
		Event: &pb.PlatformEvent{Metadata: map[string]string{metaActiveStep: "Pulling image"}},
	}}
}

func errorSandboxEvent(reason, message string) *pb.SandboxStreamEvent {
	return &pb.SandboxStreamEvent{Payload: &pb.SandboxStreamEvent_Sandbox{
		Sandbox: &pb.Sandbox{Status: &pb.SandboxStatus{
			Phase:      pb.SandboxPhase_SANDBOX_PHASE_ERROR,
			Conditions: []*pb.SandboxCondition{{Type: "Ready", Status: "False", Reason: reason, Message: message}},
		}},
	}}
}

func TestWatchUntilReady_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gw.EXPECT().WatchSandbox(gomock.Any(), watchReqMatcher{workspace: "default", sandbox: "sb-1"}).Return(&scriptedStream{events: []*pb.SandboxStreamEvent{
		phaseEvent(pb.SandboxPhase_SANDBOX_PHASE_PROVISIONING),
		phaseEvent(pb.SandboxPhase_SANDBOX_PHASE_READY),
	}}, nil)

	now := time.Unix(1000, 0)
	out, err := WatchUntilReady(context.Background(), gw, "default", "sb-1", pb.SandboxPhase_SANDBOX_PHASE_PROVISIONING, time.Minute, false, nil, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if out.Phase != pb.SandboxPhase_SANDBOX_PHASE_READY {
		t.Errorf("phase = %v", out.Phase)
	}
}

func TestWatchUntilReady_StaleReadyGuard(t *testing.T) {
	// initialPhase READY and first event READY: without a non-Ready in between,
	// Ready is not accepted; the stream then ends → ErrStreamEnded.
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gw.EXPECT().WatchSandbox(gomock.Any(), watchReqMatcher{workspace: "default", sandbox: "sb-1"}).Return(&scriptedStream{events: []*pb.SandboxStreamEvent{
		phaseEvent(pb.SandboxPhase_SANDBOX_PHASE_READY),
	}}, nil)

	now := time.Unix(1000, 0)
	_, err := WatchUntilReady(context.Background(), gw, "default", "sb-1", pb.SandboxPhase_SANDBOX_PHASE_READY, time.Minute, false, nil, func() time.Time { return now })
	if !errors.Is(err, ErrStreamEnded) {
		t.Fatalf("err = %v, want ErrStreamEnded (stale-Ready guard)", err)
	}
}

func TestWatchUntilReady_ErrorCondition(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gw.EXPECT().WatchSandbox(gomock.Any(), watchReqMatcher{workspace: "default", sandbox: "sb-1"}).Return(&scriptedStream{events: []*pb.SandboxStreamEvent{
		phaseEvent(pb.SandboxPhase_SANDBOX_PHASE_PROVISIONING),
		errorSandboxEvent("ImagePullBackOff", "cannot pull image"),
	}}, nil)

	now := time.Unix(1000, 0)
	_, err := WatchUntilReady(context.Background(), gw, "default", "sb-1", pb.SandboxPhase_SANDBOX_PHASE_PROVISIONING, time.Minute, false, nil, func() time.Time { return now })
	var pf *ErrProvisionFailed
	if !errors.As(err, &pf) {
		t.Fatalf("err = %v, want ErrProvisionFailed", err)
	}
	if pf.Reason != "ImagePullBackOff: cannot pull image" {
		t.Errorf("reason = %q", pf.Reason)
	}
}

func TestWatchUntilReady_Timeout(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	// Two non-progress snapshots; the clock advances past the idle deadline
	// between events so the timeout fires.
	gw.EXPECT().WatchSandbox(gomock.Any(), watchReqMatcher{workspace: "default", sandbox: "sb-1"}).Return(&scriptedStream{events: []*pb.SandboxStreamEvent{
		phaseEvent(pb.SandboxPhase_SANDBOX_PHASE_PROVISIONING),
		phaseEvent(pb.SandboxPhase_SANDBOX_PHASE_PROVISIONING),
	}}, nil)

	times := []time.Time{
		time.Unix(1000, 0), // deadline set to 1000+10=1010
		time.Unix(1005, 0), // first event: within deadline
		time.Unix(2000, 0), // second event: past deadline → timeout
	}
	i := 0
	clock := func() time.Time {
		t := times[i]
		if i < len(times)-1 {
			i++
		}
		return t
	}
	_, err := WatchUntilReady(context.Background(), gw, "default", "sb-1", pb.SandboxPhase_SANDBOX_PHASE_PROVISIONING, 10*time.Second, true, nil, clock)
	var to *ErrProvisionTimeout
	if !errors.As(err, &to) {
		t.Fatalf("err = %v, want ErrProvisionTimeout", err)
	}
	if !to.GPUHint {
		t.Error("GPU hint should be set when gpuRequested")
	}
}

func TestWatchUntilReady_ProgressResetsIdle(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gw.EXPECT().WatchSandbox(gomock.Any(), watchReqMatcher{workspace: "default", sandbox: "sb-1"}).Return(&scriptedStream{events: []*pb.SandboxStreamEvent{
		phaseEvent(pb.SandboxPhase_SANDBOX_PHASE_PROVISIONING),
		eventProgress(), // resets the idle deadline
		phaseEvent(pb.SandboxPhase_SANDBOX_PHASE_READY),
	}}, nil)

	// idle timeout 20s. clock() is called once per loop for the deadline check
	// and again in the progress branch to reset. Sequence (each event = one
	// deadline-check call, progress adds a reset call):
	//   init deadline: t=0  → deadline=20
	//   ev1 check:     t=5   (5<20 ok)
	//   ev2 check:     t=10  (10<20 ok); progress reset: t=12 → deadline=32
	//   ev3 check:     t=25  (25<32 ok, thanks to the reset) → READY
	times := []time.Time{
		time.Unix(0, 0),
		time.Unix(5, 0),
		time.Unix(10, 0),
		time.Unix(12, 0),
		time.Unix(25, 0),
	}
	i := 0
	clock := func() time.Time {
		tt := times[i]
		if i < len(times)-1 {
			i++
		}
		return tt
	}
	out, err := WatchUntilReady(context.Background(), gw, "default", "sb-1", pb.SandboxPhase_SANDBOX_PHASE_PROVISIONING, 20*time.Second, false, nil, clock)
	if err != nil {
		t.Fatalf("progress should reset idle deadline; err = %v", err)
	}
	if out.Phase != pb.SandboxPhase_SANDBOX_PHASE_READY {
		t.Errorf("phase = %v", out.Phase)
	}
}

func TestWatchUntilReady_CompletedIsTerminal(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gw.EXPECT().WatchSandbox(gomock.Any(), watchReqMatcher{workspace: "default", sandbox: "sb-1"}).Return(&scriptedStream{events: []*pb.SandboxStreamEvent{
		phaseEvent(pb.SandboxPhase_SANDBOX_PHASE_PROVISIONING),
		phaseEvent(pb.SandboxPhase_SANDBOX_PHASE_COMPLETED),
	}}, nil)

	now := time.Unix(1000, 0)
	_, err := WatchUntilReady(context.Background(), gw, "default", "sb-1", pb.SandboxPhase_SANDBOX_PHASE_PROVISIONING, time.Minute, false, nil, func() time.Time { return now })
	var pf *ErrProvisionFailed
	if !errors.As(err, &pf) {
		t.Fatalf("err = %v, want ErrProvisionFailed for COMPLETED phase", err)
	}
}

func TestProvisionErrorMessages(t *testing.T) {
	if got := (&ErrProvisionFailed{}).Error(); got != "sandbox entered error phase while provisioning" {
		t.Errorf("generic = %q", got)
	}
	to := &ErrProvisionTimeout{After: 300 * time.Second, LastStatus: "Provisioning", GPUHint: true}
	msg := to.Error()
	if !strings.Contains(msg, "timed out after 300s") || !strings.Contains(msg, "Provisioning") || !strings.Contains(msg, "GPU") {
		t.Errorf("timeout msg = %q", msg)
	}
}
