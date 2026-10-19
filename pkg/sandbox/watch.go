package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	dm "github.com/NVIDIA/OpenShell/sdk/go/proto/datamodelv1"
	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"

	"github.com/openshift-online/openshellctl/pkg/gateway"
)

// ProgressSink receives provisioning progress. internal/cli implements it for
// interactive/plain/silent modes; tests use a recording sink.
type ProgressSink interface {
	Header(name string)
	StepDone(label string, elapsed time.Duration)
	StepActive(label, detail string)
	Warning(msg string)
	Error(msg string)
}

// nopSink is a ProgressSink that discards everything.
type nopSink struct{}

func (nopSink) Header(string)                  {}
func (nopSink) StepDone(string, time.Duration) {}
func (nopSink) StepActive(string, string)      {}
func (nopSink) Warning(string)                 {}
func (nopSink) Error(string)                   {}

// NopSink returns a no-op ProgressSink.
func NopSink() ProgressSink { return nopSink{} }

// Clock returns the current time (injected for tests).
type Clock func() time.Time

// WatchOutcome is the result of WatchUntilReady.
type WatchOutcome struct {
	Final       *pb.Sandbox
	Phase       pb.SandboxPhase
	ErrorReason string
}

// ErrProvisionFailed reports the sandbox entering Error while provisioning.
type ErrProvisionFailed struct{ Reason string }

func (e *ErrProvisionFailed) Error() string {
	if e.Reason == "" {
		return "sandbox entered error phase while provisioning"
	}
	return "sandbox entered error phase while provisioning: " + e.Reason
}

// ErrProvisionTimeout reports provisioning exceeding the idle timeout.
type ErrProvisionTimeout struct {
	After      time.Duration
	LastStatus string
	GPUHint    bool
}

func (e *ErrProvisionTimeout) Error() string {
	msg := fmt.Sprintf("sandbox provisioning timed out after %ds", int(e.After.Seconds()))
	if e.LastStatus != "" {
		msg += ". Last reported status: " + e.LastStatus
	}
	if e.GPUHint {
		msg += ". Hint: this may be because the available GPU is already in use by another sandbox."
	}
	return msg
}

// ErrStreamEnded reports the watch stream ending before a terminal phase.
var ErrStreamEnded = errors.New("sandbox provisioning stream ended before reaching terminal phase")

// workspaceScope builds the WorkspaceSelector for raw proto requests that
// require a workspace-scoped lookup (watch, exec, logs). Panics on empty
// workspace so callers surface the bug at test time, not as a silent
// server-side InvalidArgument.
func workspaceScope(workspace string) *dm.WorkspaceSelector {
	if workspace == "" {
		panic("workspaceScope called with empty workspace")
	}
	return &dm.WorkspaceSelector{
		Selection: &dm.WorkspaceSelector_Workspace{Workspace: workspace},
	}
}

// progress metadata keys that reset the idle deadline (common.rs:510-534).
const (
	metaCompleteStep = "openshell.progress.complete_step"
	metaActiveStep   = "openshell.progress.active_step"
	metaActiveDetail = "openshell.progress.active_detail"
)

// WatchUntilReady follows a create watch until READY (after a non-Ready phase),
// Error, timeout, or EOF (run.rs:558-1023, A.7). The idle deadline resets only
// on provisioning-progress events. Ready is accepted only after a non-Ready
// phase was seen (stale-Ready guard).
func WatchUntilReady(ctx context.Context, gw gateway.Gateway, workspace, sandboxName string, initialPhase pb.SandboxPhase, idleTimeout time.Duration, gpuRequested bool, sink ProgressSink, clock Clock) (*WatchOutcome, error) {
	if sink == nil {
		sink = nopSink{}
	}
	if clock == nil {
		clock = time.Now
	}
	if idleTimeout == 0 {
		idleTimeout = 300 * time.Second
	}

	req := &pb.WatchSandboxRequest{
		WorkspaceScope: workspaceScope(workspace),
		Sandbox:        sandboxName,
		FollowStatus:   true,
		FollowLogs:     true,
		FollowEvents:   true,
		LogTailLines:   200,
		EventTail:      50,
		StopOnTerminal: false,
		LogSources:     []string{"gateway"},
	}
	stream, err := gw.WatchSandbox(ctx, req)
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	sawNonReady := initialPhase != pb.SandboxPhase_SANDBOX_PHASE_READY
	deadline := clock().Add(idleTimeout)
	var lastStatus string
	var lastActiveKey string

	for {
		ev, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil, ErrStreamEnded
		}
		if err != nil {
			return nil, err
		}

		if now := clock(); now.After(deadline) {
			return nil, &ErrProvisionTimeout{After: idleTimeout, LastStatus: lastStatus, GPUHint: gpuRequested}
		}

		switch {
		case ev.GetSandbox() != nil:
			sb := ev.GetSandbox()
			phase := sb.GetStatus().GetPhase()
			lastStatus = phase.String()
			if phase != pb.SandboxPhase_SANDBOX_PHASE_READY {
				sawNonReady = true
			}
			switch phase {
			case pb.SandboxPhase_SANDBOX_PHASE_READY:
				if sawNonReady {
					return &WatchOutcome{Final: sb, Phase: phase}, nil
				}
			case pb.SandboxPhase_SANDBOX_PHASE_ERROR:
				reason := readyFalseCondition(sb)
				sink.Error(reason)
				return nil, &ErrProvisionFailed{Reason: reason}
			case pb.SandboxPhase_SANDBOX_PHASE_COMPLETED:
				reason := readyFalseCondition(sb)
				sink.Error(reason)
				return nil, &ErrProvisionFailed{Reason: reason}
			}
		case ev.GetEvent() != nil:
			e := ev.GetEvent()
			if resetsIdle(e) {
				deadline = clock().Add(idleTimeout)
			}
			if md := e.GetMetadata(); md != nil {
				if step, ok := md[metaCompleteStep]; ok {
					elapsed := clock().Sub(deadline.Add(-idleTimeout))
					sink.StepDone(step, elapsed)
					lastActiveKey = ""
				}
				if step, ok := md[metaActiveStep]; ok {
					detail := md[metaActiveDetail]
					key := step + "\x00" + detail
					if key != lastActiveKey {
						sink.StepActive(step, detail)
						lastActiveKey = key
					}
				}
			}
		case ev.GetWarning() != nil:
			sink.Warning(ev.GetWarning().GetMessage())
		}
	}
}

// resetsIdle reports whether a platform event is a provisioning-progress event.
func resetsIdle(e *pb.PlatformEvent) bool {
	md := e.GetMetadata()
	if md != nil {
		if _, ok := md[metaCompleteStep]; ok {
			return true
		}
		if _, ok := md[metaActiveStep]; ok {
			return true
		}
		if _, ok := md[metaActiveDetail]; ok {
			return true
		}
	}
	return e.GetSource() == "vm"
}

// readyFalseCondition returns "<reason>: <message>" from the Ready=False
// condition, or a generic message when absent.
func readyFalseCondition(sb *pb.Sandbox) string {
	for _, c := range sb.GetStatus().GetConditions() {
		if c.GetType() == "Ready" && c.GetStatus() == "False" {
			if c.GetReason() != "" || c.GetMessage() != "" {
				return c.GetReason() + ": " + c.GetMessage()
			}
		}
	}
	return ""
}
