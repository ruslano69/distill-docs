package cliutil

import (
	"reflect"
	"testing"
)

func TestSplitPositionalFlag(t *testing.T) {
	tests := []struct {
		args     []string
		wantPos  string
		wantRest []string
	}{
		{[]string{"SPEC-42", "--json"}, "SPEC-42", []string{"--json"}},
		{[]string{"--json", "SPEC-42"}, "SPEC-42", []string{"--json"}},
		{[]string{"SPEC-42", "--limit", "5", "--json"}, "SPEC-42", []string{"--limit", "5", "--json"}},
		// Pre-existing limitation (preserved, not introduced here): a flag's
		// bare VALUE ahead of the slug is mistaken for the positional, since
		// this function has no notion of which flags take an argument.
		{[]string{"--limit", "5", "SPEC-42", "--json"}, "5", []string{"--limit", "SPEC-42", "--json"}},
		{[]string{"--json"}, "", []string{"--json"}},
		{[]string{}, "", []string{}},
		{[]string{"SPEC-1", "SPEC-2"}, "SPEC-1", []string{"SPEC-2"}}, // only the first bare token is positional
	}
	for _, tc := range tests {
		pos, rest := SplitPositionalFlag(tc.args)
		if pos != tc.wantPos || !reflect.DeepEqual(rest, tc.wantRest) {
			t.Errorf("SplitPositionalFlag(%v) = (%q, %v), want (%q, %v)", tc.args, pos, rest, tc.wantPos, tc.wantRest)
		}
	}
}

func TestParseEmbedding(t *testing.T) {
	tests := []struct {
		raw     string
		want    []float32
		wantErr bool
	}{
		{"", nil, false},
		{"0.1,0.2,0.3", []float32{0.1, 0.2, 0.3}, false},
		{"[0.1,0.2,0.3]", []float32{0.1, 0.2, 0.3}, false},
		{"[ 0.1, 0.2 ]", []float32{0.1, 0.2}, false},
		{"0.1,,0.2", []float32{0.1, 0.2}, false}, // empty segments skipped
		{"0.1,bogus,0.2", nil, true},
	}
	for _, tc := range tests {
		got, err := ParseEmbedding(tc.raw)
		if (err != nil) != tc.wantErr {
			t.Errorf("ParseEmbedding(%q) err = %v, wantErr %v", tc.raw, err, tc.wantErr)
			continue
		}
		if err == nil && !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ParseEmbedding(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := Truncate("short", 40); got != "short" {
		t.Errorf("short string should be unchanged, got %q", got)
	}
	long := "this title is definitely longer than forty bytes for sure"
	got := Truncate(long, 20)
	if len(got) != 20 || got[17:] != "..." {
		t.Errorf("Truncate(_, 20) = %q (len %d), want 20 bytes ending in ...", got, len(got))
	}
}
