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
	"time"
)

// Server serves /api/ and /healthz.
type Server struct {
	http.Handler
	stores map[string]*store // per user
	sync   *syncer
	events *events
}

// usersDir holds a directory per user with all their files: notes, images
// and git backup state.
const usersDir = "users"

// Open loads the users file, by default <dir>/.users.json, created with the
// default user if missing, and each user's notes in <dir>/users/<user>. Notes
// of the former shared layout are moved to a user first.
func Open(dir, usersFile string) (*Server, error) {
	if usersFile == "" {
		usersFile = filepath.Join(dir, ".users.json") // dot files are not notes
	}
	users, err := loadUsers(usersFile)
	if err != nil {
		return nil, err
	}
	if err := migrate(dir, users); err != nil {
		return nil, fmt.Errorf("move notes to the per-user layout: %w", err)
	}
	stores, names := map[string]*store{}, map[string]bool{}
	for name := range users {
		if stores[name], err = openStore(filepath.Join(dir, usersDir, name)); err != nil {
			return nil, err
		}
		names[name] = true
	}
	sy := newSyncer(filepath.Join(dir, usersDir), names)
	mux, sh := http.NewServeMux(), sy.handler()
	mux.Handle("/api/sync", sh)
	mux.Handle("/api/sync/", sh)
	ev := newEvents()
	mux.Handle("/", newHandler(func(r *http.Request) *store { return stores[userOf(r)] }, ev))
	return &Server{Handler: newAuth(users).handler(mux), stores: stores, sync: sy, events: ev}, nil
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

// RunCleanup deletes unreferenced images every Sunday at 23:00 local time
// until ctx is done.
func (s *Server) RunCleanup(ctx context.Context) {
	for {
		t := time.NewTimer(time.Until(nextCleanup(time.Now())))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case now := <-t.C:
			for _, name := range slices.Sorted(maps.Keys(s.stores)) {
				n, err := s.stores[name].cleanImages(now)
				if err != nil {
					log.Printf("clean images of %s: %v", name, err)
				}
				if n > 0 {
					log.Printf("deleted %d unreferenced images of %s", n, name)
				}
			}
		}
	}
}

// HashPassword returns the argon2id hash of password for the users file.
func HashPassword(password string) string { return hashPassword(password) }
