package paramtemplate

import (
	"reflect"
	"testing"
)

func TestMergeParams(t *testing.T) {
	tests := []struct {
		name           string
		fileSources    []map[string]string
		flagParams     []Param
		wantValues     map[string]string
		wantOverridden []string
	}{
		{
			name:       "no sources at all",
			wantValues: map[string]string{},
		},
		{
			name:       "flags only",
			flagParams: []Param{{Key: "NAME", Value: "X"}, {Key: "PROMPT", Value: "hi"}},
			wantValues: map[string]string{"NAME": "X", "PROMPT": "hi"},
		},
		{
			name:        "file only",
			fileSources: []map[string]string{{"NAME": "X"}},
			wantValues:  map[string]string{"NAME": "X"},
		},
		{
			name:        "later param-file overrides an earlier one, no warning",
			fileSources: []map[string]string{{"NAME": "X"}, {"NAME": "Y"}},
			wantValues:  map[string]string{"NAME": "Y"},
		},
		{
			name:           "a --param overrides a file value for the same key, with a warning",
			fileSources:    []map[string]string{{"NAME": "X"}},
			flagParams:     []Param{{Key: "NAME", Value: "Y"}},
			wantValues:     map[string]string{"NAME": "Y"},
			wantOverridden: []string{"NAME"},
		},
		{
			name:       "a later --param overrides an earlier --param, no warning",
			flagParams: []Param{{Key: "NAME", Value: "X"}, {Key: "NAME", Value: "Y"}},
			wantValues: map[string]string{"NAME": "Y"},
		},
		{
			name:           "multiple overridden keys reported sorted",
			fileSources:    []map[string]string{{"NAME": "X", "PROMPT": "a"}},
			flagParams:     []Param{{Key: "PROMPT", Value: "b"}, {Key: "NAME", Value: "Y"}},
			wantValues:     map[string]string{"NAME": "Y", "PROMPT": "b"},
			wantOverridden: []string{"NAME", "PROMPT"},
		},
		{
			name:        "flag key not present in any file is not reported as overridden",
			fileSources: []map[string]string{{"NAME": "X"}},
			flagParams:  []Param{{Key: "EXTRA", Value: "1"}},
			wantValues:  map[string]string{"NAME": "X", "EXTRA": "1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotValues, gotOverridden := MergeParams(tt.fileSources, tt.flagParams)
			if !reflect.DeepEqual(gotValues, tt.wantValues) {
				t.Errorf("values = %#v, want %#v", gotValues, tt.wantValues)
			}
			if !reflect.DeepEqual(gotOverridden, tt.wantOverridden) {
				t.Errorf("overridden = %#v, want %#v", gotOverridden, tt.wantOverridden)
			}
		})
	}
}

func TestUnknownKeys(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]string
		refs   []string
		want   []string
	}{
		{"nothing unknown", map[string]string{"NAME": "X"}, []string{"NAME"}, nil},
		{"one unknown", map[string]string{"NAME": "X", "EXTRA": "1"}, []string{"NAME"}, []string{"EXTRA"}},
		{"sorted when multiple unknown", map[string]string{"B": "1", "A": "2"}, nil, []string{"A", "B"}},
		{"no values, no refs", map[string]string{}, nil, nil},
		{"all referenced", map[string]string{"A": "1", "B": "2"}, []string{"A", "B", "C"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := UnknownKeys(tt.values, tt.refs)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("UnknownKeys(%v, %v) = %#v, want %#v", tt.values, tt.refs, got, tt.want)
			}
		})
	}
}
