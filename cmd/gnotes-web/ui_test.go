package main

import (
	"testing"

	"github.com/jmnser/gnotes/internal/note"
)

func TestMerge(t *testing.T) {
	remote := []note.Note{{ID: "a", Title: "remote"}, {ID: "b"}, {ID: "c"}, {ID: "d", Title: "remote"}}
	local := []note.Note{{ID: "a", Title: "local"}, {ID: "d", Title: "local"}, {ID: "e"}, {ID: "f"}}
	dirty := map[string]int{"a": 1, "e": 2}

	got := map[string]string{}
	for _, n := range merge(remote, local, dirty, []string{"b"}) {
		got[n.ID] = n.Title
	}
	// a: pending edit, b: deleted here, d: changed remotely, e: created here, f: deleted remotely
	want := map[string]string{"a": "local", "c": "", "d": "remote", "e": ""}
	if len(got) != len(want) {
		t.Fatalf("merge = %v, want %v", got, want)
	}
	for id, title := range want {
		if g, ok := got[id]; !ok || g != title {
			t.Errorf("note %s = %q (present %v), want %q", id, g, ok, title)
		}
	}
}

func TestLabel(t *testing.T) {
	tests := map[string]note.Note{
		"Title":    {Title: " Title ", Body: "body"},
		"first":    {Body: "\nfirst\nsecond"},
		"Untitled": {Body: "  "},
	}
	for want, n := range tests {
		if got := label(n); got != want {
			t.Errorf("label(%+v) = %q, want %q", n, got, want)
		}
	}
}
