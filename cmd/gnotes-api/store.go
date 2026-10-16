package main

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jmnser/gnotes/internal/note"
)

// store keeps notes in memory and persists each one as <dir>/<space>/<title>.md.
type store struct {
	mu    sync.Mutex
	dir   string
	notes map[string]note.Note
	paths map[string]string // id → file path relative to dir
}

func openStore(dir string) (*store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &store{dir: dir, notes: map[string]note.Note{}, paths: map[string]string{}}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case path != dir && strings.HasPrefix(d.Name(), "."): // .git, temp files
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		case d.IsDir() || filepath.Ext(path) != ".md":
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
		n, err := decode(data)
		if err != nil {
			log.Printf("skip %s: %v", rel, err)
			return nil
		}
		if old, ok := s.notes[n.ID]; ok {
			log.Printf("id %s in %s and %s: keeping the newer", n.ID, s.paths[n.ID], rel)
			if !n.Updated.After(old.Updated) {
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

// put creates or replaces a note; the last write wins.
func (s *store) put(n note.Note) (note.Note, error) {
	n.Updated = time.Now().UTC()

	s.mu.Lock()
	defer s.mu.Unlock()
	old, moved := s.paths[n.ID]
	rel := s.pathFor(n, old)
	if err := s.write(rel, encode(n)); err != nil {
		return note.Note{}, err
	}
	if moved && old != rel && !s.sameFile(old, rel) {
		if err := s.remove(old); err != nil {
			log.Print(err)
		}
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
	delete(s.notes, id)
	delete(s.paths, id)
	return nil
}

// pathFor returns where n is stored: old if it still fits n's space and title,
// else the first free <space>/<title>[-N].md. Names compare case-insensitively,
// as on macOS and Windows file systems.
func (s *store) pathFor(n note.Note, old string) string {
	base := fileName(n.Title, "untitled")
	if n.Space != "" {
		base = filepath.Join(s.spaceDir(fileName(n.Space, "unnamed")), base)
	}
	if numbered(old, base) {
		return old
	}
	used := map[string]bool{}
	for id, p := range s.paths {
		if id != n.ID {
			used[strings.ToLower(p)] = true
		}
	}
	for i := 1; ; i++ {
		p := base + ".md"
		if i > 1 {
			p = fmt.Sprintf("%s-%d.md", base, i)
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

// numbered reports whether path is base.md or base-N.md.
func numbered(path, base string) bool {
	rest, ok := strings.CutPrefix(path, base)
	if !ok {
		return false
	}
	if rest, ok = strings.CutSuffix(rest, ".md"); !ok {
		return false
	}
	if rest == "" {
		return true
	}
	num, ok := strings.CutPrefix(rest, "-")
	i, err := strconv.Atoi(num)
	return ok && err == nil && i > 1 && strconv.Itoa(i) == num
}

// write replaces rel atomically.
func (s *store) write(rel string, data []byte) error {
	path := filepath.Join(s.dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// remove deletes rel, and its space directory once empty.
func (s *store) remove(rel string) error {
	path := filepath.Join(s.dir, rel)
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if dir := filepath.Dir(path); dir != filepath.Clean(s.dir) {
		_ = os.Remove(dir) // fails while not empty
	}
	return nil
}

// sameFile reports whether a and b name the same file, e.g. differing only in
// case on a case-insensitive file system.
func (s *store) sameFile(a, b string) bool {
	fa, errA := os.Stat(filepath.Join(s.dir, a))
	fb, errB := os.Stat(filepath.Join(s.dir, b))
	return errA == nil && errB == nil && os.SameFile(fa, fb)
}

const maxName = 100 // bytes

// fileName turns s into a readable file name that is safe on all platforms,
// or returns fallback if nothing is left.
func fileName(s, fallback string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`/\:*?"<>|`, r) {
			return '-'
		}
		return r
	}, s)
	if len(s) > maxName {
		s = strings.ToValidUTF8(s[:maxName], "")
	}
	if s = strings.Trim(s, " ."); s == "" {
		return fallback
	}
	return s
}

// encode renders n as Markdown with its fields in YAML front matter.
func encode(n note.Note) []byte {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "id: %s\n", n.ID)
	fmt.Fprintf(&b, "title: %s\n", strconv.Quote(n.Title))
	if n.Space != "" {
		fmt.Fprintf(&b, "space: %s\n", strconv.Quote(n.Space))
	}
	if n.Pinned {
		b.WriteString("pinned: true\n")
	}
	fmt.Fprintf(&b, "updated: %s\n", n.Updated.Format(time.RFC3339Nano))
	b.WriteString("---\n")
	b.WriteString(n.Body)
	return []byte(b.String())
}

// decode parses a file written by encode. Unknown keys are ignored.
func decode(data []byte) (note.Note, error) {
	var n note.Note
	first, rest, _ := strings.Cut(string(data), "\n")
	if strings.TrimSuffix(first, "\r") != "---" {
		return n, errors.New("no front matter")
	}
	for {
		line, after, found := strings.Cut(rest, "\n")
		if line = strings.TrimSuffix(line, "\r"); line == "---" {
			rest = after
			break
		}
		if !found {
			return n, errors.New("unterminated front matter")
		}
		rest = after
		key, value, _ := strings.Cut(line, ":")
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "id":
			n.ID = value
		case "title":
			n.Title = unquote(value)
		case "space":
			n.Space = unquote(value)
		case "pinned":
			n.Pinned = value == "true"
		case "updated":
			t, err := time.Parse(time.RFC3339Nano, value)
			if err != nil {
				return n, fmt.Errorf("updated: %w", err)
			}
			n.Updated = t
		}
	}
	if !validID.MatchString(n.ID) {
		return n, fmt.Errorf("invalid id %q", n.ID)
	}
	n.Body = rest
	return n, nil
}

// unquote reads a double-quoted or plain YAML scalar.
func unquote(s string) string {
	if u, err := strconv.Unquote(s); err == nil {
		return u
	}
	return s
}
