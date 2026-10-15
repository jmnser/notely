package main

import (
	"slices"
	"strings"

	"github.com/jmnser/gnotes/internal/note"
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
		if i := strings.LastIndexByte(path, '/'); i >= 0 {
			parent, name = get(path[:i]), path[i+1:]
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

// spacePath cleans a space path entered by the user.
func spacePath(name string) string {
	var parts []string
	for p := range strings.SplitSeq(name, "/") {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	if path := strings.Join(parts, "/"); path != defaultSpace {
		return path
	}
	return ""
}

// newPath parses a path typed to create something: "Work/" is the space Work,
// "Work/Ideas" the note Ideas in Work and "Ideas" the note Ideas in the default
// space. ok is false for an empty path.
func newPath(path string) (space, title string, ok bool) {
	path = strings.TrimSpace(path)
	i := strings.LastIndexByte(path, '/')
	return spacePath(path[:max(i, 0)]), strings.TrimSpace(path[i+1:]), path != ""
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
