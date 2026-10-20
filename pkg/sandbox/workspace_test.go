package sandbox

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"
	"go.uber.org/mock/gomock"

	"github.com/openshift-online/openshellctl/pkg/gateway/mock"
)

func TestEmptyWorkspace_AllEntryPoints(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)

	tests := []struct {
		name string
		fn   func() error
	}{
		{"Create", func() error {
			_, err := Create(context.Background(), CreateDeps{GW: gw}, &CreateRequest{Workspace: ""}, false)
			return err
		}},
		{"Exec", func() error {
			_, err := Exec(context.Background(), ExecDeps{GW: gw, Stdout: io.Discard, Stderr: io.Discard}, &ExecRequest{Workspace: ""})
			return err
		}},
		{"ExecInteractive", func() error {
			_, err := ExecInteractive(context.Background(), ExecInteractiveDeps{GW: gw, Stdin: bytes.NewReader(nil), Stdout: io.Discard, Stderr: io.Discard}, &ExecRequest{Workspace: ""})
			return err
		}},
		{"Logs", func() error {
			return Logs(context.Background(), LogsDeps{GW: gw, Stdout: io.Discard, Stderr: io.Discard}, &LogsRequest{Workspace: ""})
		}},
		{"Stop", func() error {
			_, err := Stop(context.Background(), gw, "", "sb", time.Minute)
			return err
		}},
		{"Start", func() error {
			_, err := Start(context.Background(), gw, "", "sb", time.Minute)
			return err
		}},
		{"Delete", func() error {
			return Delete(context.Background(), gw, nil, "", DeleteRequest{Workspace: "", Names: []string{"sb"}}, nil)
		}},
		{"WatchUntilReady", func() error {
			_, err := WatchUntilReady(context.Background(), gw, "", "sb", pb.SandboxPhase_SANDBOX_PHASE_PROVISIONING, time.Minute, false, nil, nil)
			return err
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.fn()
			if !errors.Is(err, ErrEmptyWorkspace) {
				t.Errorf("got %v, want ErrEmptyWorkspace", err)
			}
		})
	}
}
