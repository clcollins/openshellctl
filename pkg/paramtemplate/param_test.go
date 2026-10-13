package paramtemplate

import (
	"errors"
	"strings"
	"testing"
)

func TestParseParam(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    Param
		wantErr bool
	}{
		{"simple", "NAME=X", Param{Key: "NAME", Value: "X"}, false},
		{"value contains comma", "PROMPT=a, b: c", Param{Key: "PROMPT", Value: "a, b: c"}, false},
		{"value contains equals", "A=b=c", Param{Key: "A", Value: "b=c"}, false},
		{"empty value is allowed", "A=", Param{Key: "A", Value: ""}, false},
		{"underscore and digits in key", "A_1=x", Param{Key: "A_1", Value: "x"}, false},
		{"leading underscore key", "_A=x", Param{Key: "_A", Value: "x"}, false},

		{"no equals sign", "NAME", Param{}, true},
		{"empty key", "=x", Param{}, true},
		{"key starts with digit", "1A=x", Param{}, true},
		{"key contains hyphen", "A-B=x", Param{}, true},
		{"key contains space", "A B=x", Param{}, true},
		{"empty raw", "", Param{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseParam(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseParam(%q) = %+v, nil; want error", tt.raw, got)
				}
				var malformed *MalformedParamError
				if !errors.As(err, &malformed) {
					t.Errorf("ParseParam(%q) error type = %T, want *MalformedParamError", tt.raw, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseParam(%q) unexpected error: %v", tt.raw, err)
			}
			if got != tt.want {
				t.Errorf("ParseParam(%q) = %+v, want %+v", tt.raw, got, tt.want)
			}
		})
	}
}

// TestParseParam_NeverLeaksValueIntoError is the security requirement this
// card calls out explicitly: "param values must never be logged, including
// in verbose mode; log key names only." A malformed entry whose key portion
// fails validation must not echo the value half of "KEY=VALUE" into the
// returned error's message — only the (invalid) key may appear.
func TestParseParam_NeverLeaksValueIntoError(t *testing.T) {
	_, err := ParseParam("1BAD=supersecretvalue")
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "supersecretvalue") {
		t.Errorf("error message leaks the value: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "1BAD") {
		t.Errorf("error message should still name the invalid key, got: %q", err.Error())
	}
}

// TestParseParam_NoEqualsSign_NeverEchoesRawText covers the other malformed
// shape: no '=' at all. Since there is no way to tell a mistyped key from an
// accidentally bare value in this shape, the raw text must not be echoed
// either.
func TestParseParam_NoEqualsSign_NeverEchoesRawText(t *testing.T) {
	_, err := ParseParam("supersecretvalue")
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "supersecretvalue") {
		t.Errorf("error message leaks the raw entry: %q", err.Error())
	}
}

func TestMalformedParamError_Message(t *testing.T) {
	keyErr := &MalformedParamError{Key: "1BAD", HasEquals: true, Source: "--param"}
	want := `--param: invalid parameter key "1BAD", expected KEY matching [A-Za-z_][A-Za-z0-9_]*`
	if got := keyErr.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}

	keyErrLine := &MalformedParamError{Key: "1BAD", HasEquals: true, Source: "params.txt", Line: 3}
	wantLine := `params.txt:3: invalid parameter key "1BAD", expected KEY matching [A-Za-z_][A-Za-z0-9_]*`
	if got := keyErrLine.Error(); got != wantLine {
		t.Errorf("Error() = %q, want %q", got, wantLine)
	}

	noEqErr := &MalformedParamError{HasEquals: false, Source: "--param"}
	wantNoEq := `--param: malformed parameter entry, expected KEY=VALUE (no '=' found)`
	if got := noEqErr.Error(); got != wantNoEq {
		t.Errorf("Error() = %q, want %q", got, wantNoEq)
	}
}
