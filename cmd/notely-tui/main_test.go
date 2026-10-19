//go:build !wasm

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jmnser/notely/internal/api"
	"github.com/jmnser/notely/internal/note"
)

// backend starts a notely API with the user joe, password pw.
func backend(t *testing.T) *client {
	t.Helper()
	users, _ := json.Marshal(map[string]string{"joe": api.HashPassword("pw")})
	usersFile := filepath.Join(t.TempDir(), "users.json")
	if err := os.WriteFile(usersFile, users, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := api.Open(t.TempDir(), usersFile)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	c, err := newClient(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNewClient(t *testing.T) {
	for server, want := range map[string]string{
		"https://example.com/notely/": "https://example.com/notely",
		"https://example.com/?a#b":    "https://example.com",
		"http://localhost:8000":       "http://localhost:8000",
		"http://127.0.0.1:8000":       "http://127.0.0.1:8000",
		"http://[::1]:8000":           "http://[::1]:8000",
		"http://example.com":          "",
		"https://joe:pw@example.com":  "",
		"ftp://example.com":           "",
		"example.com":                 "",
	} {
		c, err := newClient(server)
		switch {
		case want == "" && err == nil:
			t.Errorf("newClient(%q) succeeded", server)
		case want != "" && (err != nil || c.base != want):
			t.Errorf("newClient(%q) = %v, %v, want %s", server, c, err, want)
		}
	}
}

func TestClient(t *testing.T) {
	c, ctx := backend(t), context.Background()
	if _, err := c.login(ctx, "joe", "wrong"); err == nil {
		t.Fatal("login with a wrong password succeeded")
	}
	token, err := c.login(ctx, "joe", "pw")
	if err != nil {
		t.Fatal(err)
	}
	push := []note.Note{{ID: "a", Title: "A"}, {ID: "bad id", Title: "B"}}
	notes, rejected, err := c.exchange(ctx, token, push, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || notes[0].ID != "a" || rejected["bad id"] == nil {
		t.Fatalf("exchange = %v, rejected %v", notes, rejected)
	}
	if notes, _, err = c.exchange(ctx, token, nil, []string{"a"}); err != nil || len(notes) != 0 {
		t.Fatalf("exchange after delete = %v, %v", notes, err)
	}
	if err := c.logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.exchange(ctx, token, nil, nil); !errors.Is(err, errSignedOut) {
		t.Fatalf("exchange after logout: %v, want %v", err, errSignedOut)
	}
}

func TestState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notely-tui", "x.json")
	s, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Session, s.Notes = "token", []note.Note{{ID: "a"}}
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("state file: %v, %v", fi, err)
	}
	got, err := loadState(path)
	if err != nil || got.Session != "token" || len(got.Notes) != 1 {
		t.Fatalf("loadState = %+v, %v", got, err)
	}
	if err := got.remove(); err != nil {
		t.Fatal(err)
	}
	if err := got.remove(); err != nil {
		t.Fatalf("removing a missing state: %v", err)
	}
}

func TestFilter(t *testing.T) {
	targets := []string{"Shopping\nHome\nmilk and eggs", "Roadmap\nWork\nQ4 milestones"}
	for term, want := range map[string]int{"": 2, "MILK": 1, "work q4": 1, "milk q4": 0, "mle": 0} {
		if got := len(filter(term, targets)); got != want {
			t.Errorf("filter(%q) = %d matches, want %d", term, got, want)
		}
	}
}

// drain applies cmd's messages to m, as the program would, until none are
// left. Commands needing a terminal, such as the editor, are not run.
func drain(m *model, cmd tea.Cmd) {
	for queue := []tea.Cmd{cmd}; len(queue) > 0; queue = queue[1:] {
		if queue[0] == nil {
			continue
		}
		switch msg := queue[0]().(type) {
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case syncedMsg, loggedInMsg, editedMsg:
			_, next := m.Update(msg)
			queue = append(queue, next)
		}
	}
}

func TestModel(t *testing.T) {
	c := backend(t)
	s, _ := loadState(filepath.Join(t.TempDir(), "state.json"))
	m := newModel(c, s)
	if m.screen != loginScreen {
		t.Fatalf("screen = %v, want sign-in", m.screen)
	}
	m.user.SetValue("joe")
	m.password.SetValue("pw")
	m.user.Blur()
	drain(m, m.key(tea.KeyPressMsg{Code: tea.KeyEnter}))
	if m.screen != listScreen || m.state.Session == "" {
		t.Fatalf("after sign-in: screen %v, status %q", m.screen, m.status)
	}

	drain(m, m.change(note.Note{ID: "a", Space: "Work", Title: "Roadmap"}))
	drain(m, m.change(note.Note{ID: "b", Title: "Inbox"}))
	if len(m.state.Dirty) != 0 || len(m.list.Items()) != 2 {
		t.Fatalf("after sync: dirty %v, %d items, status %q", m.state.Dirty, len(m.list.Items()), m.status)
	}
	_, cmd := m.Update(editedMsg{note: *m.find("a"), body: "# Q4\n"})
	drain(m, cmd)
	drain(m, m.delete("b"))

	remote, _, err := c.exchange(context.Background(), m.state.Session, nil, nil)
	if err != nil || len(remote) != 1 || remote[0].Body != "# Q4\n" {
		t.Fatalf("backend notes = %+v, %v", remote, err)
	}
	if saved, _ := loadState(s.path); len(saved.Notes) != 1 || len(saved.Deleted) != 0 {
		t.Fatalf("saved state = %+v", saved)
	}

	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.open("a")
	if m.view.GetContent() == "" {
		t.Fatal("open note is not rendered")
	}

	m.state.Session = "expired"
	drain(m, m.sync())
	if m.screen != loginScreen || m.state.Session != "" {
		t.Fatalf("expired session: screen %v", m.screen)
	}
	m.user.SetValue("")
	for _, r := range "joe" {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if m.user.Value() != "joe" || m.password.Value() != "" {
		t.Fatalf("typed into user %q and password %q", m.user.Value(), m.password.Value())
	}
}

// signedIn returns a model signed on to a new backend.
func signedIn(t *testing.T) *model {
	t.Helper()
	c := backend(t)
	token, err := c.login(context.Background(), "joe", "pw")
	if err != nil {
		t.Fatal(err)
	}
	s, _ := loadState(filepath.Join(t.TempDir(), "state.json"))
	s.Session = token
	return newModel(c, s)
}

func TestEditDeletedElsewhere(t *testing.T) {
	m := signedIn(t)
	drain(m, m.change(note.Note{ID: "a", Title: "A"}))
	opened := *m.find("a")
	if _, _, err := m.client.exchange(context.Background(), m.state.Session, nil, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	drain(m, m.sync())
	if m.find("a") != nil {
		t.Fatal("note deleted elsewhere is still here")
	}
	_, cmd := m.Update(editedMsg{note: opened, body: "kept"})
	drain(m, cmd)
	if n := m.find("a"); n == nil || n.Body != "kept" || len(m.state.Dirty) != 0 {
		t.Fatalf("edit after delete: %+v, dirty %v", n, m.state.Dirty)
	}
}

// syncedOf runs cmd and returns its sync result.
func syncedOf(t *testing.T, cmd tea.Cmd) syncedMsg {
	t.Helper()
	for queue := []tea.Cmd{cmd}; len(queue) > 0; queue = queue[1:] {
		if queue[0] == nil {
			continue
		}
		switch msg := queue[0]().(type) {
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case syncedMsg:
			return msg
		}
	}
	t.Fatal("no sync")
	return syncedMsg{}
}

func TestChangeDuringSync(t *testing.T) {
	m := signedIn(t)
	drain(m, m.change(note.Note{ID: "a", Title: "A"}))
	drain(m, m.change(note.Note{ID: "b", Title: "B"}))

	inFlight := m.change(note.Note{ID: "a", Title: "A1"})
	pending := m.change(note.Note{ID: "a", Title: "A2"}) // waits for the sync in flight
	m.delete("b")
	if pending != nil {
		t.Fatal("second sync started while one runs")
	}
	_, next := m.Update(syncedOf(t, inFlight))
	if _, ok := m.state.Dirty["a"]; !ok || len(m.state.Deleted) != 1 || m.find("a").Title != "A2" {
		t.Fatalf("after the first sync: dirty %v, deleted %v", m.state.Dirty, m.state.Deleted)
	}
	drain(m, next)
	remote, _, _ := m.client.exchange(context.Background(), m.state.Session, nil, nil)
	if len(remote) != 1 || remote[0].Title != "A2" || len(m.state.Dirty) != 0 || len(m.state.Deleted) != 0 {
		t.Fatalf("backend %+v, dirty %v, deleted %v", remote, m.state.Dirty, m.state.Deleted)
	}
}

func TestRejectedAndOffline(t *testing.T) {
	m := signedIn(t)
	drain(m, m.change(note.Note{ID: "a", Title: strings.Repeat("x", 201)}))
	if _, ok := m.state.Dirty["a"]; !ok || !strings.Contains(m.status, "could not be saved") {
		t.Fatalf("rejected note: dirty %v, status %q", m.state.Dirty, m.status)
	}
	drain(m, m.delete("a"))
	m.state.Deleted = []string{"bad id"}
	drain(m, m.sync())
	if len(m.state.Deleted) != 0 {
		t.Fatalf("rejected delete stays: %v", m.state.Deleted)
	}

	m.client.base = "http://127.0.0.1:1"
	drain(m, m.change(note.Note{ID: "b", Title: "B"}))
	if _, ok := m.state.Dirty["b"]; !ok || !strings.HasPrefix(m.status, "Offline") || m.find("b") == nil {
		t.Fatalf("offline: dirty %v, status %q", m.state.Dirty, m.status)
	}
}

func TestSelectionFollowsNote(t *testing.T) {
	m := signedIn(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	drain(m, m.change(note.Note{ID: "old", Title: "Old"}))
	drain(m, m.change(note.Note{ID: "new", Title: "New"}))
	m.list.Select(1) // old, below the newer note
	drain(m, m.key(tea.KeyPressMsg{Code: 'p', Text: "p"}))
	if i, _ := m.list.SelectedItem().(item); i.ID != "old" || !i.Pinned {
		t.Fatalf("selected %+v after pinning, want the pinned old note", i)
	}
}

func TestRenameKeepsNewerBody(t *testing.T) {
	m := signedIn(t)
	drain(m, m.change(note.Note{ID: "a", Title: "A", Body: "old"}))
	m.key(tea.KeyPressMsg{Code: 'r', Text: "r"})
	m.find("a").Body = "newer" // synced from elsewhere meanwhile
	m.input.SetValue("Work/B")
	drain(m, m.key(tea.KeyPressMsg{Code: tea.KeyEnter}))
	if n := m.find("a"); n.Body != "newer" || n.Space != "Work" || n.Title != "B" {
		t.Fatalf("renamed note = %+v", n)
	}
}

func TestLock(t *testing.T) {
	s, _ := loadState(filepath.Join(t.TempDir(), "notely-tui", "x.json"))
	unlock, err := s.lock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.lock(); err == nil {
		t.Fatal("second lock succeeded")
	}
	unlock()
	if unlock, err = s.lock(); err != nil {
		t.Fatal(err)
	}
	unlock()
}

func TestSignOut(t *testing.T) {
	m := signedIn(t)
	m.state.Dirty["a"] = 1
	if err := m.state.save(); err != nil {
		t.Fatal(err)
	}
	if err := signOut(m.client, m.state, false); err == nil {
		t.Fatal("signed out with pending changes")
	}
	if err := signOut(m.client, m.state, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.state.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state after sign-out: %v", err)
	}
}

func TestEditorCommand(t *testing.T) {
	t.Setenv("VISUAL", " ")
	t.Setenv("EDITOR", "code -w")
	if got := editorCommand(); !slices.Equal(got, []string{"code", "-w"}) {
		t.Fatalf("editorCommand() = %q", got)
	}
}
