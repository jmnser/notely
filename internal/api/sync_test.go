package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func writeNote(t *testing.T, dir, rel, body string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// remoteFiles returns the files of branch main in the bare repository at dir
// and the number of commits.
func remoteFiles(t *testing.T, dir string) ([]string, int) {
	t.Helper()
	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := repo.Reference("refs/heads/main", true)
	if err != nil {
		t.Fatal(err)
	}
	c, err := repo.CommitObject(ref.Hash())
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	tree, _ := c.Tree()
	_ = tree.Files().ForEach(func(f *object.File) error {
		files = append(files, f.Name)
		return nil
	})
	n := 0
	iter, _ := repo.Log(&git.LogOptions{From: ref.Hash()})
	_ = iter.ForEach(func(*object.Commit) error { n++; return nil })
	slices.Sort(files)
	return files, n
}

func TestSync(t *testing.T) {
	tmp := t.TempDir()
	users, remote := filepath.Join(tmp, "users"), filepath.Join(tmp, "remote.git")
	notes := filepath.Join(users, "ann")
	if _, err := git.PlainInit(remote, true); err != nil {
		t.Fatal(err)
	}
	writeNote(t, notes, "Inbox.md", "a")
	writeNote(t, notes, "work/Plan.md", "b")
	writeNote(t, notes, "work/Flow.excalidraw", "{}")
	writeNote(t, notes, "work/notes.txt", "foreign")
	writeNote(t, notes, ".users.json", "secret")
	writeNote(t, notes, "work/0123456789abcdef0123456789abcdef.png", "png")
	writeNote(t, notes, "work/.0123456789abcdef0123456789abcdef.png.ABC.tmp", "partial")
	writeNote(t, notes, "work/notes.txt", "other")
	writeNote(t, filepath.Join(users, "bob"), "Bob.md", "bob's")
	if err := os.Symlink("/etc/hosts", filepath.Join(notes, "leak.md")); err != nil {
		t.Fatal(err)
	}
	s := newSyncer(users, map[string]bool{"ann": true, "bob": true})
	// save skips validate, so a local path works as remote.
	if err := s.save("ann", syncSettings{Remote: remote, Branch: "main"}, "tok"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	sync := func() syncStatus {
		t.Helper()
		s.syncUser(ctx, "ann")
		st := s.status["ann"]
		if st.Error != "" {
			t.Fatalf("sync: %s", st.Error)
		}
		return st
	}

	if st := sync(); st.Commit == "" {
		t.Error("first sync pushed nothing")
	}
	if files, n := remoteFiles(t, remote); !slices.Equal(files, []string{"Inbox.md", "work/0123456789abcdef0123456789abcdef.png", "work/Flow.excalidraw", "work/Plan.md"}) || n != 1 {
		t.Errorf("remote = %v, %d commits", files, n)
	}
	if st := sync(); st.Commit != "" {
		t.Error("unchanged notes made a commit")
	}

	writeNote(t, notes, "work/Plan.md", "b2")
	for _, f := range []string{"Inbox.md", "work/Flow.excalidraw"} {
		if err := os.Remove(filepath.Join(notes, f)); err != nil {
			t.Fatal(err)
		}
	}
	sync()
	if files, n := remoteFiles(t, remote); !slices.Equal(files, []string{"work/0123456789abcdef0123456789abcdef.png", "work/Plan.md"}) || n != 2 {
		t.Errorf("after edit: remote = %v, %d commits", files, n)
	}

	// A commit made elsewhere is kept in history; its files are replaced.
	other := filepath.Join(tmp, "other")
	repo, err := git.PlainClone(other, false, &git.CloneOptions{URL: remote, ReferenceName: "refs/heads/main"})
	if err != nil {
		t.Fatal(err)
	}
	writeNote(t, other, "Elsewhere.md", "x")
	wt, _ := repo.Worktree()
	_, _ = wt.Add("Elsewhere.md")
	if _, err := wt.Commit("elsewhere", &git.CommitOptions{Author: &object.Signature{Name: "x"}}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Push(&git.PushOptions{}); err != nil {
		t.Fatal(err)
	}
	sync()
	if files, n := remoteFiles(t, remote); !slices.Equal(files, []string{"work/0123456789abcdef0123456789abcdef.png", "work/Plan.md"}) || n != 4 {
		t.Errorf("after remote commit: remote = %v, %d commits", files, n)
	}
}

func TestSyncHandler(t *testing.T) {
	users := t.TempDir()
	writeNote(t, filepath.Join(users, "ann"), "Inbox.md", "a")
	s := newSyncer(users, map[string]bool{"ann": true})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(context.WithValue(r.Context(), userKey{}, "ann"))
		s.handler().ServeHTTP(w, r)
	})
	do := func(method, body string) (int, string) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, "/api/sync", strings.NewReader(body)))
		return w.Code, w.Body.String()
	}
	if code, body := do("GET", ""); code != http.StatusOK || !strings.Contains(body, `"token":false`) {
		t.Errorf("GET unconfigured = %d %s", code, body)
	}
	for _, body := range []string{
		`{"remote":"http://github.com/a/b.git","branch":"main","token":"t"}`,
		`{"remote":"https://user:pw@github.com/a/b.git","branch":"main","token":"t"}`,
		`{"remote":"file:///etc","branch":"main","token":"t"}`,
		`{"remote":"https://github.com/a/b.git","branch":"bad..name","token":"t"}`,
		`{"remote":"https://github.com/a/b.git","branch":"main"}`,
	} {
		if code, _ := do("PUT", body); code != http.StatusBadRequest {
			t.Errorf("PUT %s = %d, want 400", body, code)
		}
	}
	if code, _ := do("PUT", `{"remote":"https://github.com/a/b.git","branch":"main","token":" ghp_secret\n"}`); code != http.StatusNoContent {
		t.Fatalf("PUT = %d", code)
	}
	token := filepath.Join(users, "ann", ".sync", "token")
	if data, err := os.ReadFile(token); err != nil || string(data) != "ghp_secret" {
		t.Errorf("token file = %q, %v", data, err)
	}
	if fi, _ := os.Stat(token); fi.Mode().Perm() != 0o600 {
		t.Errorf("token mode = %v", fi.Mode())
	}
	code, body := do("GET", "")
	var got map[string]any
	_ = json.Unmarshal([]byte(body), &got)
	if code != http.StatusOK || got["token"] != true || strings.Contains(body, "ghp_secret") || got["branch"] != "main" {
		t.Errorf("GET = %d %s", code, body)
	}
	if code, _ := do("PUT", `{"remote":"https://github.com/a/c.git","branch":"notes"}`); code != http.StatusNoContent {
		t.Errorf("PUT keeping token = %d", code)
	}
	if data, _ := os.ReadFile(token); string(data) != "ghp_secret" {
		t.Error("token not kept")
	}
	if code, _ := do("PUT", `{"remote":"https://gitlab.com/a/c.git","branch":"notes"}`); code != http.StatusBadRequest {
		t.Errorf("PUT new host without token = %d, want 400", code)
	}
	if code, _ := do("DELETE", ""); code != http.StatusNoContent {
		t.Errorf("DELETE = %d", code)
	}
	if _, err := os.Stat(filepath.Join(users, "ann", ".sync")); !os.IsNotExist(err) {
		t.Error("DELETE left files")
	}
	if _, err := os.Stat(filepath.Join(users, "ann", "Inbox.md")); err != nil {
		t.Errorf("DELETE removed notes: %v", err)
	}
}

