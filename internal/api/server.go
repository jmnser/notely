// Package api is the notely backend: a JSON API storing notes as Markdown
// files, with sign-on and per-user git backup. It never runs in the browser.
package api

import (
	"context"
	"net/http"
	"path/filepath"
	"time"
)

// Server serves /api/ and /healthz.
type Server struct {
	http.Handler
	sync *syncer
}

// Open loads the notes in dir and the users file, by default <dir>/.users.json,
// created with the default user if missing.
func Open(dir, usersFile string) (*Server, error) {
	s, err := openStore(dir)
	if err != nil {
		return nil, err
	}
	if usersFile == "" {
		usersFile = filepath.Join(dir, ".users.json") // dot files are not notes
	}
	users, err := loadUsers(usersFile)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for name := range users {
		names[name] = true
	}
	sy := newSyncer(dir, names)
	mux, sh := http.NewServeMux(), sy.handler()
	mux.Handle("/api/sync", sh)
	mux.Handle("/api/sync/", sh)
	ih := (&images{dir: filepath.Join(dir, ".images")}).handler() // dot files are not notes
	mux.Handle("/api/images", ih)
	mux.Handle("/api/images/", ih)
	mux.Handle("/", newHandler(s))
	return &Server{Handler: newAuth(users).handler(mux), sync: sy}, nil
}

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
