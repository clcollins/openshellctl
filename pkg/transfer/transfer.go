// Package transfer implements upload/download/connect over the sandbox SSH
// server, tunneled through the gateway (gw.SSHTunnel). It is a faithful port of
// the upstream CLI's ssh.rs behaviours (tar-over-SSH upload/download, SSH shell
// connect) done in-process with golang.org/x/crypto/ssh rather than by shelling
// out to the `ssh` binary. See spec §5.7.
//
// The sandbox SSH server accepts `none` auth
// (crates/openshell-supervisor-process/src/ssh.rs:506-508); host keys are
// ephemeral per sandbox and the tunnel itself is authorised by the gateway
// session token, so the trust model matches the CLI's StrictHostKeyChecking=no.
package transfer

import (
	"context"
	"io/fs"
	"time"

	"github.com/openshift-online/openshellctl/pkg/gateway"
)

// Client performs file transfer and interactive connect against a sandbox.
type Client interface {
	// Upload copies a local file or directory to dest inside the sandbox. When
	// gitignore is true and local is a directory in a git repo, gitignored files
	// are excluded (gitignore-compatible, not git-index-identical). report, when
	// non-nil, receives progress lines (without trailing newline).
	Upload(ctx context.Context, workspace, sandbox, local, dest string, gitignore bool, report func(string)) error
	// Download copies a remote file or directory to dest on the local host.
	Download(ctx context.Context, workspace, sandbox, remote, dest string, report func(string)) error
	// Connect attaches an interactive session to the sandbox, returning the
	// remote exit code. When command is non-empty, an SSH exec request runs the
	// command; when empty, a shell request gives the default interactive session.
	// The Ctrl-P Ctrl-Q chord detaches (returns 0). term supplies the local
	// terminal integration (raw mode, size, resize notifications); pass a
	// nopTerminal for non-interactive callers.
	Connect(ctx context.Context, workspace, sandbox string, tty bool, term Terminal, command ...string) (exitCode int, err error)
	// ConnectMainProcess attaches to the sandbox's main process via the SSH
	// subsystem "openshell-main", returning the main process exit code. Unlike
	// Connect, it never exec's a command (the main process was already started at
	// sandbox boot via spec.command) and does not spawn a login shell. This is the
	// correct attach path for 0.1.x gateways; using Connect with a command or the
	// shell request would spawn a second process or a new login shell instead of
	// attaching to the main process.
	ConnectMainProcess(ctx context.Context, workspace, sandbox string, tty bool, term Terminal) (exitCode int, err error)
}

// Clock abstracts time for tests (keepalive scheduling).
type Clock interface {
	Now() time.Time
	NewTicker(d time.Duration) (<-chan time.Time, func())
}

// New builds a Client. fsys is the source filesystem used for uploads (an
// os.DirFS-style root); pass osFS() for production. clock schedules keepalives.
func New(gw gateway.Gateway, fsys fs.FS, clock Clock) Client {
	return &client{gw: gw, fsys: fsys, clock: clock}
}

type client struct {
	gw    gateway.Gateway
	fsys  fs.FS
	clock Clock
}
