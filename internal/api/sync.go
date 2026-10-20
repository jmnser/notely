package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// syncTimeout bounds one user's sync, so a remote that stops answering cannot
// hold up the others or the settings, which wait for the same lock.
const syncTimeout = 5 * time.Minute

// syncSettings is a user's git backup target. The token is kept apart, in
// its own file.
type syncSettings struct {
	Remote string `json:"remote"`
	Branch string `json:"branch"`
}

func (c syncSettings) validate() error {
	u, err := url.Parse(c.Remote)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return errors.New("repository must be an https:// URL without credentials")
	}
	if err := plumbing.NewBranchReferenceName(c.Branch).Validate(); err != nil || c.Branch == "" {
		return errors.New("invalid branch name")
	}
	return nil
}

// installTransport makes go-git, process-wide, use backupTransport for https.
var installTransport sync.Once

// backupTransport is an HTTPS transport that refuses loopback, link-local
// (such as cloud metadata at 169.254.169.254) and multicast addresses, so a
// user cannot point the backup at the server's own services. It checks the
// address dialed, after DNS resolution. Private LAN addresses stay allowed,
// for self-hosted git servers.
func backupTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone() //nolint:forcetypeassert // documented type
	d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: refuseInternal}
	t.DialContext = d.DialContext
	return t
}

func refuseInternal(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	if ip = ip.Unmap(); ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return fmt.Errorf("git backup to %s refused: loopback and link-local addresses are not allowed", ip)
	}
	return nil
}

// syncStatus is the outcome of a user's last sync.
type syncStatus struct {
	Time   time.Time `json:"time"`
	Commit string    `json:"commit,omitempty"` // pushed commit, empty if nothing changed
	Error  string    `json:"error,omitempty"`
}

// syncer pushes each user's notes to their git repository: one-way, the
// app's notes replace the branch content in a new commit. The notes are in
// <dir>/<user>; next to them, in .sync, it keeps settings.json, token (mode
// 0600) and repo, a clone the notes are mirrored into; only the notes' .md
// files and images are committed, those of the user's shared spaces in
// shared/<space>.
type syncer struct {
	dir    string                       // per-user directories
	users  map[string]bool              // users allowed to sync; state of removed users is kept but idle
	shared map[string]map[string]string // user → shared space → its directory
	runs   chan string
	work   sync.Mutex // one sync at a time

	mu     sync.Mutex
	status map[string]syncStatus
	queued map[string]bool // users in runs
}

// syncDir is the git backup state in a user's directory; dot files are not notes.
const syncDir = ".sync"

func newSyncer(dir string, users map[string]bool) *syncer {
	installTransport.Do(func() {
		client.InstallProtocol("https", githttp.NewClient(&http.Client{Transport: backupTransport()}))
	})
	return &syncer{
		users:  users,
		dir:    dir,
		runs:   make(chan string, 16),
		status: map[string]syncStatus{},
		queued: map[string]bool{},
	}
}

// run syncs all users every interval and single users on request until ctx
// is done. An interval of 0 syncs on request only.
func (s *syncer) run(ctx context.Context, interval time.Duration) {
	var tick <-chan time.Time
	if interval > 0 {
		t := time.NewTicker(interval)
		defer t.Stop()
		tick = t.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
			s.syncAll(ctx)
		case user := <-s.runs:
			s.mu.Lock()
			delete(s.queued, user)
			s.mu.Unlock()
			s.syncUser(ctx, user)
		}
	}
}

// request queues a sync of user unless one is queued already.
func (s *syncer) request(user string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.queued[user] {
		return
	}
	select {
	case s.runs <- user:
		s.queued[user] = true
	default: // the queue is full; the next interval syncs user
		log.Print("sync queue full")
	}
}

func (s *syncer) syncAll(ctx context.Context) {
	entries, err := os.ReadDir(s.dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Print(err)
	}
	for _, e := range entries {
		if e.IsDir() && validID.MatchString(e.Name()) {
			s.syncUser(ctx, e.Name())
		}
	}
}

