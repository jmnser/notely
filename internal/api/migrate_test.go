package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmnser/notely/internal/note"
)

func TestMigrate(t *testing.T) {
	dir := t.TempDir()
	const img, stray = "0123456789abcdef0123456789abcdef.png", "fedcba9876543210fedcba9876543210.png"
	n := note.Note{
		ID: "a", Title: "Plan", Space: "work", Updated: time.Now().UTC(),
		Body: "![x](/images/" + img + ") [full](/images/" + img + ")",
	}
	writeNote(t, dir, "work/Plan.md", string(encode(n)))
	writeNote(t, dir, "Inbox.md", string(encode(note.Note{ID: "b", Title: "Inbox", Updated: n.Updated})))
	writeNote(t, dir, ".images/"+img, "png")
	writeNote(t, dir, ".images/"+stray, "png")
	writeNote(t, dir, ".sync/ann/token", "tok")
	writeNote(t, dir, ".sync/bob/token", "tok2")
	writeNote(t, dir, ".users.json", "{}")
	users := map[string]argonHash{"ann": {}, "bob": {}}

	if err := migrate(dir, users); err == nil || !strings.Contains(err.Error(), "no user") {
		t.Fatalf("two users, no default: %v", err)
	}
	users = map[string]argonHash{"ann": {}}
	for range 2 { // repeating it changes nothing
		if err := migrate(dir, users); err != nil {
			t.Fatal(err)
		}
	}
	for f, want := range map[string]bool{
		"users/ann/work/Plan.md": true, "users/ann/Inbox.md": true, "users/ann/work/" + img: true,
		"users/ann/" + stray: true, "users/ann/.sync/token": true, "users/bob/.sync/token": true,
		".users.json": true, "work": false, "Inbox.md": false, ".images": false, ".sync": false,
	} {
		if _, err := os.Stat(filepath.Join(dir, f)); (err == nil) != want {
			t.Errorf("%s exists: %v, want %v", f, err == nil, want)
		}
	}
	data, _ := os.ReadFile(filepath.Join(dir, "users/ann/work/Plan.md"))
	got, err := decode(data)
	if err != nil || got.Body != "![x]("+img+") [full]("+img+")" || got.Title != "Plan" {
		t.Errorf("migrated note = %+v, %v", got, err)
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
