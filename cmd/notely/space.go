package main

import (
	"slices"
	"strings"

	"github.com/jmnser/notely/internal/note"
)

// space is a node of the space tree. A space path separates subspaces with
// "/"; the empty path is the default space.
type space struct {
	path   string
	name   string
	parent *space
	spaces []*space
	notes  []note.Note
	count  int // notes in the space and its subspaces
}

// tree groups notes into spaces and subspaces. Each level lists pinned
// spaces first, then sorts by name.
func tree(notes []note.Note, pinned map[string]bool) []*space {
	root := &space{}
	nodes := map[string]*space{}
	var get func(path string) *space
	get = func(path string) *space {
		if s, ok := nodes[path]; ok {
			return s
		}
		parent, name := root, path
		if before, after, ok := strings.CutLast(path, "/"); ok {
			parent, name = get(before), after
		}
		s := &space{path: path, name: spaceName(name), parent: parent}
		nodes[path] = s
		parent.spaces = append(parent.spaces, s)
		return s
	}
	for _, n := range notes {
		s := get(n.Space)
		s.notes = append(s.notes, n)
		for ; s != root; s = s.parent {
			s.count++
		}
	}
	sortSpaces(root.spaces, pinned)
	return root.spaces
}

func sortSpaces(spaces []*space, pinned map[string]bool) {
	slices.SortFunc(spaces, func(a, b *space) int {
		if pa, pb := pinned[a.path], pinned[b.path]; pa != pb && pa {
			return -1
		} else if pa != pb {
			return 1
		}
		return strings.Compare(strings.ToLower(a.name), strings.ToLower(b.name))
	})
	for _, s := range spaces {
		sortSpaces(s.spaces, pinned)
	}
}

// spaceName is the name of space path s as entered by the user.
func spaceName(s string) string {
	if s == "" {
		return defaultSpace
	}
	return s
}

// within reports whether space path s is root or one of its subspaces.
func within(s, root string) bool {
	return s == root || root != "" && strings.HasPrefix(s, root+"/")
}
