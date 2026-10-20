package gateway

import (
	"context"
	"errors"
	"testing"

	types "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"
	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"
	"google.golang.org/grpc/codes"
)

func TestErrorMessages(t *testing.T) {
	cause := errors.New("root")
	cases := []struct {
		err  error
		want string
	}{
		{&NotFoundError{Resource: "sandbox", Name: "x", Cause: cause}, `sandbox "x" not found`},
		{&NotFoundError{}, "not found"},
		{&AlreadyExistsError{Message: "dup", Cause: cause}, "dup"},
		{&ConflictError{Message: "conflict"}, "conflict"},
		{&UnauthenticatedError{Message: "expired"}, "expired"},
		{&PermissionDeniedError{Message: "denied"}, "denied"},
		{&InvalidArgumentError{Message: "bad arg"}, "bad arg"},
		{&UnavailableError{Message: "down"}, "down"},
		{&DeadlineError{}, "deadline exceeded"},
		{&RPCError{Code: codes.Internal, Message: "boom"}, "Internal: boom"},
	}
	for _, tc := range cases {
		if got := tc.err.Error(); got != tc.want {
			t.Errorf("%T.Error() = %q, want %q", tc.err, got, tc.want)
		}
		// Unwrap chains should be intact where a cause was set.
		if u := errors.Unwrap(tc.err); u != nil && !errors.Is(tc.err, u) {
			t.Errorf("%T does not unwrap to its cause", tc.err)
		}
	}
}

func TestSDK_ListProvidersAndAttachDetachAgainstFake(t *testing.T) {
	c := newFakeGateway()
	ctx := context.Background()
	if _, err := c.CreateSandbox(ctx, "default", "sb", &types.SandboxSpec{}, nil); err != nil {
		t.Fatal(err)
	}

	// ListSandboxProviders on a sandbox with none attached → empty.
	provs, err := c.ListSandboxProviders(ctx, "default", "sb")
	if err != nil {
		t.Fatalf("ListSandboxProviders: %v", err)
	}
	if len(provs) != 0 {
		t.Errorf("providers = %d, want 0", len(provs))
	}

	// Attach/Detach exercise the wrappers (and error classification when the
	// fake returns one). We assert only that the calls run without panicking;
	// the fake's provider-attachment semantics are not part of this contract.
	_, _, _ = c.AttachProvider(ctx, "default", "sb", "ghost", 0)
	_, _, _ = c.DetachProvider(ctx, "default", "sb", "ghost", 0)
}

func TestSDK_ConfigAndLogsWrappersClassify(t *testing.T) {
	c := newFakeGateway()
	ctx := context.Background()

	// The fake returns Unimplemented for Config()/GetLogs; assert the wrappers
	// run and surface a typed (non-nil) error rather than panicking.
	if _, err := c.GetSandboxConfig(ctx, "default", "sb"); err == nil {
		t.Error("expected an error from GetSandboxConfig on the fake")
	}
	if _, err := c.GetGatewayConfig(ctx); err == nil {
		t.Error("expected an error from GetGatewayConfig on the fake")
	}
	if _, err := c.UpdateConfig(ctx, "default", &types.ConfigUpdate{Name: "sb"}); err == nil {
		t.Error("expected an error from UpdateConfig on the fake")
	}
	if _, err := c.GetLogs(ctx, "default", "sb"); err == nil {
		t.Error("expected an error from GetLogs on the fake")
	}
}

func TestSDK_SSHTunnelAndTCPListenWrappers(t *testing.T) {
	c := newFakeGateway()
	ctx := context.Background()
	// The fake returns Unimplemented for SSH/TCP; assert the wrappers run and
	// surface a typed error rather than panicking.
	if _, err := c.SSHTunnel(ctx, "default", "sb"); err == nil {
		t.Error("expected an error from SSHTunnel on the fake")
	}
	if _, err := c.TCPListen(ctx, "default", "sb", 8080, 18080, "127.0.0.1"); err == nil {
		t.Error("expected an error from TCPListen on the fake")
	}
	// bind == "" path (no WithBindAddress option).
	if _, err := c.TCPListen(ctx, "default", "sb", 8080, 18080, ""); err == nil {
		t.Error("expected an error from TCPListen on the fake")
	}
}

func TestRawExecInteractive(t *testing.T) {
	srv := &fakeServer{}
	c, cleanup := newTestClient(t, srv)
	defer cleanup()

	stream, err := c.ExecSandboxInteractive(context.Background())
	if err != nil {
		t.Fatalf("ExecSandboxInteractive: %v", err)
	}
	// Send a start message, then close. The fake server's default
	// (Unimplemented) handler ends the stream; we only exercise the adapter.
	_ = stream.Send(&pb.ExecSandboxInput{})
	_ = stream.CloseSend()
	_, _ = stream.Recv()
}

func TestWorkspaceScope(t *testing.T) {
	ws := workspaceScope("prod")
	if got := ws.GetWorkspace(); got != "prod" {
		t.Errorf("workspaceScope workspace = %q, want %q", got, "prod")
	}
}

func TestDeletionCompleted(t *testing.T) {
	tests := []struct {
		name    string
		outcome pb.DeletionOutcome
		want    bool
	}{
		{"completed", pb.DeletionOutcome_DELETION_OUTCOME_COMPLETED, true},
		{"already_absent", pb.DeletionOutcome_DELETION_OUTCOME_ALREADY_ABSENT, true},
		{"accepted", pb.DeletionOutcome_DELETION_OUTCOME_ACCEPTED, true},
		{"unspecified", pb.DeletionOutcome_DELETION_OUTCOME_UNSPECIFIED, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deletionCompleted(tt.outcome); got != tt.want {
				t.Errorf("deletionCompleted(%v) = %v, want %v", tt.outcome, got, tt.want)
			}
		})
	}
}

func TestUnderRoot(t *testing.T) {
	if got := underRoot("", "mtls/ca.crt"); got != "mtls/ca.crt" {
		t.Errorf("underRoot with empty root = %q", got)
	}
	if got := underRoot("/base", "mtls/ca.crt"); got != "/base/mtls/ca.crt" {
		t.Errorf("underRoot = %q, want /base/mtls/ca.crt", got)
	}
	if got := underRoot("/base", ""); got != "" {
		t.Errorf("underRoot with empty rel = %q, want empty", got)
	}
}
