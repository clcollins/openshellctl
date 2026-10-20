package sandbox

import (
	"context"
	"io"
	"time"

	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/openshift-online/openshellctl/pkg/gateway"
)

// buildExecStartInput builds the first bidi message for interactive exec
// (run.rs:1846-1861): a Start payload carrying the exec request with tty=true,
// empty stdin, and the initial cols/rows. Pure.
func buildExecStartInput(workspace, sandboxName string, r *ExecRequest) *pb.ExecSandboxInput {
	req := &pb.ExecSandboxRequest{
		WorkspaceScope: workspaceScope(workspace),
		Sandbox:        sandboxName,
		Command:        r.Command,
		Workdir:        r.Workdir,
		Environment:    r.Env,
		Stdin:          nil,
		Tty:            true,
		Cols:           r.Cols,
		Rows:           r.Rows,
	}
	if r.Timeout > 0 {
		req.ExecutionTimeout = durationpb.New(time.Duration(r.Timeout) * time.Second)
	}
	return &pb.ExecSandboxInput{
		Payload: &pb.ExecSandboxInput_Start{Start: req},
	}
}

// stdinInput wraps a raw stdin chunk as a bidi input message. Pure.
func stdinInput(chunk []byte) *pb.ExecSandboxInput {
	return &pb.ExecSandboxInput{Payload: &pb.ExecSandboxInput_Stdin{Stdin: chunk}}
}

// resizeInput wraps a terminal resize as a bidi input message. Pure.
func resizeInput(cols, rows uint32) *pb.ExecSandboxInput {
	return &pb.ExecSandboxInput{Payload: &pb.ExecSandboxInput_Resize{Resize: &pb.ExecSandboxWindowResize{Cols: cols, Rows: rows}}}
}

// ExecInteractiveDeps are the dependencies for ExecInteractive.
type ExecInteractiveDeps struct {
	GW      gateway.Gateway
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	Resizes <-chan [2]uint32 // {cols, rows} updates; nil when unsupported
}

// ExecInteractive runs an interactive (tty) exec over the bidirectional stream
// and returns the remote exit code. It opens the bidi stream, sends the Start
// message, pumps stdin as Stdin chunks and resize events as Resize messages, and
// reads the output stream until Exit (which breaks) or EOF (run.rs:1828-1957).
// The caller has already resolved the sandbox name, put the terminal in raw mode,
// and supplies the resize channel; this function is the thin stream shell.
func ExecInteractive(ctx context.Context, d ExecInteractiveDeps, r *ExecRequest) (int, error) {
	if r.Workspace == "" {
		return 0, ErrEmptyWorkspace
	}
	bidi, err := d.GW.ExecSandboxInteractive(ctx)
	if err != nil {
		return 0, err
	}

	if err := bidi.Send(buildExecStartInput(r.Workspace, r.Name, r)); err != nil {
		return 0, err
	}

	// stdin reader.
	go func() {
		buf := make([]byte, 4096)
		for {
			n, rerr := d.Stdin.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				if serr := bidi.Send(stdinInput(chunk)); serr != nil {
					return
				}
			}
			if rerr != nil {
				return
			}
		}
	}()

	// resize forwarder.
	if d.Resizes != nil {
		go func() {
			for sz := range d.Resizes {
				if serr := bidi.Send(resizeInput(sz[0], sz[1])); serr != nil {
					return
				}
			}
		}()
	}

	return pumpInteractiveOutput(bidi, d.Stdout, d.Stderr)
}

// pumpInteractiveOutput reads the interactive exec output stream, writing
// stdout/stderr through, and returns on Exit (breaks, unlike the one-shot path)
// or EOF (run.rs:1930-1949).
func pumpInteractiveOutput(bidi gateway.BidiStream, stdout, stderr io.Writer) (int, error) {
	exitCode := 0
	for {
		ev, err := bidi.Recv()
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
			return int(exit.GetExitCode()), nil
		}
	}
}