func (s *syncer) syncUser(ctx context.Context, user string) {
	if !s.users[user] {
		return
	}
	s.work.Lock()
	defer s.work.Unlock()
	ctx, cancel := context.WithTimeout(ctx, syncTimeout)
	defer cancel()
	commit, err := s.push(ctx, user)
	if errors.Is(err, errNotConfigured) {
		return
	}
	st := syncStatus{Time: time.Now().UTC(), Commit: commit}
	if err != nil {
		st.Error = err.Error()
		log.Printf("sync %s: %v", user, err)
	}
	s.mu.Lock()
	s.status[user] = st
	s.mu.Unlock()
}

var errNotConfigured = errors.New("git backup not configured")

const origin = "origin"

// root opens the per-user directories, creating them. Sync state is only
// accessed through it, so no path can leave it.
func (s *syncer) root() (*os.Root, error) {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, err
	}
	return os.OpenRoot(s.dir)
}

func (s *syncer) settings(user string) (c syncSettings, hasToken bool, err error) {
	root, err := s.root()
	if err != nil {
		return c, false, err
	}
	defer root.Close() //nolint:errcheck
	data, err := root.ReadFile(path.Join(user, syncDir, "settings.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return c, false, errNotConfigured
	}
	if err == nil {
		err = json.Unmarshal(data, &c)
	}
	_, terr := root.Stat(path.Join(user, syncDir, "token"))
	return c, terr == nil, err
}

func (s *syncer) token(user string) (string, error) {
	root, err := s.root()
	if err != nil {
		return "", err
	}
	defer root.Close() //nolint:errcheck
	data, err := root.ReadFile(path.Join(user, syncDir, "token"))
	return strings.TrimSpace(string(data)), err
}

// save stores settings and, unless empty, the token. A new repository drops
// the clone, so the old repository's history is not pushed to it.
func (s *syncer) save(user string, c syncSettings, token string) error {
	s.work.Lock()
	defer s.work.Unlock()
	old, _, err := s.settings(user)
	if err != nil && !errors.Is(err, errNotConfigured) {
		return err
	}
	root, err := s.root()
	if err != nil {
		return err
	}
	defer root.Close() //nolint:errcheck
	if old.Remote != c.Remote {
		if err := root.RemoveAll(path.Join(user, syncDir, "repo")); err != nil {
			return err
		}
	}
	if err := root.MkdirAll(path.Join(user, syncDir), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if token != "" {
		if err := writeFile(root, path.Join(user, syncDir, "token"), []byte(token)); err != nil {
			return err
		}
	}
	return writeFile(root, path.Join(user, syncDir, "settings.json"), data)
}

// remove deletes a user's settings, token and clone.
func (s *syncer) remove(user string) error {
	s.work.Lock()
	defer s.work.Unlock()
	s.mu.Lock()
	delete(s.status, user)
	s.mu.Unlock()
	root, err := s.root()
	if err != nil {
		return err
	}
	defer root.Close() //nolint:errcheck
	return root.RemoveAll(path.Join(user, syncDir))
}

// push mirrors the notes onto the user's branch and pushes a commit if they
// changed. It returns the pushed commit.
func (s *syncer) push(ctx context.Context, user string) (string, error) {
	c, hasToken, err := s.settings(user)
	if err != nil {
		return "", err
	}
	if !hasToken {
		return "", errors.New("no token")
	}
	token, err := s.token(user)
	if err != nil {
		return "", err
	}
	auth := &githttp.BasicAuth{Username: "x-access-token", Password: token}
	branch := plumbing.NewBranchReferenceName(c.Branch)
	tracking := plumbing.NewRemoteReferenceName(origin, c.Branch)

	repo, err := openRepo(filepath.Join(s.dir, user, syncDir, "repo"), c.Remote)
	if err != nil {
		return "", err
	}
	err = repo.FetchContext(ctx, &git.FetchOptions{
		RemoteName: origin,
		Auth:       auth,
		RefSpecs:   []config.RefSpec{config.RefSpec("+" + branch + ":" + tracking)},
		Force:      true,
	})
	var noRef git.NoMatchingRefSpecError
	switch {
	case err == nil, errors.Is(err, git.NoErrAlreadyUpToDate):
	case errors.Is(err, transport.ErrEmptyRemoteRepository), errors.As(err, &noRef):
		// The branch is created by the push.
	default:
		return "", fmt.Errorf("fetch: %w", err)
	}

	// Build on the remote branch, or on local commits if it does not exist yet.
	if ref, err := repo.Reference(tracking, true); err == nil {
		if err := repo.Storer.SetReference(plumbing.NewHashReference(branch, ref.Hash())); err != nil {
			return "", err
		}
	}
	if err := repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, branch)); err != nil {
		return "", err
	}
	wt, err := repo.Worktree()
	if err != nil {
		return "", err
	}
	if head, err := repo.Head(); err == nil {
		if err := wt.Reset(&git.ResetOptions{Commit: head.Hash(), Mode: git.HardReset}); err != nil {
			return "", fmt.Errorf("reset: %w", err)
		}
	}
	srcs := []mirrorSource{{dir: filepath.Join(s.dir, user)}}
	for _, name := range slices.Sorted(maps.Keys(s.shared[user])) {
		srcs = append(srcs, mirrorSource{dir: s.shared[user][name], prefix: path.Join(sharedDir, name)})
	}
	if err := mirror(wt.Filesystem.Root(), srcs...); err != nil {
		return "", fmt.Errorf("copy notes: %w", err)
	}
	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		return "", fmt.Errorf("add: %w", err)
	}
	st, err := wt.Status()
	if err != nil {
		return "", err
	}
	commit := ""
	if !st.IsClean() {
		h, err := wt.Commit(fmt.Sprintf("sync: %d notes changed", len(st)), &git.CommitOptions{
			Author: &object.Signature{Name: "Notely", Email: user + "@notely.invalid", When: time.Now()},
		})
		if err != nil {
			return "", fmt.Errorf("commit: %w", err)
		}
		commit = h.String()
	}
	err = repo.PushContext(ctx, &git.PushOptions{
		RemoteName: origin,
		Auth:       auth,
		RefSpecs:   []config.RefSpec{config.RefSpec(branch + ":" + branch)},
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return "", fmt.Errorf("push: %w", err)
	}
	return commit, nil
}

