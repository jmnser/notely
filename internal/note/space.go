package note

import (
	"slices"
	"strings"
)

// Space is a node of the space tree. A space path separates subspaces with
// "/"; the empty path is the default space.
type Space struct {
	Path   string
	Name   string
	Parent *Space
	Spaces []*Space
	Notes  []Note
	Count  int // notes in the space and its subspaces
}

// Tree groups notes into spaces and subspaces. Each level lists pinned
// spaces first, then sorts by name.
func Tree(notes []Note, pinned map[string]bool) []*Space {
	root := &Space{}
	nodes := map[string]*Space{}
	var get func(path string) *Space
	get = func(path string) *Space {
		if s, ok := nodes[path]; ok {
			return s
		}
		parent, name := root, path
		if before, after, ok := strings.CutLast(path, "/"); ok {
			parent, name = get(before), after
		}
		s := &Space{Path: path, Name: SpaceName(name), Parent: parent}
		nodes[path] = s
		parent.Spaces = append(parent.Spaces, s)
		return s
	}
	for _, n := range notes {
		s := get(n.Space)
		s.Notes = append(s.Notes, n)
		for ; s != root; s = s.Parent {
			s.Count++
		}
	}
	sortSpaces(root.Spaces, pinned)
	return root.Spaces
}

func sortSpaces(spaces []*Space, pinned map[string]bool) {
	slices.SortFunc(spaces, func(a, b *Space) int {
		if pa, pb := pinned[a.Path], pinned[b.Path]; pa != pb && pa {
			return -1
		} else if pa != pb {
			return 1
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	for _, s := range spaces {
		sortSpaces(s.Spaces, pinned)
	}
}

// SpaceName is the name of space path s as entered by the user.
func SpaceName(s string) string {
	if s == "" {
		return DefaultSpace
	}
	return s
}

// Within reports whether space path s is root or one of its subspaces.
func Within(s, root string) bool {
	return s == root || root != "" && strings.HasPrefix(s, root+"/")
}
