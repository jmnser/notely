package notefile

import (
	"strings"
	"testing"
	"time"

	"github.com/jmnser/notely/internal/note"
)

func TestEncodeDecode(t *testing.T) {
	n := note.Note{
		ID:      "id_1",
		Space:   `say "hi"`,
		Title:   "line\nbreak: ünïcode",
		Body:    "---\n# Body\r\n\n- [ ] task\n---",
		Pinned:  true,
		Updated: time.Date(2026, 10, 15, 9, 12, 0, 123, time.UTC),
	}
	data, err := Encode(n)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(data, ".md")
	if err != nil || got != n {
		t.Fatalf("Decode(encode) = %+v, %v; want %+v", got, err, n)
	}

	got, err = Decode([]byte("---\r\nid: x\r\ntitle: Plain title\r\nextra: 1\r\n---\r\nbody"), ".md")
	if err != nil || got.ID != "x" || got.Title != "Plain title" || got.Body != "body" {
		t.Errorf("hand-written = %+v, %v", got, err)
	}

	for _, bad := range []string{"# no front matter", "---\nid: x\n", "---\ntitle: no id\n---\n", "---\nid: x\nupdated: soon\n---\n"} {
		if _, err := Decode([]byte(bad), ".md"); err == nil {
			t.Errorf("Decode(%q) succeeded", bad)
		}
	}
}

func TestEncodeDecodeDrawing(t *testing.T) {
	n := note.Note{
		ID: "id_1", Kind: note.Drawing, Space: "work", Title: "Flow <1>", Pinned: true,
		Body:    `{"appState":{"viewBackgroundColor":"#fff"},"elements":[{"id":"x","text":"a<b"}],"type":"excalidraw","version":2}`,
		Updated: time.Date(2026, 10, 15, 9, 12, 0, 123, time.UTC),
	}
	data, err := Encode(n)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "\n  \"notely\": {\n") || !strings.Contains(string(data), `"a<b"`) {
		t.Errorf("file not indented or HTML-escaped:\n%s", data)
	}
	got, err := Decode(data, ".excalidraw")
	if err != nil || got != n {
		t.Fatalf("Decode(encode) = %+v, %v; want %+v", got, err, n)
	}

	n.Body = ""
	if data, err = Encode(n); err != nil {
		t.Fatal(err)
	}
	if got, err = Decode(data, ".excalidraw"); err != nil || got.Body != `{"elements":[],"type":"excalidraw","version":2}` {
		t.Errorf("empty drawing = %q, %v", got.Body, err)
	}

	for _, bad := range []string{`not json`, `{"type":"excalidraw"}`, `{"notely":{"id":"a/b"}}`} {
		if _, err := Decode([]byte(bad), ".excalidraw"); err == nil {
			t.Errorf("Decode(%q) succeeded", bad)
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
		if got := Name(in, "fallback"); got != want {
			t.Errorf("Name(%q) = %q, want %q", in, got, want)
		}
	}
}