// openRepo opens or creates the clone at dir with origin set to remote.
func openRepo(dir, remote string) (*git.Repository, error) {
	repo, err := git.PlainOpen(dir)
	if errors.Is(err, git.ErrRepositoryNotExists) {
		repo, err = git.PlainInit(dir, false)
	}
	if err != nil {
		return nil, err
	}
	cfg, err := repo.Config()
	if err != nil {
		return nil, err
	}
	cfg.Remotes[origin] = &config.RemoteConfig{
		Name:  origin,
		URLs:  []string{remote},
		Fetch: []config.RefSpec{"+refs/heads/*:refs/remotes/origin/*"},
	}
	return repo, repo.SetConfig(cfg)
}

// copyFile copies rel of from to out in to, unless to holds it already.
func copyFile(from, to *os.Root, rel, out string) error {
	if imageName.MatchString(path.Base(rel)) {
		if _, err := to.Stat(out); err == nil {
			return nil // an image's name is its content
		}
	}
	data, err := from.ReadFile(rel)
	if err != nil {
		return err
	}
	if old, err := to.ReadFile(out); err == nil && bytes.Equal(old, data) {
		return nil
	}
	if err := to.MkdirAll(path.Dir(out), 0o700); err != nil {
		return err
	}
	return to.WriteFile(out, data, 0o600)
}

// mirrorSource is a directory mirrored to prefix.
type mirrorSource struct{ dir, prefix string }

// mirror makes dst hold exactly the .md files and images of srcs. Dot entries
// and symbolic links are skipped; os.Root keeps all access inside the trees.
func mirror(dst string, srcs ...mirrorSource) error {
	to, err := os.OpenRoot(dst)
	if err != nil {
		return err
	}
	defer to.Close() //nolint:errcheck

	files := map[string]bool{}
	for _, src := range srcs {
		if err := mirrorFrom(src, to, files); err != nil {
			return err
		}
	}
	var dirs []string
	err = fs.WalkDir(to.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case rel == ".":
			return nil
		case d.IsDir() && d.Name() == ".git":
			return fs.SkipDir
		case d.IsDir():
			dirs = append(dirs, rel)
			return nil
		case files[rel]:
			return nil
		}
		return to.Remove(rel)
	})
	for _, dir := range slices.Backward(dirs) {
		_ = to.Remove(dir) // fails while not empty
	}
	return err
}

