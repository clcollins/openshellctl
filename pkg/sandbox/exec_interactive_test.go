package sandbox

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"

	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"
	"go.uber.org/mock/gomock"

	"github.com/openshift-online/openshellctl/pkg/gateway/mock"
)

func TestBuildExecStartInput(t *testing.T) {
	r := &ExecRequest{Command: []string{"bash"}, Workdir: "/w", Timeout: 5, Env: map[string]string{"A": "B"}, Cols: 100, Rows: 30}
	in := buildExecStartInput("sb-9", r)
	start := in.GetStart()
	if start == nil {
		t.Fatal("expected Start payload")
	}
	if start.Sandbox != "sb-9" || !start.Tty || start.Cols != 100 || start.Rows != 30 {
		t.Errorf("start = %+v", start)
	}
	if len(start.Stdin) != 0 {
		t.Errorf("start stdin should be empty, got %q", start.Stdin)
	}
	if start.Workdir != "/w" || start.Environment["A"] != "B" {
		t.Errorf("start fields = %+v", start)
	}
	if start.ExecutionTimeout == nil || start.ExecutionTimeout.Seconds != 5 {
		t.Errorf("execution timeout: %+v", start.ExecutionTimeout)
	}
}

func TestBuildExecStartInput_ZeroTimeout(t *testing.T) {
	r := &ExecRequest{Command: []string{"bash"}, Timeout: 0, Cols: 80, Rows: 24}
	in := buildExecStartInput("sb-1", r)
	start := in.GetStart()
	if start == nil {
		t.Fatal("expected Start payload")
	}
	if start.ExecutionTimeout != nil {
		t.Errorf("zero timeout should produce nil ExecutionTimeout, got %v", start.ExecutionTimeout)
	}
}

func TestStdinAndResizeInput(t *testing.T) {
	if got := stdinInput([]byte("xyz")).GetStdin(); string(got) != "xyz" {
		t.Errorf("stdinInput = %q", got)
	}
	rz := resizeInput(90, 20).GetResize()
	if rz == nil || rz.Cols != 90 || rz.Rows != 20 {
		t.Errorf("resizeInput = %+v", rz)
	}
}

// fakeBidi is a scripted BidiStream: it records sent inputs and replays a fixed
// set of output events.
type fakeBidi struct {
	mu     sync.Mutex
	sent   []*pb.ExecSandboxInput
	events []*pb.ExecSandboxEvent
	i      int
}

func (b *fakeBidi) Send(in *pb.ExecSandboxInput) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sent = append(b.sent, in)
	return nil
}

func (b *fakeBidi) Recv() (*pb.ExecSandboxEvent, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.i >= len(b.events) {
		return nil, io.EOF
	}
	ev := b.events[b.i]
	b.i++
	return ev, nil
}
func (b *fakeBidi) CloseSend() error { return nil }

func TestPumpInteractiveOutput_BreaksOnExit(t *testing.T) {
	bidi := &fakeBidi{events: []*pb.ExecSandboxEvent{
		stdoutEvent("a"),
		stderrEvent("b"),
		exitEvent(5),
		stdoutEvent("should-not-be-read"),
	}}
	var out, errOut bytes.Buffer
	code, err := pumpInteractiveOutput(bidi, &out, &errOut)
	if err != nil {
		t.Fatalf("pump: %v", err)
	}
	if code != 5 {
		t.Errorf("exit = %d, want 5 (breaks on Exit)", code)
	}
	if out.String() != "a" || errOut.String() != "b" {
		t.Errorf("out=%q err=%q", out.String(), errOut.String())
	}
}

func TestExecInteractive_SendsStartAndReturnsExit(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	bidi := &fakeBidi{events: []*pb.ExecSandboxEvent{
		stdoutEvent("hi"),
		exitEvent(0),
	}}
	gw.EXPECT().ExecSandboxInteractive(gomock.Any()).Return(bidi, nil)

	var out bytes.Buffer
	code, err := ExecInteractive(context.Background(), ExecInteractiveDeps{
		GW:     gw,
		Stdin:  bytes.NewReader(nil), // immediate EOF
		Stdout: &out,
		Stderr: io.Discard,
	}, "sb-1", &ExecRequest{Command: []string{"bash"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("interactive: %v", err)
	}
	if code != 0 {
		t.Errorf("code = %d", code)
	}
	if out.String() != "hi" {
		t.Errorf("stdout = %q", out.String())
	}
	// The first sent message must be the Start payload.
	bidi.mu.Lock()
	defer bidi.mu.Unlock()
	if len(bidi.sent) == 0 || bidi.sent[0].GetStart() == nil {
		t.Errorf("first sent should be Start, got %+v", bidi.sent)
	}
}
