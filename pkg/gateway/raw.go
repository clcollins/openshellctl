package gateway

import (
	"context"

	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"
	"google.golang.org/grpc"
)

// serverStream adapts a grpc.ServerStreamingClient[T] to our Stream[T].
type serverStream[T any] struct {
	sc     grpc.ServerStreamingClient[T]
	cancel context.CancelFunc
}

func (s *serverStream[T]) Recv() (*T, error) { return s.sc.Recv() }
func (s *serverStream[T]) Close() {
	if s.cancel != nil {
		s.cancel()
	}
}

// CreateSandboxRaw creates via the raw stub (for fields the SDK spec cannot
// express, e.g. bare --gpu). Returns the created sandbox proto.
func (c *client) CreateSandboxRaw(ctx context.Context, req *pb.CreateSandboxRequest) (*pb.Sandbox, error) {
	var out *pb.Sandbox
	err := withAuthRetry(ctx, c.auth, func(ctx context.Context) error {
		resp, err := c.conn.Raw.CreateSandbox(ctx, req)
		if err != nil {
			return Classify(err, "sandbox", req.GetName())
		}
		out = resp.GetSandbox()
		return nil
	})
	return out, err
}

// WatchSandbox opens the raw watch stream (status + logs + events + warnings),
// which the SDK's status-only Watch cannot express. Auth retry applies to the
// initial open only.
func (c *client) WatchSandbox(ctx context.Context, req *pb.WatchSandboxRequest) (Stream[*pb.SandboxStreamEvent], error) {
	streamCtx, cancel := context.WithCancel(ctx)
	var sc grpc.ServerStreamingClient[pb.SandboxStreamEvent]
	err := withAuthRetry(streamCtx, c.auth, func(ctx context.Context) error {
		s, err := c.conn.Raw.WatchSandbox(ctx, req)
		if err != nil {
			return Classify(err, "sandbox", req.GetSandbox())
		}
		sc = s
		return nil
	})
	if err != nil {
		cancel()
		return nil, err
	}
	return &serverStream[pb.SandboxStreamEvent]{sc: sc, cancel: cancel}, nil
}

// ExecSandbox opens the raw one-shot exec stream (with timeout/tty/stdin the SDK
// cannot express).
func (c *client) ExecSandbox(ctx context.Context, req *pb.ExecSandboxRequest) (Stream[*pb.ExecSandboxEvent], error) {
	streamCtx, cancel := context.WithCancel(ctx)
	var sc grpc.ServerStreamingClient[pb.ExecSandboxEvent]
	err := withAuthRetry(streamCtx, c.auth, func(ctx context.Context) error {
		s, err := c.conn.Raw.ExecSandbox(ctx, req)
		if err != nil {
			return Classify(err, "sandbox", req.GetSandbox())
		}
		sc = s
		return nil
	})
	if err != nil {
		cancel()
		return nil, err
	}
	return &serverStream[pb.ExecSandboxEvent]{sc: sc, cancel: cancel}, nil
}

// bidiStream adapts a grpc.BidiStreamingClient to our BidiStream.
type bidiStream struct {
	bc     grpc.BidiStreamingClient[pb.ExecSandboxInput, pb.ExecSandboxEvent]
	cancel context.CancelFunc
}

func (b *bidiStream) Send(in *pb.ExecSandboxInput) error  { return b.bc.Send(in) }
func (b *bidiStream) Recv() (*pb.ExecSandboxEvent, error) { return b.bc.Recv() }
func (b *bidiStream) CloseSend() error {
	err := b.bc.CloseSend()
	if b.cancel != nil {
		// CloseSend does not tear down the RPC; the caller reads until EOF, then
		// context cancel releases resources. Cancel is deferred to Recv EOF by
		// the caller, so we do not cancel here.
		_ = err
	}
	return err
}

// ExecSandboxInteractive opens the raw bidirectional interactive-exec stream.
func (c *client) ExecSandboxInteractive(ctx context.Context) (BidiStream, error) {
	streamCtx, cancel := context.WithCancel(ctx)
	bc, err := c.conn.Raw.ExecSandboxInteractive(streamCtx)
	if err != nil {
		cancel()
		return nil, Classify(err, "", "")
	}
	return &bidiStream{bc: bc, cancel: cancel}, nil
}
