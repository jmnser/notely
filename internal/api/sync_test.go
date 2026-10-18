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
	notes, remote := filepath.Join(tmp, "notes"), filepath.Join(tmp, "remote.git")
	if _, err := git.PlainInit(remote, true); err != nil {
		t.Fatal(err)
	}
	writeNote(t, notes, "Inbox.md", "a")
	writeNote(t, notes, "work/Plan.md", "b")
	writeNote(t, notes, ".users.json", "secret")
	writeNote(t, notes, ".images/0123456789abcdef0123456789abcdef.png", "png")
	writeNote(t, notes, ".images/.0123456789abcdef0123456789abcdef.png.tmp", "partial")
	if err := os.Symlink("/etc/hosts", filepath.Join(notes, "leak.md")); err != nil {
		t.Fatal(err)
	}
	s := newSyncer(notes, map[string]bool{"ann": true})
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
	if files, n := remoteFiles(t, remote); !slices.Equal(files, []string{"Inbox.md", "images/0123456789abcdef0123456789abcdef.png", "work/Plan.md"}) || n != 1 {
		t.Errorf("remote = %v, %d commits", files, n)
	}
	if st := sync(); st.Commit != "" {
		t.Error("unchanged notes made a commit")
	}

	writeNote(t, notes, "work/Plan.md", "b2")
	if err := os.Remove(filepath.Join(notes, "Inbox.md")); err != nil {
		t.Fatal(err)
	}
	sync()
	if files, n := remoteFiles(t, remote); !slices.Equal(files, []string{"images/0123456789abcdef0123456789abcdef.png", "work/Plan.md"}) || n != 2 {
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
	if files, n := remoteFiles(t, remote); !slices.Equal(files, []string{"images/0123456789abcdef0123456789abcdef.png", "work/Plan.md"}) || n != 4 {
		t.Errorf("after remote commit: remote = %v, %d commits", files, n)
	}
}

func TestSyncHandler(t *testing.T) {
	notes := t.TempDir()
	s := newSyncer(notes, map[string]bool{"ann": true})
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
	token := filepath.Join(notes, ".sync", "ann", "token")
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
	if _, err := os.Stat(filepath.Join(notes, ".sync", "ann")); !os.IsNotExist(err) {
		t.Error("DELETE left files")
	}
}

func TestSyncRemoteChangeAndRemovedUser(t *testing.T) {
	tmp := t.TempDir()
	notes, a, b := filepath.Join(tmp, "notes"), filepath.Join(tmp, "a.git"), filepath.Join(tmp, "b.git")
	for _, r := range []string{a, b} {
		if _, err := git.PlainInit(r, true); err != nil {
			t.Fatal(err)
		}
	}
	writeNote(t, notes, "Inbox.md", "a")
	s := newSyncer(notes, map[string]bool{"ann": true})
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
