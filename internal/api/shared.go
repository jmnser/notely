package api

import (
	"log"
	"slices"
	"strings"
	"sync"

	"github.com/jmnser/notely/internal/note"
)

// sharedDir holds a directory per shared space, a top-level space all its
// members see and edit, set in the users file.
const sharedDir = "shared"

type space struct {
	name    string
	store   *store
	members []string
}

// view is what a user sees: their own notes and those of their shared
// spaces. A note whose space changes to or from a shared space moves between
// the stores; a note keeps its store while its space stays, so notes a user
// had in a space before it was shared stay private.
type view struct {
	user   string
	own    *store
	shared []*space
	moves  *sync.Mutex // serializes changes, which may touch two stores, across views
}

func (v *view) stores() []*store {
	stores := make([]*store, 0, 1+len(v.shared))
	stores = append(stores, v.own)
	for _, sp := range v.shared {
		stores = append(stores, sp.store)
	}
	return stores
}

// find returns the store holding note id, the user's own first, or nil.
func (v *view) find(id string) *store {
	for _, s := range v.stores() {
		if _, ok := s.spaceOf(id); ok {
			return s
		}
	}
	return nil
}

// target returns the store for n, which is in store cur if not nil.
func (v *view) target(n note.Note, cur *store) *store {
	if cur != nil {
		if space, _ := cur.spaceOf(n.ID); space == n.Space {
			return cur
		}
	}
	top, _, _ := strings.Cut(n.Space, "/")
	for _, sp := range v.shared {
		if strings.EqualFold(sp.name, top) {
			return sp.store
		}
	}
	return v.own
}

// audience returns the users who see the notes of s.
func (v *view) audience(s *store) []string {
	for _, sp := range v.shared {
		if sp.store == s {
			return sp.members
		}
	}
	return []string{v.user}
}

// list returns the notes of all stores. A note in several, e.g. recreated in
// a shared space by an offline device after it moved, shows its first copy.
func (v *view) list() []note.Note {
	notes, seen := []note.Note{}, map[string]bool{}
	for _, s := range v.stores() {
		for _, n := range s.list() {
			if !seen[n.ID] {
				seen[n.ID] = true
				notes = append(notes, n)
			}
		}
	}
	note.Sort(notes)
	return notes
}

// put stores n and returns it with the users who see the change.
func (v *view) put(n note.Note) (note.Note, []string, error) {
	v.moves.Lock()
	defer v.moves.Unlock()
	cur := v.find(n.ID)
	to := v.target(n, cur)
	n, err := to.put(n)
	if err != nil {
		return n, nil, err
	}
	users := v.audience(to)
	if cur != nil && cur != to {
		to.importImages(n.ID, n.Body, cur)
		if err := cur.delete(n.ID); err != nil {
			log.Print(err)
		}
		users = union(users, v.audience(cur))
	}
	return n, users, nil
}

// delete deletes note id and returns the users who see the change.
func (v *view) delete(id string) ([]string, error) {
	v.moves.Lock()
	defer v.moves.Unlock()
	s := v.find(id)
	if s == nil {
		return nil, nil
	}
	return v.audience(s), s.delete(id)
}

func union(a, b []string) []string {
	out := slices.Concat(a, b)
	slices.Sort(out)
	return slices.Compact(out)
}
