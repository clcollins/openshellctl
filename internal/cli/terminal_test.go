package cli

import (
	"bytes"
	"testing"
)

func TestTerminalResetSequence(t *testing.T) {
	got := terminalResetSequence()
	want := "\033[?1000l\033[?1002l\033[?1003l\033[?1006l\033[?1004l\033[?2004l\033[?1049l\033[?25h"
	if string(got) != want {
		t.Errorf("terminalResetSequence():\ngot  %q\nwant %q", got, want)
	}
}

func TestRestoreWith_NonTTY_WritesNothing(t *testing.T) {
	var restored bool
	var buf bytes.Buffer
	restoreWith(func() { restored = true }, &buf, false)
	if !restored {
		t.Error("restoreTTY was not called")
	}
	if buf.Len() != 0 {
		t.Errorf("expected no output for non-TTY, got %q", buf.String())
	}
}

func TestRestoreWith_TTY_WritesResetAfterRestore(t *testing.T) {
	type event struct{ tag string }
	var events []event
	var buf trackingWriter
	buf.onWrite = func(p []byte) { events = append(events, event{"write:" + string(p)}) }
	restoreWith(func() { events = append(events, event{"restore"}) }, &buf, true)

	if len(events) < 2 {
		t.Fatalf("expected at least 2 events (restore + write), got %d: %v", len(events), events)
	}
	if events[0].tag != "restore" {
		t.Errorf("expected restore to precede write, got first event %q", events[0].tag)
	}
	want := "write:" + string(terminalResetSequence())
	if events[1].tag != want {
		t.Errorf("second event:\ngot  %q\nwant %q", events[1].tag, want)
	}
}

// trackingWriter records writes via an optional hook while also buffering.
type trackingWriter struct {
	bytes.Buffer
	onWrite func([]byte)
}

func (w *trackingWriter) Write(p []byte) (int, error) {
	if w.onWrite != nil {
		w.onWrite(p)
	}
	return w.Buffer.Write(p)
}
