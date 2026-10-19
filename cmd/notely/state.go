//go:build !wasm

package main

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jmnser/notely/internal/note"
)

// state is what the client keeps per server, so notes can be read and edited
// offline: the session, a copy of the notes and the changes not yet synced.
// It holds the session token and private notes, so it is only readable by
// the user.
type state struct {
	path string

	Session string         `json:"session,omitempty"`
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
	data, err := os.ReadFile(path) //nolint:gosec // path is derived from the config directory
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	return s, json.Unmarshal(data, s)
}

// save writes the state atomically.
func (s *state) save() error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".state-*") // mode 0600
	if err != nil {
		return err
	}
	defer os.Remove(f.Name()) //nolint:errcheck // gone after the rename
	if _, err := f.Write(data); err != nil {
		f.Close() //nolint:errcheck,gosec // the write error matters
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), s.path)
}

// lock makes sure only one client uses the state, since each save replaces
// the whole file. The lock file holds the client's process ID; a lock whose
// process is gone, say after a crash, is taken over. unlock removes it.
func (s *state) lock() (unlock func(), err error) {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return nil, err
	}
	name := s.path + ".lock"
	for range 3 {
		f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // next to the state
		if err == nil {
			_, err = fmt.Fprint(f, os.Getpid())
			if err = cmp.Or(err, f.Close()); err != nil {
				return nil, err
			}
			return func() { os.Remove(name) }, nil //nolint:errcheck,gosec // best effort
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		if pid, ok := holder(name); ok {
			return nil, fmt.Errorf("notely (process %d) already uses this server", pid)
		}
		if err := os.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("cannot lock %s", name)
}

// holder returns the running process holding the lock file name. A file
// without a process ID, from v0.8 or earlier, counts as held while it is new,
// as another client may be about to write its ID.
func holder(name string) (pid int, ok bool) {
	data, err := os.ReadFile(name) //nolint:gosec // next to the state
	if err != nil {
		return 0, !errors.Is(err, fs.ErrNotExist) // gone: try again
	}
	if pid, err = strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 0 {
		return pid, running(pid)
	}
	fi, err := os.Stat(name)
	return 0, err == nil && time.Since(fi.ModTime()) < 10*time.Second
}

// remove deletes the state file.
func (s *state) remove() error {
	if err := os.Remove(s.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
