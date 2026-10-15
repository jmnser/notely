package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"
	"time"

	"github.com/jmnser/gnotes/internal/note"
)

// store keeps notes in memory and persists them to a JSON file.
type store struct {
	mu    sync.Mutex
	path  string
	notes map[string]note.Note
}

func openStore(path string) (*store, error) {
	s := &store{path: path, notes: map[string]note.Note{}}
	data, err := os.ReadFile(path) //nolint:gosec // path is the -data flag
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var notes []note.Note
	if err := json.Unmarshal(data, &notes); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	for _, n := range notes {
		s.notes[n.ID] = n
	}
	return s, nil
}

func (s *store) list() []note.Note {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.all()
}

func (s *store) all() []note.Note {
	notes := make([]note.Note, 0, len(s.notes))
	for _, n := range s.notes {
		notes = append(notes, n)
	}
	note.Sort(notes)
	return notes
}

// put creates or replaces a note; the last write wins.
func (s *store) put(n note.Note) (note.Note, error) {
	n.Updated = time.Now().UTC()
	return n, s.set(n.ID, &n)
}

func (s *store) delete(id string) error {
	return s.set(id, nil)
}

// set stores n under id, or removes id if n is nil, rolling back if saving fails.
func (s *store) set(id string, n *note.Note) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	old, ok := s.notes[id]
	if n != nil {
		s.notes[id] = *n
	} else {
		delete(s.notes, id)
	}
	err := s.save()
	if err != nil && ok {
		s.notes[id] = old
	} else if err != nil {
		delete(s.notes, id)
	}
	return err
}

// save writes all notes atomically.
func (s *store) save() error {
	data, err := json.Marshal(s.all())
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
