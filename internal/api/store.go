package api

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jmnser/notely/internal/atomicfile"
	"github.com/jmnser/notely/internal/note"
	"github.com/jmnser/notely/internal/notefile"
)

// store keeps a user's notes in memory and persists each one as
// <dir>/<space>/<title>.md, drawings as <title>.excalidraw, with the images
// they use next to them.
type store struct {
	mu    sync.Mutex
	dir   string
	space string // a shared space's name: its notes' top space, left out of their paths
	notes map[string]note.Note
	paths map[string]string // id → file path relative to dir
}

func openStore(dir string) (*store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &store{dir: dir, notes: map[string]note.Note{}, paths: map[string]string{}}
	var stale []string // older files of a note, left by a crash while moving it
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case path != dir && strings.HasPrefix(d.Name(), "."): // .git, temp files
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		case d.IsDir() || !notefile.IsNote(path):
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path) //nolint:gosec // path is under the -data flag
		if err != nil {
			return err
		}
		n, err := notefile.Decode(data, filepath.Ext(path))
		if err != nil {
			log.Printf("skip %s: %v", rel, err)
			return nil
		}
		if old, ok := s.notes[n.ID]; ok {
			older := rel
			if n.Updated.After(old.Updated) {
				older = s.paths[n.ID]
			}
			log.Printf("id %s in %s and %s: removing the older %s", n.ID, s.paths[n.ID], rel, older)
			stale = append(stale, older)
			if older == rel {
				return nil
			}
		}
		s.notes[n.ID] = n
		s.paths[n.ID] = rel
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, rel := range stale { // else deleting the note would bring it back on the next start
		if err := s.remove(rel); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *store) list() []note.Note {
	s.mu.Lock()
	defer s.mu.Unlock()
	notes := make([]note.Note, 0, len(s.notes))
	for _, n := range s.notes {
		notes = append(notes, n)
	}
	note.Sort(notes)
	return notes
}

// get returns note id.
func (s *store) get(id string) (note.Note, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.notes[id]
	return n, ok
}

// put creates or replaces a note; the last write wins.
func (s *store) put(n note.Note) (note.Note, error) {
	n.Updated = time.Now().UTC()

	s.mu.Lock()
	defer s.mu.Unlock()
	old, exists := s.paths[n.ID]
	was := s.notes[n.ID].Space
	rel := s.pathFor(n, old)
	moved := exists && old != rel && !s.sameFile(old, rel)
	if moved && n.Kind != note.Drawing {
		n.Body = s.carryImages(n.ID, n.Body, n.Space, filepath.Join(s.dir, filepath.Dir(old)), filepath.Dir(rel), was, s)
	}
	data, err := notefile.Encode(n)
	if err != nil {
		return note.Note{}, err
	}
	if err := s.write(rel, data); err != nil {
		return note.Note{}, err
	}
	if moved {
		if err := s.remove(old); err != nil {
			log.Print(err)
		}
		s.markDir(filepath.Dir(old), was) // for the images left, if it has no more notes
	}
	s.notes[n.ID] = n
	s.paths[n.ID] = rel
	return n, nil
}

func (s *store) delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rel, ok := s.paths[id]
	if !ok {
		return nil
	}
	if err := s.remove(rel); err != nil {
		return err
	}
	s.markDir(filepath.Dir(rel), s.notes[id].Space)
	delete(s.notes, id)
	delete(s.paths, id)
	return nil
}

// pathFor returns where n is stored: old if it still fits n's space and title,
// else the first free <space>/<title>[-N].md (.excalidraw for drawings). Names
// compare case-insensitively, as on macOS and Windows file systems.
func (s *store) pathFor(n note.Note, old string) string {
	base, ext := notefile.Name(n.Title, "untitled"), notefile.Ext(n.Kind)
	space := n.Space
	if s.space != "" {
		_, space, _ = strings.Cut(space, "/")
	}
	if space != "" {
		base = filepath.Join(s.spaceDir(notefile.Name(space, "unnamed")), base)
	}
	if notefile.Numbered(old, base, ext) {
		return old
	}
	used := map[string]bool{}
	for id, p := range s.paths {
		if id != n.ID {
			used[strings.ToLower(p)] = true
		}
	}
	for i := 1; ; i++ {
		p := base + ext
		if i > 1 {
			p = fmt.Sprintf("%s-%d%s", base, i, ext)
		}
		if used[strings.ToLower(p)] {
			continue
		}
		if _, err := os.Lstat(filepath.Join(s.dir, p)); err == nil && !strings.EqualFold(p, old) {
			continue // a file the store does not own
		}
		return p
	}
}

// spaceDir returns the first of dir, dir-2, ... that is not a file.
func (s *store) spaceDir(dir string) string {
	for i := 1; ; i++ {
		d := dir
		if i > 1 {
			d = fmt.Sprintf("%s-%d", dir, i)
		}
		if fi, err := os.Lstat(filepath.Join(s.dir, d)); err != nil || fi.IsDir() {
			return d
		}
	}
}

// write replaces rel atomically and durably.
func (s *store) write(rel string, data []byte) error {
	path := filepath.Join(s.dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return atomicfile.WriteFile(path, data)
}

// remove deletes rel, and its space directory once empty.
func (s *store) remove(rel string) error {
	path := filepath.Join(s.dir, rel)
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	s.removeDir(filepath.Dir(rel))
	return nil
}

// sameFile reports whether a and b name the same file, e.g. differing only in
// case on a case-insensitive file system.
func (s *store) sameFile(a, b string) bool {
	fa, errA := os.Stat(filepath.Join(s.dir, a))
	fb, errB := os.Stat(filepath.Join(s.dir, b))
	return errA == nil && errB == nil && os.SameFile(fa, fb)
}
