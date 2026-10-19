package sandbox

import (
	"context"
	"fmt"
	"io"
	"time"

	types "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"
	dm "github.com/NVIDIA/OpenShell/sdk/go/proto/datamodelv1"
	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/openshift-online/openshellctl/pkg/gateway"
)

// MaxStdinPayload is the exec stdin cap (run.rs:1395, 4 MiB).
const MaxStdinPayload = 4 * 1024 * 1024

// ExecRequest is the resolved input for a one-shot exec.
type ExecRequest struct {
	Workspace string
	Name      string
	Command   []string
	Workdir   string
	Timeout   uint32 // seconds, 0 = none
	Env       map[string]string
	Stdin     []byte
	TTY       bool
	Cols      uint32
	Rows      uint32
}

// ResolveExecTTY resolves the effective tty value for exec (run.rs:1461-1462):
// an explicit --tty/--no-tty override wins; otherwise tty is on only when both
// stdin and stdout are terminals.
func ResolveExecTTY(override *bool, stdinTTY, stdoutTTY bool) bool {
	if override != nil {
		return *override
	}
	return stdinTTY && stdoutTTY
}

// CheckStdinSize returns the verbatim over-limit error (run.rs:1449-1452) when
// n exceeds MaxStdinPayload.
func CheckStdinSize(n int) error {
	if n > MaxStdinPayload {
		return fmt.Errorf("stdin payload exceeds %d byte limit; pipe smaller inputs or use `sandbox upload`", MaxStdinPayload)
	}
	return nil
}

// ErrSandboxNotReady is the phase-check error (run.rs:1428-1432).
func ErrSandboxNotReady(name string, phase types.SandboxPhase) error {
	return fmt.Errorf("sandbox '%s' is not ready (phase: %s); wait for it to reach Ready state", name, phase)
}

// buildExecRequest builds the raw ExecSandboxRequest (run.rs:1478-1487). Pure.
func buildExecRequest(workspace, sandboxName string, r *ExecRequest) *pb.ExecSandboxRequest {
	req := &pb.ExecSandboxRequest{
		WorkspaceScope: &dm.WorkspaceSelector{Selection: &dm.WorkspaceSelector_Workspace{Workspace: workspace}},
		Sandbox:        sandboxName,
		Command:        r.Command,
		Workdir:        r.Workdir,
		Environment:    r.Env,
		Stdin:          r.Stdin,
		Tty:            r.TTY,
		Cols:           r.Cols,
		Rows:           r.Rows,
	}
	if r.Timeout > 0 {
		req.ExecutionTimeout = durationpb.New(time.Duration(r.Timeout) * time.Second)
	}
	return req
}

// ExecDeps are the dependencies for Exec.
type ExecDeps struct {
	GW     gateway.Gateway
	Stdout io.Writer
	Stderr io.Writer
}

// Exec runs a one-shot command in a sandbox and returns the remote exit code.
// It performs the GetSandbox phase check, then streams the raw ExecSandbox
// events, writing stdout/stderr through and recording the exit code (run.rs:
// 1401-1518, A.8). The caller supplies the fully-resolved ExecRequest (stdin
// already read and size-checked, tty already resolved). Interactive (bidi) exec
// is handled separately by ExecInteractive.
func Exec(ctx context.Context, d ExecDeps, r *ExecRequest) (int, error) {
	sb, err := d.GW.GetSandbox(ctx, r.Workspace, r.Name)
	if err != nil {
		return 0, err
	}
	if sb == nil {
		return 0, fmt.Errorf("sandbox not found")
	}
	if sb.Status.Phase != types.SandboxReady {
		return 0, ErrSandboxNotReady(r.Name, sb.Status.Phase)
	}

	stream, err := d.GW.ExecSandbox(ctx, buildExecRequest(r.Workspace, r.Name, r))
	if err != nil {
		return 0, err
	}
	defer stream.Close()

	return pumpExecStream(stream, d.Stdout, d.Stderr)
}

// pumpExecStream drains a one-shot exec stream, writing stdout/stderr through and
// returning the recorded exit code (default 0). It does NOT break on Exit — the
// stream is drained fully (run.rs:1497-1515).
func pumpExecStream(stream gateway.Stream[*pb.ExecSandboxEvent], stdout, stderr io.Writer) (int, error) {
	exitCode := 0
	for {
		ev, err := stream.Recv()
		if err == io.EOF {
			return exitCode, nil
		}
		if err != nil {
			return exitCode, err
		}
		if out := ev.GetStdout(); out != nil {
			if _, werr := stdout.Write(out.GetData()); werr != nil {
				return exitCode, werr
			}
		}
		if errOut := ev.GetStderr(); errOut != nil {
			if _, werr := stderr.Write(errOut.GetData()); werr != nil {
				return exitCode, werr
			}
		}
		if exit := ev.GetExit(); exit != nil {
			exitCode = int(exit.GetExitCode())
		}
	}
}
