//go:build !wasm

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/jmnser/notely/internal/atomicfile"
	"github.com/jmnser/notely/internal/note"
)

// state is what the client keeps per server, so notes can be read and edited
// offline: the session, a copy of the notes and the changes not yet synced.
// It holds the session token and private notes, so it is only readable by
// the user.
type state struct {
	path string

	Session string `json:"session,omitempty"`
	User    string `json:"user,omitempty"` // signed on with Session, or last

	Notes   []note.Note    `json:"notes"`
	Dirty   map[string]int `json:"dirty,omitempty"` // note ID → change sequence number
	Deleted []string       `json:"deleted,omitempty"`
}

// statePath is the state file for server in configDir.
func statePath(server string) (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	moveState(dir)
	sum := sha256.Sum256([]byte(server))
	return filepath.Join(dir, hex.EncodeToString(sum[:8])+".json"), nil
}

// moveState moves the state files of earlier versions, kept in the OS's
// config directory under notely-tui (v0.4) or notely (v0.5), into dir. It
// never replaces a file.
func moveState(dir string) {
	config, err := os.UserConfigDir()
	if err != nil {
		return
	}
	for _, old := range []string{filepath.Join(config, "notely-tui"), filepath.Join(config, "notely")} {
		if old == dir {
			continue
		}
		files, _ := filepath.Glob(filepath.Join(old, "*.json"))
		for _, f := range files {
			to := filepath.Join(dir, filepath.Base(f))
			if _, err := os.Lstat(to); errors.Is(err, fs.ErrNotExist) && os.MkdirAll(dir, 0o700) == nil {
				os.Rename(f, to) //nolint:errcheck,gosec // best effort: the old file stays
			}
		}
		os.Remove(old) //nolint:errcheck,gosec // only if empty
	}
}

// loadState reads the state at path; a missing file is an empty state.
func loadState(path string) (*state, error) {
	s := &state{path: path}
	return s, loadJSON(path, s)
}

// save writes the state atomically and durably.
func (s *state) save() error { return saveJSON(s.path, s) }

// lock makes sure only one client uses the state, since each save replaces
// the whole file.
func (s *state) lock() (unlock func(), err error) { return lockPath(s.path, "this server") }

// loadJSON decodes the JSON file at path into v; a missing file leaves v.
func loadJSON(path string, v any) error {
	data, err := os.ReadFile(path) //nolint:gosec // path is derived from the config directory
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// saveJSON writes v as JSON to path atomically and durably, readable only by
// the user.
func saveJSON(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return atomicfile.WriteFile(path, data)
}

// lockPath makes sure only one process uses path, a file or directory of
// what. It holds an OS file lock on <path>.lock, which the OS releases when
// the process ends, even on a crash; the file holds the process ID for the
// error message. unlock releases it.
func lockPath(path, what string) (unlock func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	name := path + ".lock"
	f, err := os.OpenFile(name, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // next to path
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		pid, _ := io.ReadAll(io.LimitReader(f, 20))
		f.Close() //nolint:errcheck,gosec // read only
		if p := strings.TrimSpace(string(pid)); p != "" {
			return nil, fmt.Errorf("notely (process %s) already uses %s", p, what)
		}
		return nil, fmt.Errorf("another notely already uses %s", what)
	}
	if err := f.Truncate(0); err == nil {
		fmt.Fprint(f, os.Getpid()) //nolint:errcheck // only for the message
	}
	return func() { f.Close() }, nil //nolint:errcheck,gosec // closing releases the lock
}

// remove deletes the state file.
func (s *state) remove() error {
	if err := os.Remove(s.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
