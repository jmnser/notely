//go:build !wasm

package main

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmnser/notely/internal/note"
	"github.com/jmnser/notely/internal/notefile"
)

// mirrored returns a signed-on daemon for a new directory and backend, and a
// client and session to change the notes elsewhere.
func mirrored(t *testing.T) (d *daemon, other *client, token string) {
	t.Helper()
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	other = backend(t)
	c, err := newClient(other.base)
	if err != nil {
		t.Fatal(err)
	}
	c.id = randID()
	var cfg config
	cfg.Server.User, cfg.Server.PasswordCommand = "joe", "echo pw"
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	st := &mirrorState{path: filepath.Join(t.TempDir(), "state.json"), Dir: dir, User: "joe", Files: map[string]entry{}}
	d = &daemon{c: c, cfg: cfg, st: st, root: root, rejected: map[string]string{}}
	if err := d.signIn(t.Context()); err != nil {
		t.Fatal(err)
	}
	if token, err = other.login(t.Context(), "joe", "pw"); err != nil {
		t.Fatal(err)
	}
	return d, other, token
}

func syncOK(t *testing.T, d *daemon) {
	t.Helper()
	if err := d.sync(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, d *daemon, rel, data string) {
	t.Helper()
	p := filepath.Join(d.st.Dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, d *daemon, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(d.st.Dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func exists(d *daemon, rel string) bool {
	_, err := os.Lstat(filepath.Join(d.st.Dir, filepath.FromSlash(rel)))
	return err == nil
}

// remoteNotes returns the server's notes by title.
func remoteNotes(t *testing.T, c *client, token string) map[string]note.Note {
	t.Helper()
	notes, _, err := c.exchange(t.Context(), token, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	byTitle := map[string]note.Note{}
	for _, n := range notes {
		byTitle[n.Title] = n
	}
	return byTitle
}

func pngData(t *testing.T, size int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, size, size))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestDaemonPull(t *testing.T) {
	d, other, token := mirrored(t)
	ctx := t.Context()
	n := note.Note{ID: "a", Space: "Work/Projects", Title: "Plan", Body: "# Plan"}
	if _, _, err := other.exchange(ctx, token, []note.Note{n}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := other.addImage(ctx, token, "Work", "pic.png", pngData(t, 1)); err != nil {
		t.Fatal(err)
	}
	syncOK(t, d)
	got, err := notefile.Decode([]byte(readFile(t, d, "Work/Projects/Plan.md")), ".md")
	if err != nil || got.ID != "a" || got.Body != "# Plan" || got.Space != "Work/Projects" {
		t.Fatalf("pulled note = %+v, %v", got, err)
	}
	if !bytes.Equal([]byte(readFile(t, d, "Work/pic.png")), pngData(t, 1)) {
		t.Fatal("pulled image differs")
	}

	n.Title, n.Body = "Plan B", "changed"
	if _, _, err := other.exchange(ctx, token, []note.Note{n}, nil); err != nil {
		t.Fatal(err)
	}
	syncOK(t, d)
	if exists(d, "Work/Projects/Plan.md") || !strings.HasSuffix(readFile(t, d, "Work/Projects/Plan B.md"), "changed") {
		t.Fatal("rename elsewhere not pulled")
	}

	if _, _, err := other.exchange(ctx, token, nil, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if err := other.deleteImage(ctx, token, note.Image{Space: "Work", Name: "pic.png"}); err != nil {
		t.Fatal(err)
	}
	syncOK(t, d)
	if exists(d, "Work") {
		t.Fatal("files deleted elsewhere are still there")
	}
}

func TestDaemonPush(t *testing.T) {
	d, other, token := mirrored(t)
	writeFile(t, d, "Ideas.md", "hello")
	writeFile(t, d, "Work/Todo.md", "- [ ] task")
	writeFile(t, d, "Work/Flow.excalidraw", `{"type":"excalidraw","elements":[]}`)
	syncOK(t, d)
	notes := remoteNotes(t, other, token)
	if n := notes["Flow"]; n.Kind != note.Drawing || n.Space != "Work" {
		t.Errorf("Flow = %+v", n)
	}
	delete(notes, "Flow")
	if n := notes["Ideas"]; n.Space != "" || n.Body != "hello" {
		t.Errorf("Ideas = %+v", n)
	}
	if n := notes["Todo"]; n.Space != "Work" || n.Body != "- [ ] task" {
		t.Errorf("Todo = %+v", n)
	}
	got, err := notefile.Decode([]byte(readFile(t, d, "Ideas.md")), ".md")
	if err != nil || got.ID != notes["Ideas"].ID {
		t.Fatalf("Ideas.md got no front matter: %+v, %v", got, err)
	}
	before := readFile(t, d, "Ideas.md")
	syncOK(t, d)
	if len(remoteNotes(t, other, token)) != 3 || readFile(t, d, "Ideas.md") != before {
		t.Fatal("a second sync changed something")
	}

	got.Body = "edited"
	writeFile(t, d, "Ideas.md", string(notefile.EncodeMarkdown(got)))
	syncOK(t, d)
	if n := remoteNotes(t, other, token)["Ideas"]; n.Body != "edited" {
		t.Fatalf("edit not pushed: %+v", n)
	}
	if readFile(t, d, "Ideas.md") != string(notefile.EncodeMarkdown(got)) {
		t.Error("pushed file was rewritten")
	}

	if err := os.Rename(filepath.Join(d.st.Dir, "Ideas.md"), filepath.Join(d.st.Dir, "Work", "Big ideas.md")); err != nil {
		t.Fatal(err)
	}
	syncOK(t, d)
	notes = remoteNotes(t, other, token)
	if n := notes["Big ideas"]; len(notes) != 3 || n.ID != got.ID || n.Space != "Work" {
		t.Fatalf("move not pushed: %+v", notes)
	}
	if len(d.st.Files) != 3 {
		t.Errorf("files = %v, want the former path dropped", d.st.Files)
	}
	if moved, _ := notefile.Decode([]byte(readFile(t, d, "Work/Big ideas.md")), ".md"); moved.Title != "Big ideas" ||
		moved.Space != "Work" {
		t.Errorf("front matter not moved along: %+v", moved)
	}

	writeFile(t, d, "Copy.md", readFile(t, d, "Work/Big ideas.md"))
	syncOK(t, d)
	if notes = remoteNotes(t, other, token); len(notes) != 4 || notes["Copy"].ID == got.ID {
		t.Fatalf("copy = %+v", notes)
	}

	if err := os.Remove(filepath.Join(d.st.Dir, "Copy.md")); err != nil {
		t.Fatal(err)
	}
	syncOK(t, d)
	if notes = remoteNotes(t, other, token); len(notes) != 3 {
		t.Fatalf("deletion not pushed: %+v", notes)
	}
}

func TestDaemonLocalWins(t *testing.T) {
	d, other, token := mirrored(t)
	n := note.Note{ID: "a", Title: "Plan", Body: "old"}
	if _, _, err := other.exchange(t.Context(), token, []note.Note{n}, nil); err != nil {
		t.Fatal(err)
	}
	syncOK(t, d)
	n.Body = "theirs"
	if _, _, err := other.exchange(t.Context(), token, []note.Note{n}, nil); err != nil {
		t.Fatal(err)
	}
	n.Body = "mine"
	writeFile(t, d, "Plan.md", string(notefile.EncodeMarkdown(n)))
	syncOK(t, d)
	if got := remoteNotes(t, other, token)["Plan"]; got.Body != "mine" {
		t.Fatalf("server has %q, want the local edit", got.Body)
	}
}

func TestDaemonBadFile(t *testing.T) {
	d, other, token := mirrored(t)
	writeFile(t, d, "Plan.md", "hello")
	syncOK(t, d)
	writeFile(t, d, "Plan.md", "---\nbroken")
	syncOK(t, d)
	if n := remoteNotes(t, other, token)["Plan"]; n.Body != "hello" {
		t.Fatalf("broken file changed the note: %+v", n)
	}
	if readFile(t, d, "Plan.md") != "---\nbroken" {
		t.Fatal("broken file was replaced")
	}
}

func TestDaemonImages(t *testing.T) {
	d, other, token := mirrored(t)
	ctx := t.Context()
	writeFile(t, d, "Work/Beach Day.PNG", string(pngData(t, 1)))
	writeFile(t, d, "Work/Plan.md", "![](beach-day.png)")
	syncOK(t, d)
	images, err := other.images(ctx, token)
	if err != nil || len(images) != 1 || images[0].Path() != "Work/beach-day.png" {
		t.Fatalf("images = %+v, %v", images, err)
	}
	if exists(d, "Work/Beach Day.PNG") || !exists(d, "Work/beach-day.png") {
		t.Fatal("image file not renamed as stored")
	}

	if err := os.Rename(filepath.Join(d.st.Dir, "Work", "beach-day.png"), filepath.Join(d.st.Dir, "Work", "sea.png")); err != nil {
		t.Fatal(err)
	}
	syncOK(t, d)
	if images, _ = other.images(ctx, token); len(images) != 1 || images[0].Name != "sea.png" {
		t.Fatalf("move not pushed: %+v", images)
	}
	if !strings.HasSuffix(readFile(t, d, "Work/Plan.md"), "![](sea.png)") {
		t.Errorf("reference not changed along: %q", readFile(t, d, "Work/Plan.md"))
	}

	writeFile(t, d, "Work/sea.png", string(pngData(t, 2)))
	syncOK(t, d)
	if data, err := other.image(ctx, token, note.Image{Space: "Work", Name: "sea.png"}); err != nil ||
		!bytes.Equal(data, pngData(t, 2)) {
		t.Fatalf("change not pushed: %v", err)
	}

	writeFile(t, d, "notes.png", "not an image")
	if err := os.Remove(filepath.Join(d.st.Dir, "Work", "sea.png")); err != nil {
		t.Fatal(err)
	}
	syncOK(t, d)
	if images, _ = other.images(ctx, token); len(images) != 0 {
		t.Fatalf("deletion not pushed: %+v", images)
	}
	if !exists(d, "notes.png") {
		t.Fatal("rejected file removed")
	}
}

func TestDaemonRun(t *testing.T) {
	d, other, token := mirrored(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error)
	go func() { done <- d.run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); !ok(); time.Sleep(50 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}
	time.Sleep(200 * time.Millisecond) // the first sync
	writeFile(t, d, "Sub/Ideas.md", "hello")
	waitFor("the push", func() bool { return remoteNotes(t, other, token)["Ideas"].Body == "hello" })

	if _, _, err := other.exchange(t.Context(), token, []note.Note{{ID: "b", Title: "Theirs"}}, nil); err != nil {
		t.Fatal(err)
	}
	waitFor("the pull", func() bool { return exists(d, "Theirs.md") })
}
