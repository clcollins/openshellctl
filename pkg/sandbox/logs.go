package sandbox

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	types "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"
	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/openshift-online/openshellctl/pkg/gateway"
	"github.com/openshift-online/openshellctl/pkg/output"
)

// ParseDurationToMs parses a "<n><unit>" duration into milliseconds, matching
// common.rs:723-746: the last rune is the unit (s|m|h), the rest is an i64. All
// error strings are verbatim.
func ParseDurationToMs(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration string")
	}
	r := []rune(s)
	unit := string(r[len(r)-1])
	numStr := string(r[:len(r)-1])

	var n int64
	if _, err := fmt.Sscanf(numStr, "%d", &n); err != nil || !isAllDigitsOrSign(numStr) {
		return 0, fmt.Errorf("invalid duration: %s (expected e.g. 5m, 1h, 30s)", s)
	}

	switch unit {
	case "s":
		return n * 1000, nil
	case "m":
		return n * 60000, nil
	case "h":
		return n * 3600000, nil
	default:
		return 0, fmt.Errorf("unknown duration unit: %s (use s, m, or h)", unit)
	}
}

// isAllDigitsOrSign reports whether s is a valid base-10 integer literal (an
// optional leading '-' then digits). Guards Sscanf's lenient parsing so "5x2"
// or "" are rejected as invalid durations.
func isAllDigitsOrSign(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		if c == '-' && i == 0 {
			continue
		}
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != "-"
}

// ComputeSinceMs computes the since-ms filter (run.rs:6943-6955): now-dur when a
// --since was given, else 0.
func ComputeSinceMs(nowMs, durMs int64, hasSince bool) int64 {
	if !hasSince {
		return 0
	}
	return nowMs - durMs
}

// FilterSources drops the literal "all" entry (run.rs:6937-6941); "all" means no
// source filter. Returns nil when the result is empty.
func FilterSources(sources []string) []string {
	var out []string
	for _, s := range sources {
		if s != "all" {
			out = append(out, s)
		}
	}
	return out
}

// LogsRequest is the resolved input for a logs command.
type LogsRequest struct {
	Workspace string
	Name      string
	Lines     uint32
	Tail      bool
	SinceMs   int64
	Sources   []string // "all" already filtered out
	Level     string   // upper-cased
}

// buildWatchLogsRequest builds the raw WatchSandboxRequest for --tail
// (run.rs:6957-6983). Pure.
func buildWatchLogsRequest(sandboxID string, r *LogsRequest) *pb.WatchSandboxRequest {
	req := &pb.WatchSandboxRequest{
		Sandbox:        sandboxID,
		FollowStatus:   false,
		FollowLogs:     true,
		FollowEvents:   false,
		LogTailLines:   r.Lines,
		EventTail:      0,
		StopOnTerminal: false,
		LogSources:     r.Sources,
		LogMinLevel:    r.Level,
	}
	if r.SinceMs > 0 {
		req.SinceTime = timestamppb.New(time.UnixMilli(r.SinceMs))
	}
	return req
}

// logLineFromProto converts a raw SandboxLogLine to the SDK types.LogLine that
// output.FormatLogLine renders. Pure.
func logLineFromProto(l *pb.SandboxLogLine) types.LogLine {
	var ts time.Time
	if et := l.GetEventTime(); et != nil {
		ts = et.AsTime()
	}
	return types.LogLine{
		Timestamp: ts,
		Level:     l.GetLevel(),
		Target:    l.GetTarget(),
		Message:   l.GetMessage(),
		Source:    l.GetSource(),
		Fields:    l.GetFields(),
	}
}

// bufferTruncatedWarning returns the verbatim stderr warning (run.rs:7000-7005)
// when since filtering may be incomplete, or "" when no warning is due.
func bufferTruncatedWarning(sinceMs int64, bufferTotal uint32) string {
	if sinceMs > 0 && bufferTotal > 0 {
		return fmt.Sprintf("Warning: log buffer contains only the last %d lines; --since results may be incomplete.", bufferTotal)
	}
	return ""
}

// LogsDeps are the dependencies for Logs.
type LogsDeps struct {
	GW     gateway.Gateway
	Stdout io.Writer
	Stderr io.Writer
}

// Logs prints sandbox logs. With Tail it opens the raw watch stream and prints
// Log payloads live; otherwise it fetches a bounded window via GetLogs and
// prints it, emitting the buffer-truncated warning when applicable (run.rs:
// 6911-7013, A.9). The caller has resolved the sandbox id/name and normalised
// the request (since computed, sources filtered, level upper-cased).
func Logs(ctx context.Context, d LogsDeps, sandboxID string, r *LogsRequest) error {
	if r.Tail {
		return tailLogs(ctx, d, sandboxID, r)
	}
	return fetchLogs(ctx, d, r)
}

// tailLogs streams live logs, printing only Log payloads (run.rs:6957-6983).
func tailLogs(ctx context.Context, d LogsDeps, sandboxID string, r *LogsRequest) error {
	stream, err := d.GW.WatchSandbox(ctx, buildWatchLogsRequest(sandboxID, r))
	if err != nil {
		return err
	}
	defer stream.Close()
	for {
		ev, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if log := ev.GetLog(); log != nil {
			if _, werr := fmt.Fprintln(d.Stdout, output.FormatLogLine(logLineFromProto(log))); werr != nil {
				return werr
			}
		}
	}
}

// fetchLogs fetches and prints a bounded window (run.rs:6984-7009).
func fetchLogs(ctx context.Context, d LogsDeps, r *LogsRequest) error {
	opts := []types.LogOption{
		types.WithLogLines(r.Lines),
		types.WithLogMinLevel(r.Level),
	}
	if r.SinceMs > 0 {
		opts = append(opts, types.WithLogSince(time.UnixMilli(r.SinceMs)))
	}
	if len(r.Sources) > 0 {
		opts = append(opts, types.WithLogSources(r.Sources...))
	}

	res, err := d.GW.GetLogs(ctx, r.Workspace, r.Name, opts...)
	if err != nil {
		return err
	}

	if warn := bufferTruncatedWarning(r.SinceMs, res.BufferTotal); warn != "" {
		_, _ = fmt.Fprintln(d.Stderr, warn)
	}
	for i := range res.Lines {
		if _, werr := fmt.Fprintln(d.Stdout, output.FormatLogLine(res.Lines[i])); werr != nil {
			return werr
		}
	}
	return nil
}
