// Package api is the notely backend: a JSON API storing notes as Markdown
// files, with sign-on and per-user git backup. It never runs in the browser.
package api

import (
	"context"
	"fmt"
	"log"
	"maps"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Server serves /api/ and /healthz.
type Server struct {
	http.Handler
	api    http.Handler      // behind sign-on
	stores map[string]*store // per user
	shared map[string]*space // by name
	sync   *syncer
	events *events
}

// usersDir holds a directory per user with all their files: notes, images
// and git backup state.
const usersDir = "users"

// Open loads the users file, by default <dir>/.users.json, created with the
// default user if missing, each user's notes in <dir>/users/<user> and the
// shared spaces' in <dir>/shared/<space>. Notes of the former shared layout
// are moved to a user first.
func Open(dir, usersFile string) (*Server, error) {
	if usersFile == "" {
		usersFile = filepath.Join(dir, ".users.json") // dot files are not notes
	}
	users, spaces, err := loadUsers(usersFile)
	if err != nil {
		return nil, err
	}
	if err := migrate(dir, users); err != nil {
		return nil, fmt.Errorf("move notes to the per-user layout: %w", err)
	}
	srv := &Server{stores: map[string]*store{}, shared: map[string]*space{}}
	names := slices.Sorted(maps.Keys(users))
	for _, name := range names {
		if srv.stores[name], err = openStore(filepath.Join(dir, usersDir, name)); err != nil {
			return nil, err
		}
		if err := srv.stores[name].migrateAssets(); err != nil {
			return nil, fmt.Errorf("move the assets of %s to a space: %w", name, err)
		}
		for _, sp := range spaces[name] {
			if srv.shared[sp] == nil {
				st, err := openStore(filepath.Join(dir, sharedDir, sp))
				if err != nil {
					return nil, err
				}
				st.space = sp
				srv.shared[sp] = &space{name: sp, store: st}
			}
			srv.shared[sp].members = append(srv.shared[sp].members, name)
		}
	}
	views, allowed := map[string]*view{}, map[string]bool{}
	sharedDirs := map[string]map[string]string{} // user → shared space → directory
	moves := &sync.Mutex{}
	for _, name := range names {
		v := &view{user: name, own: srv.stores[name], moves: moves}
		sharedDirs[name] = map[string]string{}
		for _, sp := range spaces[name] {
			v.shared = append(v.shared, srv.shared[sp])
			sharedDirs[name][sp] = srv.shared[sp].store.dir
			warnPrivate(name, srv.stores[name], sp)
		}
		views[name], allowed[name] = v, true
	}
	srv.sync = newSyncer(filepath.Join(dir, usersDir), allowed)
	srv.sync.shared = sharedDirs
	mux, sh := http.NewServeMux(), srv.sync.handler()
	mux.Handle("/api/sync", sh)
	mux.Handle("/api/sync/", sh)
	srv.events = newEvents()
	mux.Handle("/", newHandler(func(r *http.Request) *view { return views[userOf(r)] }, srv.events))
	srv.api = mux
	srv.Handler = newAuth(users).handler(mux)
	return srv, nil
}

// warnPrivate logs if user has notes of their own in shared space sp: they
// stay private, but show in the same space.
func warnPrivate(user string, own *store, sp string) {
	n := 0
	for _, nt := range own.list() {
		if top, _, _ := strings.Cut(nt.Space, "/"); strings.EqualFold(top, sp) {
			n++
		}
	}
	if n > 0 {
		log.Printf("%s has %d private notes in the shared space %s; they stay private until moved out and back",
			user, n, sp)
	}
}

// Shutdown ends the open event streams. Call it once, when the server shuts down.
func (s *Server) Shutdown() { s.events.close() }

// RunSync backs up the notes every interval and on request until ctx is
// done, then pushes the last changes. An interval of 0 syncs on request only.
func (s *Server) RunSync(ctx context.Context, interval time.Duration) {
	s.sync.run(ctx, interval)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	s.sync.syncAll(ctx)
}

// HashPassword returns the argon2id hash of password for the users file.
func HashPassword(password string) string { return hashPassword(password) }
