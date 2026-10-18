package api

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jmnser/notely/internal/note"
)

// files lists the Markdown files under dir.
func files(t *testing.T, dir string) []string {
	t.Helper()
	var got []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, path)
			got = append(got, filepath.ToSlash(rel))
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	return got
}

func TestStoreLayout(t *testing.T) {
	dir := t.TempDir()
	s, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	put := func(n note.Note) {
		t.Helper()
		if _, err := s.put(n); err != nil {
			t.Fatal(err)
		}
	}
	check := func(want ...string) {
		t.Helper()
		if got := files(t, dir); !slices.Equal(got, want) {
			t.Fatalf("files = %q, want %q", got, want)
		}
	}

	put(note.Note{ID: "a", Title: "Plan"})
	put(note.Note{ID: "b", Title: "plan"})
	put(note.Note{ID: "c", Space: "work", Title: "a/b: c?"})
	put(note.Note{ID: "d", Title: ""})
	check("Plan.md", "plan-2.md", "untitled.md", "work/a-b- c-.md")

	put(note.Note{ID: "a", Title: "Plan", Body: "edited"}) // same title keeps the file
	put(note.Note{ID: "b", Title: "plan"})                 // keeps its suffix
	check("Plan.md", "plan-2.md", "untitled.md", "work/a-b- c-.md")

	put(note.Note{ID: "c", Space: "home", Title: "c"}) // move removes the empty space
	check("Plan.md", "home/c.md", "plan-2.md", "untitled.md")

	if err := s.delete("a"); err != nil {
		t.Fatal(err)
	}
	put(note.Note{ID: "a", Title: "Plan"})
	check("Plan.md", "home/c.md", "plan-2.md", "untitled.md")

	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("# not ours"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "x.md"), []byte("---\nid: x\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	put(note.Note{ID: "e", Title: "notes"}) // leaves foreign files alone
	if _, err := os.Stat(filepath.Join(dir, "notes-2.md")); err != nil {
		t.Fatal(err)
	}

	s, err = openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	notes := s.list()
	ids := make([]string, 0, len(notes))
	for _, n := range notes {
		ids = append(ids, n.ID)
	}
	slices.Sort(ids)
	if want := []string{"a", "b", "c", "d", "e"}; !slices.Equal(ids, want) {
		t.Errorf("reopened ids = %q, want %q", ids, want)
	}
}

func TestEncodeDecode(t *testing.T) {
	n := note.Note{
		ID:      "id_1",
		Space:   `say "hi"`,
		Title:   "line\nbreak: ünïcode",
		Body:    "---\n# Body\r\n\n- [ ] task\n---",
		Pinned:  true,
		Updated: time.Date(2026, 10, 15, 9, 12, 0, 123, time.UTC),
	}
	got, err := decode(encode(n))
	if err != nil || got != n {
		t.Fatalf("decode(encode) = %+v, %v; want %+v", got, err, n)
	}

	got, err = decode([]byte("---\r\nid: x\r\ntitle: Plain title\r\nextra: 1\r\n---\r\nbody"))
	if err != nil || got.ID != "x" || got.Title != "Plain title" || got.Body != "body" {
		t.Errorf("hand-written = %+v, %v", got, err)
	}

	for _, bad := range []string{"# no front matter", "---\nid: x\n", "---\ntitle: no id\n---\n", "---\nid: x\nupdated: soon\n---\n"} {
		if _, err := decode([]byte(bad)); err == nil {
			t.Errorf("decode(%q) succeeded", bad)
		}
	}
}

func TestFileName(t *testing.T) {
	tests := map[string]string{
		"Meeting notes":         "Meeting notes",
		"a/b\\c:d":              "a-b-c-d",
		"..":                    "fallback",
		" .hidden. ":            "hidden",
		"":                      "fallback",
		strings.Repeat("ä", 60): strings.Repeat("ä", 50),
		"con":                   "con-",
		"NUL.txt":               "NUL-.txt",
		"Console":               "Console",
	}
	for in, want := range tests {
		if got := fileName(in, "fallback"); got != want {
			t.Errorf("fileName(%q) = %q, want %q", in, got, want)
		}
	}
}
