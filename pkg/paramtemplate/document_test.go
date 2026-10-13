package paramtemplate

import (
	"reflect"
	"testing"

	yaml "go.yaml.in/yaml/v3"
)

func TestParseDocument(t *testing.T) {
	t.Run("valid yaml", func(t *testing.T) {
		doc, err := ParseDocument([]byte("a: b\n"))
		if err != nil {
			t.Fatalf("ParseDocument: %v", err)
		}
		if doc == nil {
			t.Fatal("ParseDocument returned nil node")
		}
	})
	t.Run("invalid yaml", func(t *testing.T) {
		_, err := ParseDocument([]byte("a: [unterminated\n"))
		if err == nil {
			t.Fatal("expected a parse error")
		}
	})
}

func TestCollectReferences(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want []string
	}{
		{
			name: "no references",
			yaml: "metadata:\n  name: fixed\nspec:\n  image: python\n",
			want: nil,
		},
		{
			name: "single reference",
			yaml: "metadata:\n  name: ${NAME}\n",
			want: []string{"NAME"},
		},
		{
			name: "multiple references deduplicated and sorted",
			yaml: "metadata:\n  name: ${NAME}\nspec:\n  command:\n    - claude\n    - '${PROMPT}'\n  label: ${NAME}-x\n",
			want: []string{"NAME", "PROMPT"},
		},
		{
			name: "multiple references embedded in one scalar",
			yaml: "metadata:\n  name: ${A}-${B}\n",
			want: []string{"A", "B"},
		},
		{
			name: "map keys are not scanned for references",
			yaml: "spec:\n  env:\n    '${NOTAKEY}': value\n",
			want: nil,
		},
		{
			name: "escaped $${KEY} is not a reference",
			yaml: "metadata:\n  name: '$${ESCAPED}'\n",
			want: nil,
		},
		{
			name: "bare $KEY without braces is not a reference",
			yaml: "metadata:\n  name: $NOTAREF\n",
			want: nil,
		},
		{
			name: "non-string scalars are not scanned",
			yaml: "spec:\n  resources:\n    gpu:\n      count: 1\n  tty: true\n",
			want: nil,
		},
		{
			name: "empty document has no references",
			yaml: "",
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, err := ParseDocument([]byte(tt.yaml))
			if err != nil {
				t.Fatalf("ParseDocument: %v", err)
			}
			got := CollectReferences(doc)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("CollectReferences() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

// TestWalkStringScalars_NilNodeIsNoop exercises the defensive nil guard
// directly — unreachable through the public ParseDocument/CollectReferences/
// Substitute entry points, since ParseDocument never returns a nil node
// without also returning an error.
func TestWalkStringScalars_NilNodeIsNoop(t *testing.T) {
	called := false
	walkStringScalars(nil, func(*yaml.Node) { called = true })
	if called {
		t.Error("visit was called for a nil node")
	}
}
