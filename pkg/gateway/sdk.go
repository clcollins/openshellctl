package gateway

import (
	"context"
	"io"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
	types "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"
	dm "github.com/NVIDIA/OpenShell/sdk/go/proto/datamodelv1"
	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"

	"github.com/openshift-online/openshellctl/pkg/auth"
)

// client is the concrete Gateway backed by a dialed Conn. Typed methods use the
// SDK; the delete-bool, bare-gpu create, watch, exec, ssh, and tcp paths use the
// raw stub. Every call is Classify-wrapped; unary calls retry once on
// Unauthenticated.
type client struct {
	conn *Conn
	auth auth.TokenSource
}

// New builds a Gateway from a dialed Conn and its token source (for auth retry).
func New(conn *Conn, src auth.TokenSource) Gateway {
	return &client{conn: conn, auth: src}
}

func (c *client) sdk() v1.ClientInterface { return c.conn.SDK }

func (c *client) GetSandbox(ctx context.Context, workspace, name string) (*types.Sandbox, error) {
	var out *types.Sandbox
	err := withAuthRetry(ctx, c.auth, func(ctx context.Context) error {
		s, err := c.sdk().Sandboxes().Get(ctx, workspace, name)
		if err != nil {
			return Classify(err, "sandbox", name)
		}
		out = s
		return nil
	})
	return out, err
}

func (c *client) CreateSandbox(ctx context.Context, workspace, name string, spec *types.SandboxSpec, labels map[string]string) (*types.Sandbox, error) {
	var out *types.Sandbox
	err := withAuthRetry(ctx, c.auth, func(ctx context.Context) error {
		s, err := c.sdk().Sandboxes().Create(ctx, workspace, name, spec, v1.WithLabels(labels))
		if err != nil {
			return Classify(err, "sandbox", name)
		}
		out = s
		return nil
	})
	return out, err
}

func (c *client) ListSandboxes(ctx context.Context, workspace string, opts types.ListOptions) ([]*types.Sandbox, error) {
	var out []*types.Sandbox
	err := withAuthRetry(ctx, c.auth, func(ctx context.Context) error {
		s, err := c.sdk().Sandboxes().ListAll(ctx, workspace, opts)
		if err != nil {
			return Classify(err, "sandbox", "")
		}
		out = s
		return nil
	})
	return out, err
}

// DeleteSandbox uses the raw stub so the DeleteSandboxResponse outcome is
// preserved (the SDK's Delete discards it). The DeletionOutcome enum is mapped
// to a bool: Completed and AlreadyAbsent → true; everything else → false.
func (c *client) DeleteSandbox(ctx context.Context, workspace, name string) (bool, error) {
	var deleted bool
	err := withAuthRetry(ctx, c.auth, func(ctx context.Context) error {
		resp, err := c.conn.Raw.DeleteSandbox(ctx, &pb.DeleteSandboxRequest{
			Name:           name,
			WorkspaceScope: workspaceScope(workspace),
		})
		if err != nil {
			return Classify(err, "sandbox", name)
		}
		deleted = deletionCompleted(resp.GetOutcome())
		return nil
	})
	return deleted, err
}

func (c *client) StopSandbox(ctx context.Context, workspace, name string) (*types.Sandbox, error) {
	var out *types.Sandbox
	err := withAuthRetry(ctx, c.auth, func(ctx context.Context) error {
		s, err := c.sdk().Sandboxes().Stop(ctx, workspace, name)
		if err != nil {
			return Classify(err, "sandbox", name)
		}
		out = s
		return nil
	})
	return out, err
}

func (c *client) StartSandbox(ctx context.Context, workspace, name string) (*types.Sandbox, error) {
	var out *types.Sandbox
	err := withAuthRetry(ctx, c.auth, func(ctx context.Context) error {
		s, err := c.sdk().Sandboxes().Start(ctx, workspace, name)
		if err != nil {
			return Classify(err, "sandbox", name)
		}
		out = s
		return nil
	})
	return out, err
}