// mirrorFrom copies the .md files and images of src to to, adding their
// paths to files.
func mirrorFrom(src mirrorSource, to *os.Root, files map[string]bool) error {
	from, err := os.OpenRoot(src.dir)
	if err != nil {
		return err
	}
	defer from.Close() //nolint:errcheck
	return fs.WalkDir(from.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		switch {
		case errors.Is(err, fs.ErrNotExist): // moved by an edit meanwhile; the next sync has it
			return nil
		case err != nil:
			return err
		case rel != "." && strings.HasPrefix(d.Name(), "."):
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		case !d.Type().IsRegular():
			return nil
		case path.Ext(rel) != noteExt && !imageName.MatchString(d.Name()):
			return nil
		}
		out := path.Join(src.prefix, rel)
		if files[out] {
			log.Printf("backup: left out %s of %s: the user's own space %s has it", rel, src.dir, path.Dir(out))
			return nil
		}
		files[out] = true
		if err := copyFile(from, to, rel, out); errors.Is(err, fs.ErrNotExist) {
			delete(files, out) // moved by an edit meanwhile; the next sync has it
		} else if err != nil {
			return err
		}
		return nil
	})
}

// handler serves the signed-on user's git backup settings:
// GET, PUT and DELETE /api/sync, POST /api/sync/run.
func (s *syncer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/sync", func(w http.ResponseWriter, r *http.Request) {
		user := userOf(r)
		c, hasToken, err := s.settings(user)
		if err != nil && !errors.Is(err, errNotConfigured) {
			serverError(w, err)
			return
		}
		out := struct {
			syncSettings
			Token bool        `json:"token"` // a token is stored; it is never returned
			Last  *syncStatus `json:"last,omitempty"`
		}{syncSettings: c, Token: hasToken}
		s.mu.Lock()
		if st, ok := s.status[user]; ok {
			out.Last = &st
		}
		s.mu.Unlock()
		writeJSON(w, out)
	})
	mux.HandleFunc("PUT /api/sync", func(w http.ResponseWriter, r *http.Request) {
		user := userOf(r)
		var in struct {
			syncSettings
			Token string `json:"token"` // empty keeps the stored token
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&in); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		in.Remote, in.Branch = strings.TrimSpace(in.Remote), strings.TrimSpace(in.Branch)
		in.Token = strings.TrimSpace(in.Token)
		if err := in.validate(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// The stored token only goes to the host it was given for.
		if old, hasToken, _ := s.settings(user); in.Token == "" && (!hasToken || host(old.Remote) != host(in.Remote)) {
			http.Error(w, "a token is required", http.StatusBadRequest)
			return
		}
		if err := s.save(user, in.syncSettings, in.Token); err != nil {
			serverError(w, err)
			return
		}
		s.request(user)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /api/sync", func(w http.ResponseWriter, r *http.Request) {
		if err := s.remove(userOf(r)); err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/sync/run", func(w http.ResponseWriter, r *http.Request) {
		s.request(userOf(r))
		w.WriteHeader(http.StatusAccepted)
	})
	return mux
}

// host returns the lower-case host of a repository URL.
func host(remote string) string {
	u, err := url.Parse(remote)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Host)
}

// writeFile replaces name in root atomically, readable by the owner only. The
// temporary file is a dot file with a random name, so concurrent writes of
// the same name do not mix.
func writeFile(root *os.Root, name string, data []byte) error {
	tmp := path.Join(path.Dir(name), "."+path.Base(name)+"."+rand.Text()+".tmp")
	if err := root.WriteFile(tmp, data, 0o600); err != nil {
		_ = root.Remove(tmp)
		return err
	}
	if err := root.Rename(tmp, name); err != nil {
		_ = root.Remove(tmp)
		return err
	}
	return nil
}
