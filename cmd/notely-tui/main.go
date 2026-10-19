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
	flag.Parse()
	if err := run(*server, *logout); err != nil {
		fmt.Fprintln(os.Stderr, "notely-tui:", err)
		os.Exit(1)
	}
}

func run(server string, logout bool) error {
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
	if logout {
		if s.Session != "" {
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			if err := c.logout(ctx, s.Session); err != nil {
				fmt.Fprintln(os.Stderr, "notely-tui: sign out on the server:", err)
			}
		}
		return s.remove()
	}
	_, err = tea.NewProgram(newModel(c, s)).Run()
	return err
}

// randID returns a new note ID; IDs are made by clients so notes can be created offline.
func randID() string { return rand.Text() }
