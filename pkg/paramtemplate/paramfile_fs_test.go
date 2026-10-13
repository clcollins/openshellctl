package paramtemplate

import (
	"errors"
	"reflect"
	"testing"
	"testing/fstest"
)

func TestLoadParamFile(t *testing.T) {
	t.Run("reads and parses an existing file", func(t *testing.T) {
		fsys := fstest.MapFS{
			"params.txt": {Data: []byte("NAME=X\nPROMPT=hi\n")},
		}
		got, err := LoadParamFile(fsys, "params.txt")
		if err != nil {
			t.Fatalf("LoadParamFile: %v", err)
		}
		want := map[string]string{"NAME": "X", "PROMPT": "hi"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("LoadParamFile() = %#v, want %#v", got, want)
		}
	})

	t.Run("missing file is a typed, wrapped error", func(t *testing.T) {
		fsys := fstest.MapFS{}
		_, err := LoadParamFile(fsys, "missing.txt")
		if err == nil {
			t.Fatal("expected an error")
		}
		var unreadable *ParamFileUnreadableError
		if !errors.As(err, &unreadable) {
			t.Fatalf("error type = %T, want *ParamFileUnreadableError", err)
		}
		if unreadable.Path != "missing.txt" {
			t.Errorf("Path = %q, want %q", unreadable.Path, "missing.txt")
		}
		if unreadable.Unwrap() == nil {
			t.Error("Unwrap() = nil, want the underlying fs error")
		}
	})

	t.Run("malformed content surfaces a MalformedParamError naming the file", func(t *testing.T) {
		fsys := fstest.MapFS{
			"params.txt": {Data: []byte("NOTAKEYVALUE\n")},
		}
		_, err := LoadParamFile(fsys, "params.txt")
		var malformed *MalformedParamError
		if !errors.As(err, &malformed) {
			t.Fatalf("error type = %T, want *MalformedParamError", err)
		}
		if malformed.Source != "params.txt" {
			t.Errorf("Source = %q, want %q", malformed.Source, "params.txt")
		}
	})
}

func TestParamFileUnreadableError_Message(t *testing.T) {
	err := &ParamFileUnreadableError{Path: "params.txt", Err: errors.New("no such file")}
	want := `reading --param-file "params.txt": no such file`
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestUnresolvedParamsError_Message(t *testing.T) {
	err := &UnresolvedParamsError{Keys: []string{"NAME", "PROMPT"}}
	want := "unresolved parameter reference(s): NAME, PROMPT"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestUnknownParamsError_Message(t *testing.T) {
	err := &UnknownParamsError{Keys: []string{"EXTRA"}}
	want := "parameter(s) not referenced by manifest: EXTRA (use --ignore-unknown-parameters to allow)"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}
