package sandbox

import (
	"context"
	"errors"
	"fmt"
	"time"

	types "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"

	"github.com/openshift-online/openshellctl/pkg/gateway"
	"github.com/openshift-online/openshellctl/pkg/gatewayconfig"
)

// DeleteRequest is the input to Delete.
type DeleteRequest struct {
	Workspace   string
	Names       []string
	All         bool
	Wait        bool
	WaitTimeout time.Duration
}

// DeleteOutcome reports the result for one sandbox.
type DeleteOutcome struct {
	Name    string
	Deleted bool
}

// ErrNothingToDelete is returned by --all when no sandboxes exist. The CLI
// renders the verbatim upstream message "No sandboxes to delete." for it.
var ErrNothingToDelete = errors.New("no sandboxes to delete")

// NothingToDeleteMessage is the verbatim upstream user-facing string.
const NothingToDeleteMessage = "No sandboxes to delete."

// ErrDeleteTimeout is returned when --wait times out.
type ErrDeleteTimeout struct {
	Name  string
	After time.Duration
}

func (e *ErrDeleteTimeout) Error() string {
	return fmt.Sprintf("timed out after %s waiting for sandbox %q to be deleted", e.After, e.Name)
}

// Delete deletes sandboxes (run.rs:2350-2414). With All it lists the workspace
// (limit 1000); empty → ErrNothingToDelete. Per name it calls DeleteSandbox;
// on deleted==true it clears last_sandbox and reports. The first RPC error
// aborts. Wait (extension) polls GetSandbox every 500ms until NotFound or
// timeout. cfgw may be nil (no last_sandbox clearing).
func Delete(ctx context.Context, gw gateway.Gateway, cfgw gatewayconfig.Writer, gatewayName string, r DeleteRequest, report func(DeleteOutcome)) error {
	if r.Workspace == "" {
		return ErrEmptyWorkspace
	}
	names := r.Names
	if r.All {
		list, err := gw.ListSandboxes(ctx, r.Workspace, types.ListOptions{})
		if err != nil {
			return err
		}
		if len(list) == 0 {
			return ErrNothingToDelete
		}
		names = names[:0]
		for _, s := range list {
			names = append(names, s.Name)
		}
	}

	for _, name := range names {
		deleted, err := gw.DeleteSandbox(ctx, r.Workspace, name)
		if err != nil {
			return err
		}
		if deleted && cfgw != nil && gatewayName != "" {
			_ = gatewayconfig.ClearLastSandboxIfMatches(cfgw, gatewayName, r.Workspace, name)
		}
		if report != nil {
			report(DeleteOutcome{Name: name, Deleted: deleted})
		}
		if r.Wait && deleted {
			if err := waitGone(ctx, gw, r.Workspace, name, r.WaitTimeout, waitGoneDeps{}); err != nil {
				return err
			}
		}
	}
	return nil
}

// waitGoneDeps lets tests replace both "now" and the poll cadence without
// real sleeps. Every field is optional: a zero-value waitGoneDeps reproduces
// the original real-ticker, real-clock behavior.
type waitGoneDeps struct {
	Clock Clock // nil -> time.Now (pkg/sandbox's existing func() time.Time type, watch.go)
	// Tick, when set, drives polling directly instead of a real ticker — the
	// test seam. Production (nil) builds a real time.NewTicker(Interval or
	// 500ms).
	Tick     <-chan time.Time
	Interval time.Duration // only used when Tick is nil; 0 -> 500ms
}

// waitGone polls GetSandbox until NotFound or timeout. Replaces the old
// waitForDeletion, which used raw time.Now()/time.NewTicker with no test
// seam at all — this version is injectable so "gone on the Nth poll",
// "timeout", and "context cancel" are all testable without a real sleep.
func waitGone(ctx context.Context, gw gateway.Gateway, workspace, name string, timeout time.Duration, d waitGoneDeps) error {
	clock := d.Clock
	if clock == nil {
		clock = time.Now
	}
	if timeout == 0 {
		timeout = 5 * time.Minute
	}

	tick := d.Tick
	if tick == nil {
		interval := d.Interval
		if interval == 0 {
			interval = 500 * time.Millisecond
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		tick = ticker.C
	}

	deadline := clock().Add(timeout)
	for {
		_, err := gw.GetSandbox(ctx, workspace, name)
		var nf *gateway.NotFoundError
		if errors.As(err, &nf) {
			return nil
		}
		if err != nil {
			// A real RPC error (Unauthenticated, Unavailable, ...) is not
			// "still there" — propagate it immediately instead of silently
			// retrying it for the full timeout window and surfacing a
			// misleading *ErrDeleteTimeout once the deadline passes. This is
			// exactly the "swallow the real error, confuse the caller"
			// failure mode --replace exists to eliminate.
			return err
		}
		if clock().After(deadline) {
			return &ErrDeleteTimeout{Name: name, After: timeout}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick:
		}
	}
}
