//go:build !wasm

// Command notely-tui is a terminal client for a notely server. It keeps a copy
// of the notes, so they can be read and edited offline, and syncs changes in
// the background.
package main

import (
	"cmp"
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"
)

func main() {
	server := flag.String("server", cmp.Or(os.Getenv("NOTELY_SERVER"), "http://localhost:8000"),
		"URL notely is served under, e.g. https://host/notely ($NOTELY_SERVER)")
	logout := flag.Bool("logout", false, "sign out and remove the notes from this device")
	force := flag.Bool("force", false, "with -logout: also when changes are not synced or the server is unreachable")
	flag.Parse()
	if err := run(*server, *logout, *force); err != nil {
		fmt.Fprintln(os.Stderr, "notely-tui:", err)
		os.Exit(1)
	}
}

func run(server string, logout, force bool) error {
	c, err := newClient(server)
	if err != nil {
		return err
	}
	path, err := statePath(c.base)
	if err != nil {
		return err
	}
	s, err := loadState(path)
	if err != nil {
		return err
	}
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if logout {
		return signOut(c, s, force)
	}
	_, err = tea.NewProgram(newModel(c, s)).Run()
	return err
}

// signOut ends the session and removes the state. Unless forced, it keeps
// both when changes would be lost or the server cannot be reached.
func signOut(c *client, s *state, force bool) error {
	if n := len(s.Dirty) + len(s.Deleted); n > 0 && !force {
		return fmt.Errorf("%d changes not synced; sync them first or add -force", n)
	}
	if s.Session != "" {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if err := c.logout(ctx, s.Session); err != nil && !force {
			return fmt.Errorf("sign out on the server: %w; add -force to remove the notes anyway", err)
		}
	}
	return s.remove()
}

// randID returns a new note ID; IDs are made by clients so notes can be created offline.
func randID() string { return rand.Text() }
