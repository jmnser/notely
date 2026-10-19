//go:build !wasm

// Command notely is a terminal client for a notely server. It keeps a copy
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
	server := flag.String("server", "", "URL notely-server is served under, e.g. https://host/notely "+
		"($NOTELY_SERVER, else the config's server.url, else http://localhost:8000)")
	user := flag.String("user", "", "with -init: the user name (default the login name)")
	initialize := flag.Bool("init", false, "write a starter config that reads the password from the system keychain")
	logout := flag.Bool("logout", false, "sign out and remove the notes from this device")
	force := flag.Bool("force", false, "with -logout: also when changes are not synced or the server is unreachable")
	flag.Parse()
	if err := run(*server, *user, *initialize, *logout, *force); err != nil {
		fmt.Fprintln(os.Stderr, "notely:", err)
		os.Exit(1)
	}
}

func run(server, user string, initialize, logout, force bool) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	cfg, err := loadConfig(path)
	if err != nil {
		return err
	}
	server = cmp.Or(server, os.Getenv("NOTELY_SERVER"), cfg.Server.URL, "http://localhost:8000")
	c, err := newClient(server)
	if err != nil {
		return err
	}
	if initialize {
		user = cmp.Or(user, os.Getenv("USER"), os.Getenv("USERNAME"))
		store, err := initConfig(path, c.base, user)
		if err != nil {
			return err
		}
		fmt.Printf("wrote %s\n", path)
		if store != "" {
			fmt.Printf("\nStore the password in the system keychain with:\n  %s\n", store)
		} else {
			fmt.Println("\nSet server.password_command to a command that prints the password.")
		}
		return nil
	}
	state, err := statePath(c.base)
	if err != nil {
		return err
	}
	s, err := loadState(state)
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
	_, err = tea.NewProgram(newModel(c, s, cfg)).Run()
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
