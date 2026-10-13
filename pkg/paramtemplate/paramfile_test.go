package paramtemplate

import (
	"errors"
	"reflect"
	"testing"
)

func TestParseParamFileContent(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		data    string
		want    map[string]string
		wantErr bool
	}{
		{
			name: "basic key=value lines",
			path: "params.txt",
			data: "NAME=X\nPROMPT=a, b: c\n",
			want: map[string]string{"NAME": "X", "PROMPT": "a, b: c"},
		},
		{
			name: "blank lines ignored",
			path: "params.txt",
			data: "NAME=X\n\n\nPROMPT=hi\n",
			want: map[string]string{"NAME": "X", "PROMPT": "hi"},
		},
		{
			name: "comment lines ignored",
			path: "params.txt",
			data: "# a comment\nNAME=X\n# another\nPROMPT=hi\n",
			want: map[string]string{"NAME": "X", "PROMPT": "hi"},
		},
		{
			name: "whitespace-only line ignored",
			path: "params.txt",
			data: "NAME=X\n   \nPROMPT=hi\n",
			want: map[string]string{"NAME": "X", "PROMPT": "hi"},
		},
		{
			name: "no trailing newline",
			path: "params.txt",
			data: "NAME=X",
			want: map[string]string{"NAME": "X"},
		},
		{
			name: "empty file",
			path: "params.txt",
			data: "",
			want: map[string]string{},
		},
		{
			name: "duplicate key within file, last wins",
			path: "params.txt",
			data: "NAME=X\nNAME=Y\n",
			want: map[string]string{"NAME": "Y"},
		},
		{
			name:    "malformed line reports source and line number",
			path:    "params.txt",
			data:    "NAME=X\nNOTAKEYVALUE\n",
			wantErr: true,
		},
		{
			name:    "invalid key in file",
			path:    "params.txt",
			data:    "1BAD=x\n",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseParamFileContent(tt.path, []byte(tt.data))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseParamFileContent(%q) = %v, nil; want error", tt.data, got)
				}
				var malformed *MalformedParamError
				if !errors.As(err, &malformed) {
					t.Errorf("error type = %T, want *MalformedParamError", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseParamFileContent(%q) unexpected error: %v", tt.data, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseParamFileContent(%q) = %#v, want %#v", tt.data, got, tt.want)
			}
		})
	}
}

func TestParseParamFileContent_MalformedLineNumber(t *testing.T) {
	_, err := ParseParamFileContent("params.txt", []byte("NAME=X\nBADLINE\n"))
	if err == nil {
		t.Fatal("expected error")
	}
	var malformed *MalformedParamError
	if !errors.As(err, &malformed) {
		t.Fatalf("error type = %T, want *MalformedParamError", err)
	}
	if malformed.Line != 2 {
		t.Errorf("Line = %d, want 2", malformed.Line)
	}
	if malformed.Source != "params.txt" {
		t.Errorf("Source = %q, want %q", malformed.Source, "params.txt")
	}
}
