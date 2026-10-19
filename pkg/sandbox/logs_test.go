package sandbox

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	types "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"
	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"
	"go.uber.org/mock/gomock"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/openshift-online/openshellctl/pkg/gateway/mock"
)

func TestParseDurationToMs(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		wantErr string
	}{
		{"30s", 30000, ""},
		{"5m", 300000, ""},
		{"1h", 3600000, ""},
		{"  10m  ", 600000, ""},
		{"-5m", -300000, ""}, // negative offsets are accepted by the parser (i64)
		{"-s", 0, "invalid duration: -s (expected e.g. 5m, 1h, 30s)"},
		{"", 0, "empty duration string"},
		{"5x", 0, "unknown duration unit: x (use s, m, or h)"},
		{"abcm", 0, "invalid duration: abcm (expected e.g. 5m, 1h, 30s)"},
		{"m", 0, "invalid duration: m (expected e.g. 5m, 1h, 30s)"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := ParseDurationToMs(c.in)
			if c.wantErr != "" {
				if err == nil || err.Error() != c.wantErr {
					t.Fatalf("err = %v, want %q", err, c.wantErr)
				}
				return
			}
			if err != nil || got != c.want {
				t.Errorf("got %d err %v, want %d", got, err, c.want)
			}
		})
	}
}

func TestComputeSinceMs(t *testing.T) {
	if got := ComputeSinceMs(1000, 300, false); got != 0 {
		t.Errorf("no since = %d, want 0", got)
	}
	if got := ComputeSinceMs(1000, 300, true); got != 700 {
		t.Errorf("with since = %d, want 700", got)
	}
}

func TestFilterSources(t *testing.T) {
	if got := FilterSources([]string{"all"}); got != nil {
		t.Errorf("all-only = %v, want nil", got)
	}
	got := FilterSources([]string{"gateway", "all", "sandbox"})
	if strings.Join(got, ",") != "gateway,sandbox" {
		t.Errorf("filtered = %v", got)
	}
}

func TestBufferTruncatedWarning(t *testing.T) {
	if bufferTruncatedWarning(0, 100) != "" {
		t.Error("no since → no warning")
	}
	if bufferTruncatedWarning(500, 0) != "" {
		t.Error("no buffer → no warning")
	}
	want := "Warning: log buffer contains only the last 42 lines; --since results may be incomplete."
	if got := bufferTruncatedWarning(500, 42); got != want {
		t.Errorf("warning = %q", got)
	}
}

func TestBuildWatchLogsRequest(t *testing.T) {
	req := buildWatchLogsRequest("sb-1", &LogsRequest{
		Lines:   50,
		SinceMs: 12345,
		Sources: []string{"gateway"},
		Level:   "WARN",
	})
	if req.Sandbox != "sb-1" || !req.FollowLogs || req.FollowStatus || req.FollowEvents {
		t.Errorf("follow flags: %+v", req)
	}
	if req.LogTailLines != 50 || req.LogMinLevel != "WARN" {
		t.Errorf("log fields: %+v", req)
	}
	if req.SinceTime == nil || !req.SinceTime.AsTime().Equal(time.UnixMilli(12345)) {
		t.Errorf("since time: %+v", req.SinceTime)
	}
	if len(req.LogSources) != 1 || req.LogSources[0] != "gateway" {
		t.Errorf("sources: %+v", req.LogSources)
	}
}

func TestBuildWatchLogsRequest_NoSince(t *testing.T) {
	req := buildWatchLogsRequest("sb-1", &LogsRequest{Lines: 50})
	if req.SinceTime != nil {
		t.Errorf("sinceMs=0 should produce nil SinceTime, got %+v", req.SinceTime)
	}
}

func TestLogLineFromProto(t *testing.T) {
	et := timestamppb.New(time.UnixMilli(1000))
	l := logLineFromProto(&pb.SandboxLogLine{
		EventTime: et,
		Level:     "INFO",
		Target:    "svc",
		Message:   "hello",
		Source:    "sandbox",
		Fields:    map[string]string{"k": "v"},
	})
	if !l.Timestamp.Equal(time.UnixMilli(1000)) || l.Level != "INFO" || l.Message != "hello" || l.Source != "sandbox" {
		t.Errorf("converted = %+v", l)
	}
}

func TestLogLineFromProto_NilEventTime(t *testing.T) {
	l := logLineFromProto(&pb.SandboxLogLine{Level: "WARN", Message: "m"})
	if !l.Timestamp.IsZero() {
		t.Errorf("nil EventTime should produce zero time, got %v", l.Timestamp)
	}
}

// logStream is a scripted watch stream carrying only Log payloads.
func logEvent(msg string) *pb.SandboxStreamEvent {
	return &pb.SandboxStreamEvent{Payload: &pb.SandboxStreamEvent_Log{
		Log: &pb.SandboxLogLine{EventTime: timestamppb.New(time.UnixMilli(1000)), Level: "INFO", Target: "t", Message: msg, Source: "sandbox"},
	}}
}

func TestLogs_Tail(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gw.EXPECT().WatchSandbox(gomock.Any(), gomock.Any()).Return(&scriptedStream{events: []*pb.SandboxStreamEvent{
		logEvent("line one"),
		phaseEvent(pb.SandboxPhase_SANDBOX_PHASE_READY), // ignored (not a Log payload)
		logEvent("line two"),
	}}, nil)

	var out bytes.Buffer
	err := Logs(context.Background(), LogsDeps{GW: gw, Stdout: &out, Stderr: io.Discard},
		"sb-1", &LogsRequest{Tail: true, Lines: 200})
	if err != nil {
		t.Fatalf("logs tail: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "line one") || !strings.Contains(got, "line two") {
		t.Errorf("stdout = %q", got)
	}
	if strings.Count(got, "\n") != 2 {
		t.Errorf("expected exactly 2 log lines, got %q", got)
	}
}

func TestLogs_FetchWithBufferWarning(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gw.EXPECT().GetLogs(gomock.Any(), "default", "demo", gomock.Any()).Return(&types.LogResult{
		Lines: []types.LogLine{
			{Timestamp: time.UnixMilli(1000), Level: "INFO", Target: "t", Message: "a", Source: "gateway"},
		},
		BufferTotal: 500,
	}, nil)

	var out, errOut bytes.Buffer
	err := Logs(context.Background(), LogsDeps{GW: gw, Stdout: &out, Stderr: &errOut},
		"sb-1", &LogsRequest{Workspace: "default", Name: "demo", Lines: 200, SinceMs: 999})
	if err != nil {
		t.Fatalf("logs fetch: %v", err)
	}
	if !strings.Contains(out.String(), "a") {
		t.Errorf("stdout = %q", out.String())
	}
	if !strings.Contains(errOut.String(), "log buffer contains only the last 500 lines") {
		t.Errorf("stderr warning missing: %q", errOut.String())
	}
}

func TestLogs_FetchNoWarningWithoutSince(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gw.EXPECT().GetLogs(gomock.Any(), "default", "demo", gomock.Any()).Return(&types.LogResult{
		Lines:       nil,
		BufferTotal: 500,
	}, nil)

	var errOut bytes.Buffer
	err := Logs(context.Background(), LogsDeps{GW: gw, Stdout: io.Discard, Stderr: &errOut},
		"sb-1", &LogsRequest{Workspace: "default", Name: "demo", Lines: 200, SinceMs: 0})
	if err != nil {
		t.Fatalf("logs fetch: %v", err)
	}
	if errOut.Len() != 0 {
		t.Errorf("no since → no warning, got %q", errOut.String())
	}
}
