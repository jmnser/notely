//go:build !wasm

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	path := filepath.Join(t.TempDir(), "notely", "x.json")
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

func TestMatches(t *testing.T) {
	n := note.Note{Title: "Shopping", Space: "Home", Body: "milk and eggs"}
	for query, want := range map[string]bool{"": true, "MILK": true, "home eggs": true, "milk q4": false, "mle": false} {
		if got := matches(n, query); got != want {
			t.Errorf("matches(%q) = %v, want %v", query, got, want)
		}
	}
}

// keysOf lists the rows of the list by key, indented by depth.
func keysOf(m *model) []string {
	got := make([]string, 0, len(m.list.Items()))
	for _, i := range m.list.Items() {
		r := i.(row)
		got = append(got, strings.Repeat(" ", r.depth)+r.key())
	}
	return got
}

func press(m *model, keys ...string) {
	for _, k := range keys {
		msg := tea.KeyPressMsg{Code: []rune(k)[0], Text: k}
		switch k {
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		case "left":
			msg = tea.KeyPressMsg{Code: tea.KeyLeft}
		case "right":
			msg = tea.KeyPressMsg{Code: tea.KeyRight}
		}
		drain(m, m.key(msg))
	}
}

func TestTree(t *testing.T) {
	m := signedIn(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	for _, n := range []note.Note{
		{ID: "inbox", Title: "Inbox"},
		{ID: "plan", Space: "Work", Title: "Plan", Body: "milk"},
		{ID: "q4", Space: "Work/Projects", Title: "Q4"},
	} {
		drain(m, m.change(n))
	}
	want := []string{"space:", " note:inbox", "space:Work", " space:Work/Projects", "  note:q4", " note:plan"}
	if got := keysOf(m); !slices.Equal(got, want) {
		t.Fatalf("rows = %q, want %q", got, want)
	}

	m.reselect("note:q4")
	press(m, "left") // to its space
	if m.selectKey != "space:Work/Projects" {
		t.Fatalf("left on a note selected %s", m.selectKey)
	}
	press(m, "left", "left") // collapse it, then to its parent
	if m.selectKey != "space:Work" || slices.Contains(keysOf(m), "  note:q4") {
		t.Fatalf("after collapsing: selected %s, rows %q", m.selectKey, keysOf(m))
	}
	press(m, "enter") // collapses Work
	if got := keysOf(m); !slices.Equal(got, []string{"space:", " note:inbox", "space:Work"}) {
		t.Fatalf("rows with Work collapsed = %q", got)
	}

	press(m, "/", "m", "i", "l", "k", "enter") // search shows matches in their open spaces
	if got := keysOf(m); !slices.Equal(got, []string{"space:Work", " note:plan"}) {
		t.Fatalf("rows while searching = %q", got)
	}
	press(m, "esc")
	if got := keysOf(m); len(got) != 3 || m.search.Value() != "" {
		t.Fatalf("rows after clearing the search = %q", got)
	}

	m.reselect("space:Work")
	press(m, "n")
	if m.input.Value() != "Work/" {
		t.Fatalf("new note in a space starts with %q", m.input.Value())
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
	m := newModel(c, s, config{})
	if m.signIn(); m.screen != loginScreen {
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
	if len(m.state.Dirty) != 0 || len(m.list.Items()) != 4 { // two notes in two spaces
		t.Fatalf("after sync: dirty %v, rows %q, status %q", m.state.Dirty, keysOf(m), m.status)
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
	for _, r := range "pw" { // the user name is kept
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if m.user.Value() != "joe" || m.password.Value() != "pw" {
		t.Fatalf("typed into user %q and password %q", m.user.Value(), m.password.Value())
	}
}

func TestConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notely", "config.toml")
	if c, err := loadConfig(path); err != nil || c.Server.URL != "" {
		t.Fatalf("missing config = %+v, %v", c, err)
	}
	if _, err := initConfig(path, "https://example.com", "joe; rm -rf ~"); err == nil {
		t.Fatal("init accepted a user name unfit for the shell")
	}
	if _, err := initConfig(path, "https://example.com/notely", "joe"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("config file: %v, %v", fi, err)
	}
	c, err := loadConfig(path)
	lookup, _ := keychain("joe")
	if err != nil || c.Server.URL != "https://example.com/notely" || c.Server.User != "joe" ||
		c.Server.PasswordCommand != lookup {
		t.Fatalf("loadConfig = %+v, %v", c, err)
	}
	if _, err := initConfig(path, "https://example.com", "joe"); err == nil {
		t.Fatal("init replaced the config")
	}
	if err := os.WriteFile(path, []byte("[server\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(path); err == nil {
		t.Fatal("loaded an invalid config")
	}
}

func TestConfigDir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	if dir, _ := configDir(); dir != filepath.Join("/xdg", "notely") {
		t.Errorf("configDir() = %s", dir)
	}
	t.Setenv("XDG_CONFIG_HOME", "relative")
	t.Setenv("HOME", "/home/joe")
	if dir, _ := configDir(); dir != filepath.Join("/home/joe", ".config", "notely") {
		t.Errorf("configDir() with a relative XDG_CONFIG_HOME = %s", dir)
	}
	t.Setenv("NOTELY_CONFIG", "/etc/notely.toml")
	if path, _ := configPath(); path != "/etc/notely.toml" {
		t.Errorf("configPath() = %s", path)
	}
}

func TestPasswordCommand(t *testing.T) {
	var out bytes.Buffer
	if err := passwordCommand("printf 'pw\\r\\nsecond line'", &out).Run(); err != nil {
		t.Skip("no shell:", err)
	}
	if got, err := firstLine(out.String()); got != "pw" || err != nil {
		t.Fatalf("firstLine(%q) = %q, %v", out.String(), got, err)
	}
	if _, err := firstLine("\n"); err == nil {
		t.Fatal("empty output accepted")
	}
}

func TestForServer(t *testing.T) {
	var cfg config
	cfg.Server.URL, cfg.Server.PasswordCommand = "https://example.com/notely/", "pass"
	if got := forServer(cfg, "https://example.com/notely"); got.Server.PasswordCommand != "pass" {
		t.Error("dropped the password command for the configured server")
	}
	if got := forServer(cfg, "https://evil.example"); got.Server.PasswordCommand != "" {
		t.Error("kept the password command for another server")
	}
}

func TestTitleLimitAndRejection(t *testing.T) {
	m := signedIn(t)
	press(m, "n")
	m.input.SetValue(strings.Repeat("x", maxTitle+1))
	press(m, "enter")
	if len(m.state.Notes) != 0 || !strings.Contains(m.status, "at most") {
		t.Fatalf("long title: %d notes, status %q", len(m.state.Notes), m.status)
	}
	drain(m, m.change(note.Note{ID: "a", Title: "Tabs", Space: "a\tb"})) // the server refuses control characters
	if !strings.Contains(m.status, "“Tabs” could not be saved") {
		t.Fatalf("status %q does not name the rejected note", m.status)
	}
}

func TestAutoLogin(t *testing.T) {
	m := signedIn(t)
	m.cfg.Server.User, m.cfg.Server.PasswordCommand = "joe", "printf pw"
	m.state.Session = "expired"
	drain(m, m.sync())
	if !m.autoTried || m.screen == loginScreen {
		t.Fatalf("expired session: screen %v, tried %v", m.screen, m.autoTried)
	}
	_, cmd := m.Update(passwordMsg{password: "pw"}) // as the command printed it
	drain(m, cmd)
	if m.state.Session == "" || m.screen != listScreen || m.autoTried {
		t.Fatalf("auto sign-in: screen %v, status %q", m.screen, m.status)
	}

	m.state.Session = "expired"
	drain(m, m.sync())
	_, cmd = m.Update(passwordMsg{err: errors.New("keychain locked")})
	drain(m, cmd)
	if m.screen != loginScreen || !m.password.Focused() || !strings.Contains(m.status, "keychain locked") {
		t.Fatalf("failed auto sign-in: screen %v, status %q", m.screen, m.status)
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
	return newModel(c, s, config{})
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
	m.list.Select(2) // old, below its space and the newer note
	press(m, "p")
	if r, _ := m.list.SelectedItem().(row); r.note.ID != "old" || !r.note.Pinned {
		t.Fatalf("selected %+v after pinning, want the pinned old note", r)
	}
}

func TestRenameKeepsNewerBody(t *testing.T) {
	m := signedIn(t)
	drain(m, m.change(note.Note{ID: "a", Title: "A", Body: "old"}))
	m.reselect("note:a")
	m.key(tea.KeyPressMsg{Code: 'r', Text: "r"})
	m.find("a").Body = "newer" // synced from elsewhere meanwhile
	m.input.SetValue("Work/B")
	drain(m, m.key(tea.KeyPressMsg{Code: tea.KeyEnter}))
	if n := m.find("a"); n.Body != "newer" || n.Space != "Work" || n.Title != "B" {
		t.Fatalf("renamed note = %+v", n)
	}
}

func TestLock(t *testing.T) {
	s, _ := loadState(filepath.Join(t.TempDir(), "notely", "x.json"))
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

func TestStaleLock(t *testing.T) {
	s, _ := loadState(filepath.Join(t.TempDir(), "x.json"))
	if err := os.WriteFile(s.path+".lock", []byte("999999"), 0o600); err != nil { // left by a crash
		t.Fatal(err)
	}
	unlock, err := s.lock()
	if err != nil {
		t.Fatalf("lock over a stale lock file: %v", err)
	}
	defer unlock()
	if _, err := s.lock(); err == nil || !strings.Contains(err.Error(), strconv.Itoa(os.Getpid())) {
		t.Fatalf("second lock: %v, want refused naming this process", err)
	}
}

func TestLockRace(t *testing.T) {
	s, _ := loadState(filepath.Join(t.TempDir(), "x.json"))
	var holding, overlaps atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 20 {
				if unlock, err := s.lock(); err == nil {
					if holding.Add(1) > 1 {
						overlaps.Add(1)
					}
					time.Sleep(time.Millisecond)
					holding.Add(-1)
					unlock()
				}
			}
		})
	}
	wg.Wait()
	if n := overlaps.Load(); n > 0 {
		t.Fatalf("two clients held the lock at once %d times", n)
	}
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

func TestStatePathMoves(t *testing.T) {
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config) // Linux
	t.Setenv("HOME", config)            // macOS
	if dir, _ := os.UserConfigDir(); dir != config {
		t.Skip("config directory not settable here")
	}
	old := filepath.Join(config, "notely-tui")
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	path, err := statePath("https://example.com")
	if err != nil || filepath.Dir(path) != filepath.Join(config, "notely") {
		t.Fatalf("statePath = %s, %v", path, err)
	}
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old directory: %v", err)
	}
}

func TestBuildVersion(t *testing.T) {
	if got := buildVersion(); got == "" {
		t.Fatal("empty version")
	}
	defer func(v string) { version = v }(version)
	version = "v1.2.3"
	if got := buildVersion(); got != "v1.2.3" {
		t.Fatalf("buildVersion() = %q", got)
	}
}

func TestEditorCommand(t *testing.T) {
	t.Setenv("VISUAL", " ")
	t.Setenv("EDITOR", "code -w")
	if got := editorCommand(); !slices.Equal(got, []string{"code", "-w"}) {
		t.Fatalf("editorCommand() = %q", got)
	}
}
