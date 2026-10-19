package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jmnser/notely/internal/note"
)

func TestTree(t *testing.T) {
	notes := []note.Note{
		{ID: "1", Space: "work/b"},
		{ID: "2", Space: "work"},
		{ID: "3"},
		{ID: "4", Space: "work/a/x"},
		{ID: "5", Space: "Home"},
	}
	var lines []string
	var walk func([]*space, string)
	walk = func(spaces []*space, indent string) {
		for _, s := range spaces {
			lines = append(lines, fmt.Sprintf("%s%s %s %d %d", indent, s.name, s.path, s.count, len(s.notes)))
			walk(s.spaces, indent+"  ")
		}
	}
	walk(tree(notes, map[string]bool{"work": true, "work/b": true}), "")
	want := `work work 3 1
  b work/b 1 1
  a work/a 1 0
    x work/a/x 1 1
Home Home 1 1
Notes  1 1`
	if got := strings.Join(lines, "\n"); got != want {
		t.Errorf("tree:\n%s\nwant:\n%s", got, want)
	}
}

func TestWithin(t *testing.T) {
	for _, c := range []struct {
		s, root string
		want    bool
	}{
		{"a", "a", true}, {"a/b", "a", true}, {"ab", "a", false}, {"a", "", false}, {"", "", true},
	} {
		if got := within(c.s, c.root); got != c.want {
			t.Errorf("within(%q, %q) = %v", c.s, c.root, got)
		}
	}
}
