package api

import (
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
// spaces. A note whose top space changes to or from a shared space moves
// between the stores; a note keeps its store while its top space stays, so
// notes a user had in a space before it was shared stay private.
type view struct {
	user   string
	own    *store
	shared []*space
	moves  *sync.Mutex // serializes changes, which may touch several stores, across views
}

func (v *view) stores() []*store {
	stores := make([]*store, 0, 1+len(v.shared))
	stores = append(stores, v.own)
	for _, sp := range v.shared {
		stores = append(stores, sp.store)
	}
	return stores
}

// topSpace splits a space path into its top space and the rest.
func topSpace(path string) (top, rest string) {
	top, rest, _ = strings.Cut(path, "/")
	return top, rest
}

// find returns the store holding the newest copy of note id, or nil. A note
// is in several stores when, e.g., an offline device recreated it in a shared
// space after another member moved it out.
func (v *view) find(id string) *store {
	var found *store
	var newest note.Note
	for _, s := range v.stores() {
		if n, ok := s.get(id); ok && (found == nil || n.Updated.After(newest.Updated)) {
			found, newest = s, n
		}
	}
	return found
}

// target returns the store for n, which is in store cur if not nil, and
// n.Space with a shared top space spelt as configured.
func (v *view) target(n note.Note, cur *store) (*store, string) {
	top, rest := topSpace(n.Space)
	if cur == v.own {
		if old, ok := cur.get(n.ID); ok {
			if oldTop, _ := topSpace(old.Space); strings.EqualFold(oldTop, top) {
				return cur, n.Space
			}
		}
	}
	for _, sp := range v.shared {
		if strings.EqualFold(sp.name, top) {
			if rest == "" {
				return sp.store, sp.name
			}
			return sp.store, sp.name + "/" + rest
		}
	}
	return v.own, n.Space
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

// list returns the notes of all stores, the newest copy of each.
func (v *view) list() []note.Note {
	byID := map[string]note.Note{}
	for _, s := range v.stores() {
		for _, n := range s.list() {
			if old, ok := byID[n.ID]; !ok || n.Updated.After(old.Updated) {
				byID[n.ID] = n
			}
		}
	}
	notes := make([]note.Note, 0, len(byID))
	for _, n := range byID {
		notes = append(notes, n)
	}
	note.Sort(notes)
	return notes
}

// put stores n and returns it with the users who see the change. Other
// copies of the note are deleted; images move along from the newest.
func (v *view) put(n note.Note) (note.Note, []string, error) {
	v.moves.Lock()
	defer v.moves.Unlock()
	cur := v.find(n.ID)
	to, space := v.target(n, cur)
	n.Space = space
	if cur != nil && cur != to {
		// References of the former layout point into the user's own store.
		n.Body = oldImageRef.ReplaceAllString(n.Body, "$1$2")
	}
	n, err := to.put(n)
	if err != nil {
		return n, nil, err
	}
	users := v.audience(to)
	if cur != nil && cur != to {
		to.importImages(n.ID, n.Body, cur)
	}
	for _, s := range v.stores() {
		if _, ok := s.get(n.ID); ok && s != to {
			if err := s.delete(n.ID); err != nil {
				return n, users, err
			}
			users = union(users, v.audience(s))
		}
	}
	return n, users, nil
}

// delete deletes all copies of note id and returns the users who see the change.
func (v *view) delete(id string) ([]string, error) {
	v.moves.Lock()
	defer v.moves.Unlock()
	var users []string
	for _, s := range v.stores() {
		if _, ok := s.get(id); ok {
			if err := s.delete(id); err != nil {
				return users, err
			}
			users = union(users, v.audience(s))
		}
	}
	return users, nil
}

// spaces returns the names of the user's shared spaces.
func (v *view) spaces() []string {
	names := make([]string, 0, len(v.shared))
	for _, sp := range v.shared {
		names = append(names, sp.name)
	}
	return names
}

func union(a, b []string) []string {
	out := slices.Concat(a, b)
	slices.Sort(out)
	return slices.Compact(out)
}