func TestSyncRemoteChangeAndRemovedUser(t *testing.T) {
	tmp := t.TempDir()
	notes, a, b := filepath.Join(tmp, "users", "ann"), filepath.Join(tmp, "a.git"), filepath.Join(tmp, "b.git")
	for _, r := range []string{a, b} {
		if _, err := git.PlainInit(r, true); err != nil {
			t.Fatal(err)
		}
	}
	writeNote(t, notes, "Inbox.md", "a")
	s := newSyncer(filepath.Dir(notes), map[string]bool{"ann": true})
	ctx := context.Background()
	for i, remote := range []string{a, a, b} {
		if i == 1 {
			writeNote(t, notes, "Inbox.md", "b") // a second commit in a only
		}
		if err := s.save("ann", syncSettings{Remote: remote, Branch: "main"}, "tok"); err != nil {
			t.Fatal(err)
		}
		s.syncUser(ctx, "ann")
		if st := s.status["ann"]; st.Error != "" {
			t.Fatal(st.Error)
		}
	}
	if _, n := remoteFiles(t, b); n != 1 {
		t.Errorf("new repository got %d commits, want 1 without the old history", n)
	}

	s.users = map[string]bool{} // ann removed from the users file
	writeNote(t, notes, "Inbox.md", "c")
	s.syncUser(ctx, "ann")
	if _, n := remoteFiles(t, b); n != 1 {
		t.Errorf("removed user synced: %d commits", n)
	}
}

func TestSyncRequest(t *testing.T) {
	s := newSyncer(t.TempDir(), map[string]bool{"ann": true, "bob": true})
	s.request("ann")
	s.request("ann")
	s.request("bob")
	if len(s.runs) != 2 {
		t.Errorf("queued %d syncs, want 2", len(s.runs))
	}
}

func TestBackupTransportRefusesInternal(t *testing.T) {
	for addr, refused := range map[string]bool{
		"127.0.0.1:443": true, "[::1]:443": true, "169.254.169.254:443": true, "[::ffff:127.0.0.1]:443": true,
		"0.0.0.0:443": true, "192.168.1.10:443": false, "140.82.112.3:443": false,
		"100.101.102.103:443": true, "[fd7a:115c:a1e0::1]:443": true,
	} {
		if err := refuseInternal("tcp", addr, nil); (err != nil) != refused {
			t.Errorf("refuseInternal(%s) = %v, want refused %v", addr, err, refused)
		}
	}
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	res, err := backupTransport().RoundTrip(httptest.NewRequest(http.MethodGet, srv.URL, nil).WithContext(t.Context()))
	if err == nil {
		res.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("request to a loopback server: %v", err)
	}
}
