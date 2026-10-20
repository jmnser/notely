package api

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
)

func TestSharedSpaces(t *testing.T) {
	dir := t.TempDir()
	users := filepath.Join(t.TempDir(), "users.json")
	h := hashPassword("pw")
	writeNote(t, dir, "users/ann/Family/Diary.md", "---\nid: diary\ntitle: \"Diary\"\nspace: \"Family\"\nupdated: 2026-01-01T00:00:00Z\n---\nprivate")
	if err := os.WriteFile(filepath.Join(dir, layoutFile), []byte(usersDir), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(users, []byte(`{"ann":{"hash":"`+h+`","spaces":["Family"]},"bob":{"hash":"`+h+`","spaces":["Family"]},"carl":"`+h+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir, users)
	if err != nil {
		t.Fatal(err)
	}
	signOn := func(user string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.api.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, user)))
		})
	}
	ann, bob, carl := signOn("ann"), signOn("bob"), signOn("carl")
	ids := func(h http.Handler) []string {
		notes := list(t, h)
		out := make([]string, 0, len(notes))
		for _, n := range notes {
			out = append(out, n.ID)
		}
		slices.Sort(out)
		return out
	}

	if w := request(t, ann, http.MethodPut, "/api/notes/plan", `{"title":"Plan","space":"family/Trips"}`); w.Code != http.StatusOK {
		t.Fatalf("put: %d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(filepath.Join(dir, "shared", "Family", "Trips", "Plan.md")); err != nil {
		t.Errorf("shared note: %v", err)
	}
	if got := ids(bob); !slices.Equal(got, []string{"plan"}) {
		t.Errorf("bob sees %v", got)
	}
	if got := ids(carl); len(got) != 0 {
		t.Errorf("carl sees %v", got)
	}
	if got := ids(ann); !slices.Equal(got, []string{"diary", "plan"}) {
		t.Errorf("ann sees %v", got)
	}
	// Ann's note from before the space was shared stays hers when edited.
	request(t, ann, http.MethodPut, "/api/notes/diary", `{"title":"Diary","space":"Family","body":"more"}`)
	if got := ids(bob); !slices.Equal(got, []string{"plan"}) {
		t.Errorf("private note shared: bob sees %v", got)
	}

	img := request(t, bob, http.MethodPost, "/api/notes/plan/images", string(pngData(t)))
	if img.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", img.Code, img.Body)
	}
	name := pngName(t)
	if w := request(t, carl, http.MethodGet, "/api/notes/plan/images/"+name, ""); w.Code != http.StatusNotFound {
		t.Errorf("carl reads a shared image: %d", w.Code)
	}
	if w := request(t, ann, http.MethodGet, "/api/notes/plan/images/"+name, ""); w.Code != http.StatusOK {
		t.Errorf("ann reads a shared image: %d", w.Code)
	}
	if w := request(t, carl, http.MethodPut, "/api/notes/plan", `{"title":"Stolen","space":"Family"}`); w.Code != http.StatusOK {
		t.Fatal(w.Code)
	}
	if got := ids(bob); !slices.Equal(got, []string{"plan"}) || list(t, bob)[0].Title != "Plan" {
		t.Errorf("carl changed a shared note: %v", list(t, bob))
	}

	// Moving out of the shared space makes the note Bob's, with its image.
	request(t, bob, http.MethodPut, "/api/notes/plan", `{"title":"Plan","space":"Mine","body":"![](`+name+`)"}`)
	if got := ids(ann); !slices.Equal(got, []string{"diary"}) {
		t.Errorf("ann still sees %v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "users", "bob", "Mine", name)); err != nil {
		t.Errorf("image not moved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "shared", "Family", "Trips", "Plan.md")); err == nil {
		t.Error("shared copy left")
	}
	// And back.
	request(t, bob, http.MethodPut, "/api/notes/plan", `{"title":"Plan","space":"Family","body":"![](`+name+`)"}`)
	if _, err := os.Stat(filepath.Join(dir, "shared", "Family", name)); err != nil {
		t.Errorf("image not moved back: %v", err)
	}
	if w := request(t, ann, http.MethodDelete, "/api/notes/plan", ""); w.Code != http.StatusNoContent {
		t.Fatal(w.Code)
	}
	if got := ids(bob); len(got) != 0 {
		t.Errorf("bob sees %v after ann deleted it", got)
	}
}

func TestSharedEvents(t *testing.T) {
	ev := newEvents()
	ev.listeners[make(chan struct{}, 1)] = listener{user: "ann"}
	ev.listeners[make(chan struct{}, 1)] = listener{user: "bob"}
	ev.listeners[make(chan struct{}, 1)] = listener{user: "carl"}
	ev.notify([]string{"ann", "bob"}, "")
	told := 0
	for c, l := range ev.listeners {
		if len(c) == 1 {
			told++
			if l.user == "carl" {
				t.Error("carl told")
			}
		}
	}
	if told != 2 {
		t.Errorf("told %d", told)
	}
}

func TestSyncShared(t *testing.T) {
	tmp := t.TempDir()
	users, shared, remote := filepath.Join(tmp, "users"), filepath.Join(tmp, "shared", "Family"), filepath.Join(tmp, "remote.git")
	if _, err := git.PlainInit(remote, true); err != nil {
		t.Fatal(err)
	}
	writeNote(t, filepath.Join(users, "ann"), "Inbox.md", "a")
	writeNote(t, shared, "Plan.md", "b")
	writeNote(t, shared, "Trips/0123456789abcdef0123456789abcdef.png", "png")
	s := newSyncer(users, map[string]bool{"ann": true})
	s.shared = map[string]map[string]string{"ann": {"Family": shared}}
	if err := s.save("ann", syncSettings{Remote: remote, Branch: "main"}, "tok"); err != nil {
		t.Fatal(err)
	}
	s.syncUser(context.Background(), "ann")
	if st := s.status["ann"]; st.Error != "" {
		t.Fatal(st.Error)
	}
	want := []string{"Inbox.md", "shared/Family/Plan.md", "shared/Family/Trips/0123456789abcdef0123456789abcdef.png"}
	if files, _ := remoteFiles(t, remote); !slices.Equal(files, want) {
		t.Errorf("remote = %v", files)
	}
}

func TestLoadUsersSpaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	for _, tt := range []struct {
		json string
		ok   bool
	}{
		{`{"ann":{"hash":"H","spaces":["Family","Family"]},"bob":"H"}`, true},
		{`{"ann":{"hash":"H","spaces":["a/b"]}}`, false},
		{`{"ann":{"hash":"H","spaces":[".git"]}}`, false},
		{`{"ann":{"hash":"H","spaces":["Notes"]}}`, false},
		{`{"ann":{"hash":"H","spaces":["Family"]},"bob":{"hash":"H","spaces":["family"]}}`, false},
		{`{"ann":{"hash":"H","space":["Family"]}}`, false},
	} {
		if err := os.WriteFile(path, []byte(strings.ReplaceAll(tt.json, `"H"`, `"`+dummyHash+`"`)), 0o600); err != nil {
			t.Fatal(err)
		}
		users, spaces, err := loadUsers(path)
		if (err == nil) != tt.ok {
			t.Errorf("%s: %v", tt.json, err)
		}
		if tt.ok && (len(users) != 2 || !slices.Equal(spaces["ann"], []string{"Family"}) || spaces["bob"] != nil) {
			t.Errorf("%s: %v %v", tt.json, users, spaces)
		}
	}
}
