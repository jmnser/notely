package api

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmnser/notely/internal/note"
)

func TestMigrate(t *testing.T) {
	dir := t.TempDir()
	const img, stray = "0123456789abcdef0123456789abcdef.png", "fedcba9876543210fedcba9876543210.png"
	n := note.Note{
		ID: "a", Title: "Plan", Space: "work", Updated: time.Now().UTC(),
		Body: "![x](/images/" + img + ") [full](/images/" + img + ") https://host/api/images/" + img,
	}
	writeNote(t, dir, "work/Plan.md", string(encode(n)))
	writeNote(t, dir, "Inbox.md", string(encode(note.Note{ID: "b", Title: "Inbox", Updated: n.Updated})))
	writeNote(t, dir, "users/List.md", string(encode(note.Note{ID: "c", Title: "List", Space: "users", Updated: n.Updated})))
	writeNote(t, dir, "users.json", "{}") // a -users file in the data directory
	writeNote(t, dir, ".images/"+img, "png")
	writeNote(t, dir, ".images/"+stray, "png")
	writeNote(t, dir, ".sync/ann/token", "tok")
	writeNote(t, dir, ".sync/bob/token", "tok2")
	writeNote(t, dir, ".users.json", "{}")
	users := map[string]argonHash{"ann": {}, "bob": {}}

	for range 2 { // repeating it changes nothing
		if err := migrate(dir, users); err != nil {
			t.Fatal(err)
		}
	}
	for f, want := range map[string]bool{
		"users/ann/work/Plan.md": true, "users/bob/work/Plan.md": true, "users/ann/Inbox.md": true,
		"users/ann/users/List.md": true, "users/bob/users/List.md": true, "users/ann/work/" + img: true,
		"users/bob/work/" + img: true, "users/ann/" + stray: true, "users/ann/.sync/token": true,
		"users/bob/.sync/token": true, ".users.json": true, "users.json": true, ".layout": true,
		"work": false, "Inbox.md": false, ".images": false, ".sync": false, ".users-space": false, "users/List.md": false,
	} {
		if _, err := os.Stat(filepath.Join(dir, f)); (err == nil) != want {
			t.Errorf("%s exists: %v, want %v", f, err == nil, want)
		}
	}
	data, _ := os.ReadFile(filepath.Join(dir, "users/bob/work/Plan.md"))
	got, err := decode(data)
	if want := "![x](" + img + ") [full](" + img + ") https://host/api/images/" + img; err != nil ||
		got.Body != want || got.Title != "Plan" {
		t.Errorf("migrated note = %+v, %v, want body %q", got, err, want)
	}

	// Once migrated, a users directory is the new layout.
	writeNote(t, dir, "users/ann/New.md", "x")
	if err := migrate(dir, users); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "users/ann/New.md")); err != nil {
		t.Errorf("migrated again: %v", err)
	}
}

func TestMigrateInterrupted(t *testing.T) {
	dir := t.TempDir()
	writeNote(t, dir, "work/Plan.md", string(encode(note.Note{ID: "a", Title: "Plan", Space: "work"})))
	writeNote(t, dir, ".layout", "migrating")
	writeNote(t, dir, "users/ann/work/Plan.md", "partial copy, kept")
	if err := migrate(dir, map[string]argonHash{"ann": {}}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "users/ann/work/Plan.md")); string(data) != "partial copy, kept" {
		t.Errorf("copy replaced: %q", data)
	}
	if _, err := os.Stat(filepath.Join(dir, "work")); err == nil {
		t.Error("old notes left")
	}
}

func TestLoadUsersUnique(t *testing.T) {
	h := hashPassword("pw")
	for name, data := range map[string]string{
		"twice":   `{"ann":"` + h + `","ann":"` + h + `"}`,
		"case":    `{"ann":"` + h + `","Ann":"` + h + `"}`,
		"invalid": `{"../x":"` + h + `"}`,
	} {
		path := filepath.Join(t.TempDir(), "users.json")
		writeNote(t, filepath.Dir(path), "users.json", data)
		if _, err := loadUsers(path); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
}
