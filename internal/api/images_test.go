package api

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmnser/notely/internal/note"
)

func pngData(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestImages(t *testing.T) {
	dir := t.TempDir()
	s, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	h := newHandler(only(s), newEvents())
	request(t, h, http.MethodPut, "/api/notes/a", `{"title":"a","space":"work"}`)
	img := pngData(t)

	names := make([]string, 0, 2)
	for range 2 { // the same image is stored once, under the same name
		w := request(t, h, http.MethodPost, "/api/notes/a/images", string(img))
		var out struct{ Name string }
		if err := json.Unmarshal(w.Body.Bytes(), &out); w.Code != http.StatusOK || err != nil {
			t.Fatalf("upload: %d %s", w.Code, w.Body)
		}
		names = append(names, out.Name)
	}
	name := names[0]
	if names[1] != name || !imageName.MatchString(name) || !strings.HasSuffix(name, ".png") {
		t.Fatalf("names = %v", names)
	}
	if _, err := os.Stat(filepath.Join(dir, "work", name)); err != nil {
		t.Errorf("not next to the note: %v", err)
	}
	if w := request(t, h, http.MethodGet, "/api/notes/a/images", ""); w.Body.String() != `["`+name+`"]`+"\n" {
		t.Errorf("list: %d %s", w.Code, w.Body)
	}

	w := request(t, h, http.MethodGet, "/api/notes/a/images/"+name, "")
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), img) || w.Header().Get("Content-Type") != "image/png" ||
		!strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("get: %d %v", w.Code, w.Header())
	}
	for _, c := range []struct {
		name, method, path, body string
		want                     int
	}{
		{"svg", http.MethodPost, "/api/notes/a/images", `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`, http.StatusBadRequest},
		{"text", http.MethodPost, "/api/notes/a/images", "hello", http.StatusBadRequest},
		{"too large", http.MethodPost, "/api/notes/a/images", string(img) + strings.Repeat("x", maxImage), http.StatusBadRequest},
		{"unknown note", http.MethodPost, "/api/notes/b/images", string(img), http.StatusNotFound},
		{"unknown note list", http.MethodGet, "/api/notes/b/images", "", http.StatusNotFound},
		{"missing", http.MethodGet, "/api/notes/a/images/00000000000000000000000000000000.png", "", http.StatusNotFound},
		{"bad name", http.MethodGet, "/api/notes/a/images/..%2f.users.json", "", http.StatusNotFound},
		{"svg name", http.MethodGet, "/api/notes/a/images/00000000000000000000000000000000.svg", "", http.StatusNotFound},
	} {
		if w := request(t, h, c.method, c.path, c.body); w.Code != c.want {
			t.Errorf("%s: %d, want %d", c.name, w.Code, c.want)
		}
	}

	// Moving a note takes the images it references along.
	request(t, h, http.MethodPut, "/api/notes/a", `{"title":"a","space":"home","body":"![x](`+name+`)"}`)
	if _, err := os.Stat(filepath.Join(dir, "home", name)); err != nil {
		t.Errorf("not copied along: %v", err)
	}
	if w := request(t, h, http.MethodGet, "/api/notes/a/images/"+name, ""); w.Code != http.StatusOK {
		t.Errorf("get after move: %d", w.Code)
	}
}

func TestCleanImages(t *testing.T) {
	dir := t.TempDir()
	s, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	const used, unused, fresh = "00000000000000000000000000000001.png", "00000000000000000000000000000002.png",
		"00000000000000000000000000000003.png"
	if _, err := s.put(note.Note{ID: "a", Title: "a", Space: "work", Body: "![x](" + used + ")"}); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-8 * 24 * time.Hour)
	for _, f := range []string{"work/" + used, "work/" + unused, "work/" + fresh, used, "gone/" + unused, ".sync/" + unused} {
		writeNote(t, dir, f, "img")
		if f != "work/"+fresh {
			if err := os.Chtimes(filepath.Join(dir, f), old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	n, err := s.cleanImages(time.Now())
	if err != nil || n != 3 {
		t.Fatalf("cleanImages = %d, %v, want 3 removed", n, err)
	}
	for f, want := range map[string]bool{
		"work/" + used: true, "work/" + unused: false, "work/" + fresh: true, used: false,
		"gone": false, ".sync/" + unused: true, "work/a.md": true,
	} {
		if _, err := os.Stat(filepath.Join(dir, f)); (err == nil) != want {
			t.Errorf("%s exists: %v, want %v", f, err == nil, want)
		}
	}
}

func TestNextCleanup(t *testing.T) {
	for now, want := range map[string]string{
		"2026-10-20T10:00:00Z": "2026-10-25T23:00:00Z", // Tuesday
		"2026-10-25T22:59:00Z": "2026-10-25T23:00:00Z", // Sunday, before
		"2026-10-25T23:00:00Z": "2026-11-01T23:00:00Z", // Sunday, at
		"2026-10-26T01:00:00Z": "2026-11-01T23:00:00Z", // Monday
	} {
		n, _ := time.Parse(time.RFC3339, now)
		if got := nextCleanup(n).Format(time.RFC3339); got != want {
			t.Errorf("nextCleanup(%s) = %s, want %s", now, got, want)
		}
	}
}
