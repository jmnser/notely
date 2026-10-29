//go:build !wasm

// Command notely is a terminal client for a notely server. It keeps a copy
// of the notes, so they can be read and edited offline, and syncs changes in
// the background. With -daemon it instead keeps the notes and images in a
// directory in sync with the server.
package main

import (
	"cmp"
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"

	tea "charm.land/bubbletea/v2"
)

func main() {
	server := flag.String("server", "", "URL notely-server is served under, e.g. https://host/notely "+
		"($NOTELY_SERVER, else the config's server.url, else http://localhost:8000)")
	user := flag.String("user", "", "with -init: the user name (default the login name)")
	initialize := flag.Bool("init", false, "write a starter config that reads the password from the system keychain")
	logout := flag.Bool("logout", false, "sign out and remove the notes from this device")
	force := flag.Bool("force", false, "with -logout: also when changes are not synced or the server is unreachable")
	daemon := flag.Bool("daemon", false, "keep the notes and images in a directory in sync with the server")
	dir := flag.String("dir", "", "with -daemon: the directory (default the config's daemon.dir, else ~/Notely)")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("notely", buildVersion())
		return
	}
	var err error
	if *daemon {
		err = runSync(*server, *dir)
	} else {
		err = run(*server, *user, *initialize, *logout, *force)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "notely:", err)
		os.Exit(1)
	}
}

func run(server, user string, initialize, logout, force bool) error {
	path, cfg, c, err := setup(server)
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
	stateFile, err := statePath(c.base)
	if err != nil {
		return err
	}
	// Locked first, so the state read is not one a quitting instance replaces.
	unlock, err := (&state{path: stateFile}).lock()
	if err != nil {
		return err
	}
	defer unlock()
	s, err := loadState(stateFile)
	if err != nil {
		return err
	}
	if logout {
		return signOut(c, s, force)
	}
	// Closing the terminal quits cleanly, so the lock is removed.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGHUP, syscall.SIGTERM)
	defer stop()
	_, err = tea.NewProgram(newModel(c, s, cfg), tea.WithContext(ctx)).Run()
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// runSync runs the daemon until SIGINT or SIGTERM.
func runSync(server, dir string) error {
	_, cfg, c, err := setup(server)
	if err != nil {
		return err
	}
	if dir = cmp.Or(dir, cfg.Daemon.Dir, "~/Notely"); strings.HasPrefix(dir, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		dir = filepath.Join(home, dir[2:])
	}
	stateFile, err := statePath(c.base)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runDaemon(ctx, c, cfg, dir, strings.TrimSuffix(stateFile, ".json")+".daemon.json")
}

// setup reads the config and returns its path, the config and the client for
// server, else the configured one.
func setup(server string) (string, config, *client, error) {
	path, err := configPath()
	if err != nil {
		return "", config{}, nil, err
	}
	cfg, err := loadConfig(path)
	if err != nil {
		return "", config{}, nil, err
	}
	server = cmp.Or(server, os.Getenv("NOTELY_SERVER"), cfg.Server.URL, "http://localhost:8000")
	c, err := newClient(server)
	if err != nil {
		return "", config{}, nil, err
	}
	return path, forServer(cfg, c.base), c, nil
}

// forServer drops the password command unless base is the configured
// server: its password is for that server only.
func forServer(cfg config, base string) config {
	if c, err := newClient(cfg.Server.URL); err != nil || c.base != base {
		cfg.Server.PasswordCommand = ""
	}
	return cfg
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

// version is set at release time.
var version string

// buildVersion is the release version, else the module version go install
// records, else "devel".
func buildVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "devel"
}

// randID returns a new note ID; IDs are made by clients so notes can be created offline.
func randID() string { return rand.Text() }