func (c *client) ListSandboxProviders(ctx context.Context, workspace, sandbox string) ([]*types.Provider, error) {
	var out []*types.Provider
	err := withAuthRetry(ctx, c.auth, func(ctx context.Context) error {
		p, err := c.sdk().Sandboxes().ListAllProviders(ctx, workspace, sandbox)
		if err != nil {
			return Classify(err, "sandbox", sandbox)
		}
		out = p
		return nil
	})
	return out, err
}

func (c *client) AttachProvider(ctx context.Context, workspace, sandbox, provider string, expectedRV uint64) (*types.Sandbox, bool, error) {
	var (
		sb       *types.Sandbox
		attached bool
	)
	err := withAuthRetry(ctx, c.auth, func(ctx context.Context) error {
		res, err := c.sdk().Sandboxes().AttachProvider(ctx, workspace, sandbox, provider, expectedRV)
		if err != nil {
			return Classify(err, "provider", provider)
		}
		sb, attached = res.Sandbox, res.Attached
		return nil
	})
	return sb, attached, err
}

func (c *client) DetachProvider(ctx context.Context, workspace, sandbox, provider string, expectedRV uint64) (*types.Sandbox, bool, error) {
	var (
		sb       *types.Sandbox
		detached bool
	)
	err := withAuthRetry(ctx, c.auth, func(ctx context.Context) error {
		res, err := c.sdk().Sandboxes().DetachProvider(ctx, workspace, sandbox, provider, expectedRV)
		if err != nil {
			return Classify(err, "provider", provider)
		}
		sb, detached = res.Sandbox, res.Detached
		return nil
	})
	return sb, detached, err
}

func (c *client) ListProviders(ctx context.Context, workspace string, opts types.ListOptions) ([]*types.Provider, error) {
	var out []*types.Provider
	err := withAuthRetry(ctx, c.auth, func(ctx context.Context) error {
		p, err := c.sdk().Providers().ListAll(ctx, workspace, opts)
		if err != nil {
			return Classify(err, "provider", "")
		}
		out = p
		return nil
	})
	return out, err
}

func (c *client) CreateProvider(ctx context.Context, workspace string, provider *types.Provider) (*types.Provider, error) {
	var out *types.Provider
	err := withAuthRetry(ctx, c.auth, func(ctx context.Context) error {
		p, err := c.sdk().Providers().Create(ctx, workspace, provider)
		if err != nil {
			name := ""
			if provider != nil {
				name = provider.Name
			}
			return Classify(err, "provider", name)
		}
		out = p
		return nil
	})
	return out, err
}

func (c *client) GetProvider(ctx context.Context, workspace, name string) (*types.Provider, error) {
	var out *types.Provider
	err := withAuthRetry(ctx, c.auth, func(ctx context.Context) error {
		p, err := c.sdk().Providers().Get(ctx, workspace, name)
		if err != nil {
			return Classify(err, "provider", name)
		}
		out = p
		return nil
	})
	return out, err
}

func (c *client) UpdateProvider(ctx context.Context, workspace string, provider *types.Provider) (*types.Provider, error) {
	var out *types.Provider
	err := withAuthRetry(ctx, c.auth, func(ctx context.Context) error {
		p, err := c.sdk().Providers().Update(ctx, workspace, provider)
		if err != nil {
			name := ""
			if provider != nil {
				name = provider.Name
			}
			return Classify(err, "provider", name)
		}
		out = p
		return nil
	})
	return out, err
}

func (c *client) DeleteProvider(ctx context.Context, workspace, name string) error {
	return withAuthRetry(ctx, c.auth, func(ctx context.Context) error {
		if _, err := c.sdk().Providers().Delete(ctx, workspace, name); err != nil {
			return Classify(err, "provider", name)
		}
		return nil
	})
}

func (c *client) GetSandboxConfig(ctx context.Context, workspace, sandbox string) (*types.SandboxConfig, error) {
	var out *types.SandboxConfig
	err := withAuthRetry(ctx, c.auth, func(ctx context.Context) error {
		cfg, err := c.sdk().Config().GetSandbox(ctx, workspace, sandbox)
		if err != nil {
			return Classify(err, "sandbox", sandbox)
		}
		out = cfg
		return nil
	})
	return out, err
}

