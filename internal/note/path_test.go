package note

import (
	"fmt"
	"testing"
)

func TestSpacePath(t *testing.T) {
	for in, want := range map[string]string{
		"Work":               "Work",
		" Work / Projects /": "Work/Projects",
		"//a//b":             "a/b",
		"Notes":              "",
		" ":                  "",
		"Notes/x":            "Notes/x",
	} {
		if got := SpacePath(in); got != want {
			t.Errorf("SpacePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParsePath(t *testing.T) {
	for in, want := range map[string]string{
		"Ideas":           "|Ideas|true",
		" Work/ ":         "Work||true",
		"Work/Ideas":      "Work|Ideas|true",
		"Work / Sub/ Doc": "Work/Sub|Doc|true",
		"Notes/x":         "|x|true",
		"/":               "||true",
		"  ":              "||false",
	} {
		space, title, ok := ParsePath(in)
		if got := fmt.Sprintf("%s|%s|%v", space, title, ok); got != want {
			t.Errorf("ParsePath(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestMerge(t *testing.T) {
	remote := []Note{{ID: "a", Title: "remote"}, {ID: "b"}, {ID: "c"}, {ID: "d", Title: "remote"}}
	local := []Note{{ID: "a", Title: "local"}, {ID: "d", Title: "local"}, {ID: "e"}, {ID: "f"}}
	dirty := map[string]int{"a": 1, "e": 2}

	got := map[string]string{}
	for _, n := range Merge(remote, local, dirty, []string{"b"}) {
		got[n.ID] = n.Title
	}
	// a: pending edit, b: deleted here, d: changed remotely, e: created here, f: deleted remotely
	want := map[string]string{"a": "local", "c": "", "d": "remote", "e": ""}
	if len(got) != len(want) {
		t.Fatalf("Merge = %v, want %v", got, want)
	}
	for id, title := range want {
		if g, ok := got[id]; !ok || g != title {
			t.Errorf("note %s = %q (present %v), want %q", id, g, ok, title)
		}
	}
}

func TestLabel(t *testing.T) {
	tests := map[string]Note{
		"Title":    {Title: " Title ", Body: "body"},
		"first":    {Body: "\nfirst\nsecond"},
		"Untitled": {Body: "  "},
	}
	for want, n := range tests {
		if got := n.Label(); got != want {
			t.Errorf("Label(%+v) = %q, want %q", n, got, want)
		}
	}
}
