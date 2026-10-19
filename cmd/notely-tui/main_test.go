//go:build !wasm

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	for server, ok := range map[string]bool{
		"https://example.com/notely/": true,
		"http://localhost:8000":       true,
		"http://127.0.0.1:8000":       true,
		"http://[::1]:8000":           true,
		"http://example.com":          false,
		"https://joe:pw@example.com":  false,
		"ftp://example.com":           false,
		"example.com":                 false,
	} {
		c, err := newClient(server)
		if (err == nil) != ok {
			t.Errorf("newClient(%q) error = %v, want ok %v", server, err, ok)
		}
		if err == nil && c.base == "https://example.com/notely/" {
			t.Errorf("newClient(%q) kept the trailing slash", server)
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
	_, cmd := m.Update(editedMsg{id: "a", body: "# Q4\n"})
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
}
