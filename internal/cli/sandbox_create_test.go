package cli

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	types "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"
	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"
	"go.uber.org/mock/gomock"

	"github.com/openshift-online/openshellctl/pkg/gateway"
	"github.com/openshift-online/openshellctl/pkg/gateway/mock"
	"github.com/openshift-online/openshellctl/pkg/transfer"
)

// TestSandboxCreate_Replace runs `sandbox create --replace` end to end
// through the real command tree via the Feature 0 cliDeps seam
// (mock.MockGateway) — an ordered Get→Delete→Get-NotFound→Create sequence,
// matching pkg/sandbox's own TestCreate_Replace but exercising the full CLI
// flag/validation path on top.
func TestSandboxCreate_Replace(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gomock.InOrder(
		gw.EXPECT().GetSandbox(gomock.Any(), "default", "sb").Return(&types.Sandbox{Name: "sb"}, nil),
		gw.EXPECT().DeleteSandbox(gomock.Any(), "default", "sb").Return(true, nil),
		gw.EXPECT().GetSandbox(gomock.Any(), "default", "sb").
			Return((*types.Sandbox)(nil), &gateway.NotFoundError{Resource: "sandbox", Name: "sb"}),
		gw.EXPECT().CreateSandbox(gomock.Any(), "default", "sb", gomock.Any(), gomock.Any()).
			Return(&types.Sandbox{ID: "id-2", Name: "sb"}, nil),
	)

	out, err := runCmdWithGateway(t, cliDeps{Gateway: gw}, "sandbox", "create",
		"--name", "sb", "--from", "python", "--replace", "--detach", "-o", "json")
	if err != nil {
		t.Fatalf("sandbox create --replace: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "id-2") {
		t.Errorf("expected the freshly-created sandbox in output, got:\n%s", out)
	}
	if !strings.Contains(out, "Replacing existing sandbox sb") {
		t.Errorf("expected a replacing-sandbox progress message before the create step, got:\n%s", out)
	}
}

// TestSandboxCreate_Replace_DeleteRPCError_NonZeroExit is the ticket's
// second acceptance criterion end to end through the CLI: a delete RPC
// failure during --replace must exit with that error's real code, not 0,
// and Create must never be called.
func TestSandboxCreate_Replace_DeleteRPCError_NonZeroExit(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gomock.InOrder(
		gw.EXPECT().GetSandbox(gomock.Any(), "default", "sb").Return(&types.Sandbox{Name: "sb"}, nil),
		gw.EXPECT().DeleteSandbox(gomock.Any(), "default", "sb").Return(false, &gateway.UnauthenticatedError{Message: "token expired"}),
	)
	// No CreateSandbox expectation — gomock fails the test if it's called.

	_, err := runCmdWithGateway(t, cliDeps{Gateway: gw}, "sandbox", "create",
		"--name", "sb", "--from", "python", "--replace", "--detach")
	if err == nil {
		t.Fatal("expected an error")
	}
	if exitCodeFor(err) == ExitOK {
		t.Errorf("exit = %d, want non-zero (the delete RPC error's real code)", exitCodeFor(err))
	}
	if exitCodeFor(err) != ExitAuth {
		t.Errorf("exit = %d, want ExitAuth (3, UnauthenticatedError's mapping)", exitCodeFor(err))
	}
}

// TestSandboxCreate_ReplaceWithoutName_UsageError confirms --replace with no
// resolvable name (no --name, no manifest, server-generated name) fails
// fast with a usage error — mirroring the manifest-side
// "replace requires metadata.name" rule for the flag-only path.
func TestSandboxCreate_ReplaceWithoutName_UsageError(t *testing.T) {
	_, err := runCmd(t, "sandbox", "create", "--from", "python", "--replace")
	if err == nil {
		t.Fatal("expected a usage error")
	}
	if exitCodeFor(err) != ExitUsage {
		t.Errorf("exit = %d, want ExitUsage (2)", exitCodeFor(err))
	}
	if !strings.Contains(err.Error(), "--replace") {
		t.Errorf("error should mention --replace, got: %v", err)
	}
}

// TestSandboxCreate_ReplaceTimeoutFlag_Registered confirms --replace-timeout
// parses as a duration flag (a bare registration/parsing smoke test — the
// actual default-applies-when-Replace-is-set behavior is covered by
// pkg/sandbox/mergespec_test.go).
func TestSandboxCreate_ReplaceTimeoutFlag_Registered(t *testing.T) {
	_, err := runCmd(t, "sandbox", "create", "--replace-timeout", "notaduration")
	if err == nil {
		t.Fatal("expected a flag-parse error for an invalid duration")
	}
	if exitCodeFor(err) != ExitUsage {
		t.Errorf("exit = %d, want ExitUsage (2, cobra flag parse errors map to usage)", exitCodeFor(err))
	}
}

// TestSandboxCreate_NoKeep_AttachTransportLoss_DeletesSandbox proves that
// --no-keep causes an explicit DeleteSandbox RPC when ConnectMainProcess
// returns *transfer.AttachClosedError (transport loss, no exit-status).
// The gateway's ephemeral cleanup hook does not fire on a transport loss, so
// the CLI must delete explicitly.
func TestSandboxCreate_NoKeep_AttachTransportLoss_DeletesSandbox(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)

	gomock.InOrder(
		// (4) raw create (AwaitMainProcessAttachment + RetentionEphemeral require raw proto path)
		gw.EXPECT().CreateSandboxRaw(gomock.Any(), gomock.Any()).
			Return(&pb.Sandbox{}, nil),
		// (7) watch to Ready; initial phase is UNSPECIFIED so sawNonReady=true;
		// stream returns one READY event which immediately satisfies the watch.
		gw.EXPECT().WatchSandbox(gomock.Any(), gomock.Any()).
			Return(&sandboxPhaseStream{phase: pb.SandboxPhase_SANDBOX_PHASE_READY}, nil),
		// GetSandbox to refresh the sandbox after the watch (error ignored by caller).
		gw.EXPECT().GetSandbox(gomock.Any(), "default", gomock.Any()).
			Return(&types.Sandbox{}, nil),
		// DeleteSandbox must be called exactly once on transport loss.
		gw.EXPECT().DeleteSandbox(gomock.Any(), "default", gomock.Any()).
			Return(true, nil),
	)

	stub := &stubTransferClient{connectErr: &transfer.AttachClosedError{}}
	_, err := runCmdWithGateway(t, cliDeps{Gateway: gw, Transfer: stub},
		"sandbox", "create", "--name", "sb-t", "--from", "python", "--no-keep")
	var ace *transfer.AttachClosedError
	if !errors.As(err, &ace) {
		t.Errorf("expected *transfer.AttachClosedError, got %v", err)
	}
}

// sandboxPhaseStream is a gateway.Stream[*pb.SandboxStreamEvent] that returns
// one sandbox-phase event then EOF.
type sandboxPhaseStream struct {
	phase pb.SandboxPhase
	done  bool
}

func (s *sandboxPhaseStream) Recv() (*pb.SandboxStreamEvent, error) {
	if s.done {
		return nil, io.EOF
	}
	s.done = true
	return &pb.SandboxStreamEvent{Payload: &pb.SandboxStreamEvent_Sandbox{
		Sandbox: &pb.Sandbox{Status: &pb.SandboxStatus{Phase: s.phase}},
	}}, nil
}
func (s *sandboxPhaseStream) Close() {}

// stubTransferClient implements transfer.Client with ConnectMainProcess
// returning a fixed error; all other methods delegate to nil (unreachable in
// this test).
type stubTransferClient struct {
	transfer.Client
	connectErr error
}

func (s *stubTransferClient) ConnectMainProcess(_ context.Context, _, _ string, _ bool, _ transfer.Terminal) (int, error) {
	return 0, s.connectErr
}
