package sandbox

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	types "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"
	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"
	"go.uber.org/mock/gomock"

	"github.com/openshift-online/openshellctl/pkg/gateway"
	"github.com/openshift-online/openshellctl/pkg/gateway/mock"
)

func TestResolveExecTTY(t *testing.T) {
	tTrue, tFalse := true, false
	cases := []struct {
		name             string
		override         *bool
		stdinTTY, stdout bool
		want             bool
	}{
		{"override true wins", &tTrue, false, false, true},
		{"override false wins", &tFalse, true, true, false},
		{"auto both ttys", nil, true, true, true},
		{"auto stdin only", nil, true, false, false},
		{"auto stdout only", nil, false, true, false},
		{"auto neither", nil, false, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ResolveExecTTY(c.override, c.stdinTTY, c.stdout); got != c.want {
				t.Errorf("ResolveExecTTY = %v, want %v", got, c.want)
			}
		})
	}
}

func TestCheckStdinSize(t *testing.T) {
	if err := CheckStdinSize(MaxStdinPayload); err != nil {
		t.Errorf("at limit should be ok: %v", err)
	}
	err := CheckStdinSize(MaxStdinPayload + 1)
	if err == nil || err.Error() != "stdin payload exceeds 4194304 byte limit; pipe smaller inputs or use `sandbox upload`" {
		t.Errorf("over limit err = %v", err)
	}
}

func TestBuildExecRequest(t *testing.T) {
	r := &ExecRequest{
		Command: []string{"ls", "-la"},
		Workdir: "/work",
		Timeout: 30,
		Env:     map[string]string{"K": "V"},
		Stdin:   []byte("input"),
		TTY:     true,
		Cols:    120,
		Rows:    40,
	}
	req := buildExecRequest("default", "demo", r)
	if req.Sandbox != "demo" || req.Workdir != "/work" {
		t.Errorf("scalar fields: %+v", req)
	}
	if req.WorkspaceScope == nil || req.WorkspaceScope.GetWorkspace() != "default" {
		t.Errorf("workspace scope: %+v", req.WorkspaceScope)
	}
	if req.ExecutionTimeout == nil || req.ExecutionTimeout.Seconds != 30 {
		t.Errorf("execution timeout: %+v", req.ExecutionTimeout)
	}
	if !req.Tty || req.Cols != 120 || req.Rows != 40 {
		t.Errorf("tty/size: %+v", req)
	}
	if string(req.Stdin) != "input" || req.Environment["K"] != "V" {
		t.Errorf("stdin/env: %+v", req)
	}
}

func TestBuildExecRequest_ZeroTimeout(t *testing.T) {
	r := &ExecRequest{Command: []string{"ls"}, Timeout: 0}
	req := buildExecRequest("default", "sb-1", r)
	if req.ExecutionTimeout != nil {
		t.Errorf("zero timeout should produce nil ExecutionTimeout, got %v", req.ExecutionTimeout)
	}
}

// execStream is a scripted one-shot exec stream.
type execStream struct {
	events []*pb.ExecSandboxEvent
	i      int
}

func (s *execStream) Recv() (*pb.ExecSandboxEvent, error) {
	if s.i >= len(s.events) {
		return nil, io.EOF
	}
	ev := s.events[s.i]
	s.i++
	return ev, nil
}
func (s *execStream) Close() {}

func stdoutEvent(data string) *pb.ExecSandboxEvent {
	return &pb.ExecSandboxEvent{Payload: &pb.ExecSandboxEvent_Stdout{Stdout: &pb.ExecSandboxStdout{Data: []byte(data)}}}
}

func stderrEvent(data string) *pb.ExecSandboxEvent {
	return &pb.ExecSandboxEvent{Payload: &pb.ExecSandboxEvent_Stderr{Stderr: &pb.ExecSandboxStderr{Data: []byte(data)}}}
}

func exitEvent(code int32) *pb.ExecSandboxEvent {
	return &pb.ExecSandboxEvent{Payload: &pb.ExecSandboxEvent_Exit{Exit: &pb.ExecSandboxExit{ExitCode: code}}}
}

func readySandbox(id string) *types.Sandbox {
	sb := &types.Sandbox{ID: id, Name: "demo"}
	sb.Status.Phase = types.SandboxReady
	return sb
}

func TestExec_StreamsAndReturnsExitCode(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gw.EXPECT().GetSandbox(gomock.Any(), "default", "demo").Return(readySandbox("sb-1"), nil)
	gw.EXPECT().ExecSandbox(gomock.Any(), gomock.Any()).Return(&execStream{events: []*pb.ExecSandboxEvent{
		stdoutEvent("hello "),
		stderrEvent("warn"),
		stdoutEvent("world"),
		exitEvent(3),
	}}, nil)

	var out, errOut bytes.Buffer
	code, err := Exec(context.Background(), ExecDeps{GW: gw, Stdout: &out, Stderr: &errOut},
		&ExecRequest{Workspace: "default", Name: "demo", Command: []string{"echo"}})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
	if out.String() != "hello world" {
		t.Errorf("stdout = %q", out.String())
	}
	if errOut.String() != "warn" {
		t.Errorf("stderr = %q", errOut.String())
	}
}

func TestExec_NotReady(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	sb := &types.Sandbox{ID: "sb-1", Name: "demo"}
	sb.Status.Phase = types.SandboxProvisioning
	gw.EXPECT().GetSandbox(gomock.Any(), "default", "demo").Return(sb, nil)

	_, err := Exec(context.Background(), ExecDeps{GW: gw, Stdout: io.Discard, Stderr: io.Discard},
		&ExecRequest{Workspace: "default", Name: "demo", Command: []string{"echo"}})
	if err == nil || err.Error() != "sandbox 'demo' is not ready (phase: Provisioning); wait for it to reach Ready state" {
		t.Fatalf("err = %v", err)
	}
}

func TestExec_GetSandboxError(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gw.EXPECT().GetSandbox(gomock.Any(), "default", "demo").Return(nil, &gateway.NotFoundError{Resource: "sandbox", Name: "demo"})

	_, err := Exec(context.Background(), ExecDeps{GW: gw, Stdout: io.Discard, Stderr: io.Discard},
		&ExecRequest{Workspace: "default", Name: "demo", Command: []string{"echo"}})
	var nf *gateway.NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("err = %v, want NotFoundError", err)
	}
}

func TestExec_NoExitEventDefaultsZero(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gw.EXPECT().GetSandbox(gomock.Any(), "default", "demo").Return(readySandbox("sb-1"), nil)
	gw.EXPECT().ExecSandbox(gomock.Any(), gomock.Any()).Return(&execStream{events: []*pb.ExecSandboxEvent{
		stdoutEvent("no exit event"),
	}}, nil)

	code, err := Exec(context.Background(), ExecDeps{GW: gw, Stdout: io.Discard, Stderr: io.Discard},
		&ExecRequest{Workspace: "default", Name: "demo", Command: []string{"echo"}})
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v, want 0/nil", code, err)
	}
}