func (c *client) GetGatewayConfig(ctx context.Context) (*types.GatewayConfig, error) {
	var out *types.GatewayConfig
	err := withAuthRetry(ctx, c.auth, func(ctx context.Context) error {
		cfg, err := c.sdk().Config().GetGateway(ctx)
		if err != nil {
			return Classify(err, "", "")
		}
		out = cfg
		return nil
	})
	return out, err
}

func (c *client) UpdateConfig(ctx context.Context, workspace string, u *types.ConfigUpdate) (*types.ConfigUpdateResult, error) {
	var out *types.ConfigUpdateResult
	err := withAuthRetry(ctx, c.auth, func(ctx context.Context) error {
		res, err := c.sdk().Config().Update(ctx, workspace, u)
		if err != nil {
			return Classify(err, "", "")
		}
		out = res
		return nil
	})
	return out, err
}

func (c *client) GetLogs(ctx context.Context, workspace, sandbox string, opts ...types.LogOption) (*types.LogResult, error) {
	var out *types.LogResult
	err := withAuthRetry(ctx, c.auth, func(ctx context.Context) error {
		res, err := c.sdk().Sandboxes().GetLogs(ctx, workspace, sandbox, opts...)
		if err != nil {
			return Classify(err, "sandbox", sandbox)
		}
		out = res
		return nil
	})
	return out, err
}

func (c *client) CurrentUser(ctx context.Context) (*types.CurrentUser, error) {
	var out *types.CurrentUser
	err := withAuthRetry(ctx, c.auth, func(ctx context.Context) error {
		u, err := c.sdk().Health().GetCurrentUser(ctx)
		if err != nil {
			return Classify(err, "", "")
		}
		out = u
		return nil
	})
	return out, err
}

// SSHTunnel opens the sandbox SSH tunnel over the SDK (port 22).
func (c *client) SSHTunnel(ctx context.Context, workspace, sandbox string) (io.ReadWriteCloser, error) {
	rwc, err := c.sdk().SSH().Tunnel(ctx, workspace, sandbox, 22)
	if err != nil {
		return nil, Classify(err, "sandbox", sandbox)
	}
	return rwc, nil
}

// TCPListen opens a local listener forwarding to the sandbox's remote port.
func (c *client) TCPListen(ctx context.Context, workspace, sandbox string, remotePort, localPort uint32, bind string) (v1.ForwardListener, error) {
	var opts []v1.ListenOption
	if bind != "" {
		opts = append(opts, v1.WithBindAddress(bind))
	}
	l, err := c.sdk().TCP().Listen(ctx, workspace, sandbox, remotePort, localPort, opts...)
	if err != nil {
		return nil, Classify(err, "sandbox", sandbox)
	}
	return l, nil
}

// workspaceScope builds the WorkspaceSelector oneof for raw proto requests.
// Callers must validate workspace before calling; this is a thin builder.
func workspaceScope(workspace string) *dm.WorkspaceSelector {
	if workspace == "" {
		panic("workspaceScope called with empty workspace — caller must validate")
	}
	return &dm.WorkspaceSelector{
		Selection: &dm.WorkspaceSelector_Workspace{Workspace: workspace},
	}
}

// deletionCompleted maps a proto DeletionOutcome to a bool. Completed,
// Accepted (async), and AlreadyAbsent all mean the server acknowledged the
// delete — the sandbox is gone or will be gone shortly.
func deletionCompleted(o pb.DeletionOutcome) bool {
	switch o {
	case pb.DeletionOutcome_DELETION_OUTCOME_COMPLETED,
		pb.DeletionOutcome_DELETION_OUTCOME_ACCEPTED,
		pb.DeletionOutcome_DELETION_OUTCOME_ALREADY_ABSENT:
		return true
	default:
		return false
	}
}
