package cli

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	types "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"
	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"
	"go.uber.org/mock/gomock"

	"github.com/openshift-online/openshellctl/pkg/gateway"
	"github.com/openshift-online/openshellctl/pkg/gateway/mock"
	"github.com/openshift-online/openshellctl/pkg/sandbox"
)

func TestArgsFromName(t *testing.T) {
	if got := argsFromName(""); got != nil {
		t.Errorf("empty → %v, want nil", got)
	}
	if got := argsFromName("demo"); len(got) != 1 || got[0] != "demo" {
		t.Errorf("name → %v", got)
	}
}

func TestIsReady(t *testing.T) {
	sb := &types.Sandbox{}
	sb.Status.Phase = types.SandboxReady
	if !isReady(sb) {
		t.Error("Ready should be ready")
	}
	sb.Status.Phase = types.SandboxProvisioning
	if isReady(sb) {
		t.Error("Provisioning is not ready")
	}
}

func TestTerminalSizeDefaultsOnBadFd(t *testing.T) {
	// -1 is never a terminal → defaults.
	if cols, rows := terminalSize(-1); cols != 80 || rows != 24 {
		t.Errorf("terminalSize(-1) = %d,%d, want 80,24", cols, rows)
	}
}

func TestReadStdinCapped(t *testing.T) {
	data, err := readStdinCapped(strings.NewReader("hello"))
	if err != nil || string(data) != "hello" {
		t.Fatalf("data=%q err=%v", data, err)
	}

	big := strings.NewReader(strings.Repeat("x", sandbox.MaxStdinPayload+1))
	_, err = readStdinCapped(big)
	if err == nil || exitCodeFor(err) != ExitUsage {
		t.Fatalf("over-cap err = %v exit = %d, want usage", err, exitCodeFor(err))
	}
}

func TestFlagTypeStrings(t *testing.T) {
	st := &ttyTriState{}
	f := ttyStateFlag{st, true}
	if f.Type() != "ttyTriState" {
		t.Errorf("Type = %q", f.Type())
	}
	if f.String() != "" {
		t.Errorf("String = %q", f.String())
	}
	if err := f.Set("notabool"); err == nil {
		t.Error("Set should reject a non-bool")
	}
}

// execReadyStream returns a stream that emits one exit event with the given code.
type oneExitStream struct {
	code int32
	done bool
}

func (s *oneExitStream) Recv() (*pb.ExecSandboxEvent, error) {
	if s.done {
		return nil, io.EOF
	}
	s.done = true
	return &pb.ExecSandboxEvent{Payload: &pb.ExecSandboxEvent_Exit{Exit: &pb.ExecSandboxExit{ExitCode: s.code}}}, nil
}
func (s *oneExitStream) Close() {}

func TestRunExecStreaming_PropagatesExitCode(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	sb := &types.Sandbox{ID: "sb-1", Name: "demo"}
	sb.Status.Phase = types.SandboxReady
	gw.EXPECT().GetSandbox(gomock.Any(), "default", "demo").Return(sb, nil)
	gw.EXPECT().ExecSandbox(gomock.Any(), gomock.Any()).Return(&oneExitStream{code: 7}, nil)

	cmd := newSandboxExecCommand()
	cmd.SetContext(context.Background())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader("")) // non-tty stdin

	req := &sandbox.ExecRequest{Workspace: "default", Name: "demo", Command: []string{"false"}}
	err := runExecStreaming(cmd, gw, req, false)
	if err == nil || exitCodeFor(err) != 7 {
		t.Fatalf("err = %v exit = %d, want remote exit 7", err, exitCodeFor(err))
	}
}

func TestRunExecStreaming_ZeroExitIsNil(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	sb := &types.Sandbox{ID: "sb-1", Name: "demo"}
	sb.Status.Phase = types.SandboxReady
	gw.EXPECT().GetSandbox(gomock.Any(), "default", "demo").Return(sb, nil)
	gw.EXPECT().ExecSandbox(gomock.Any(), gomock.Any()).Return(&oneExitStream{code: 0}, nil)

	cmd := newSandboxExecCommand()
	cmd.SetContext(context.Background())
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetIn(strings.NewReader(""))

	req := &sandbox.ExecRequest{Workspace: "default", Name: "demo", Command: []string{"true"}}
	if err := runExecStreaming(cmd, gw, req, false); err != nil {
		t.Fatalf("exit 0 should be nil, got %v", err)
	}
}

func TestRunExecInteractive_NotReadyErrors(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	sb := &types.Sandbox{ID: "sb-1", Name: "demo"}
	sb.Status.Phase = types.SandboxProvisioning
	gw.EXPECT().GetSandbox(gomock.Any(), "default", "demo").Return(sb, nil)

	cmd := newSandboxExecCommand()
	cmd.SetContext(context.Background())
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))

	req := &sandbox.ExecRequest{Workspace: "default", Name: "demo", Command: []string{"bash"}}
	err := runExecInteractive(cmd, gw, req)
	if err == nil || !strings.Contains(err.Error(), "is not ready") {
		t.Fatalf("err = %v, want not-ready", err)
	}
}

var _ gateway.Gateway = (*mock.MockGateway)(nil)
