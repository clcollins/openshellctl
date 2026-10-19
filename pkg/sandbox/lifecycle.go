package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"

	"github.com/openshift-online/openshellctl/pkg/gateway"
)

// ErrLifecycle reports a sandbox entering Error while awaiting a lifecycle target.
type ErrLifecycle struct{ Target string }

func (e *ErrLifecycle) Error() string {
	return fmt.Sprintf("sandbox entered Error while waiting for %s", e.Target)
}

// ErrLifecycleTimeout reports a lifecycle wait timing out.
type ErrLifecycleTimeout struct {
	Target string
	After  time.Duration
}

func (e *ErrLifecycleTimeout) Error() string {
	return fmt.Sprintf("timed out after %ds waiting for sandbox to reach %s", int(e.After.Seconds()), e.Target)
}

// ErrLifecycleStreamEnded reports the watch stream ending before the target phase.
type ErrLifecycleStreamEnded struct{ Target string }

func (e *ErrLifecycleStreamEnded) Error() string {
	return fmt.Sprintf("sandbox watch ended before reaching %s", e.Target)
}

// Stop stops a sandbox and watches FollowStatus until STOPPED (run.rs:2417-2543).
func Stop(ctx context.Context, gw gateway.Gateway, workspace, name string, timeout time.Duration) (*pb.Sandbox, error) {
	sb, err := gw.StopSandbox(ctx, workspace, name)
	if err != nil {
		return nil, err
	}
	if sb == nil {
		return nil, errors.New("gateway returned no sandbox after stop")
	}
	return watchUntilPhase(ctx, gw, sb.ID, pb.SandboxPhase_SANDBOX_PHASE_STOPPED, "Stopped", timeout)
}

// Start starts a sandbox and watches until READY.
func Start(ctx context.Context, gw gateway.Gateway, workspace, name string, timeout time.Duration) (*pb.Sandbox, error) {
	sb, err := gw.StartSandbox(ctx, workspace, name)
	if err != nil {
		return nil, err
	}
	if sb == nil {
		return nil, errors.New("gateway returned no sandbox after start")
	}
	return watchUntilPhase(ctx, gw, sb.ID, pb.SandboxPhase_SANDBOX_PHASE_READY, "Ready", timeout)
}

// watchUntilPhase follows status until the target phase, Error, timeout, or EOF.
func watchUntilPhase(ctx context.Context, gw gateway.Gateway, id string, target pb.SandboxPhase, targetName string, timeout time.Duration) (*pb.Sandbox, error) {
	if timeout == 0 {
		timeout = 300 * time.Second
	}
	watchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stream, err := gw.WatchSandbox(watchCtx, &pb.WatchSandboxRequest{Sandbox: id, FollowStatus: true})
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	for {
		ev, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil, &ErrLifecycleStreamEnded{Target: targetName}
		}
		if err != nil {
			if errors.Is(watchCtx.Err(), context.DeadlineExceeded) {
				return nil, &ErrLifecycleTimeout{Target: targetName, After: timeout}
			}
			return nil, err
		}
		sb := ev.GetSandbox()
		if sb == nil {
			continue
		}
		switch sb.GetStatus().GetPhase() {
		case target:
			return sb, nil
		case pb.SandboxPhase_SANDBOX_PHASE_ERROR:
			return nil, &ErrLifecycle{Target: targetName}
		}
	}
}
