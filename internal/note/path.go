package note

import (
	"slices"
	"strings"
)

// DefaultSpace is the name shown for notes without a space.
const DefaultSpace = "Notes"

// Label is the note's title, else its first line, else "Untitled".
func (n Note) Label() string {
	if t := strings.TrimSpace(n.Title); t != "" {
		return t
	}
	if n.Kind == Drawing {
		return "Untitled drawing"
	}
	if l, _, _ := strings.Cut(strings.TrimSpace(n.Body), "\n"); l != "" {
		return l
	}
	return "Untitled"
}

// SpacePath cleans a space path entered by the user.
func SpacePath(name string) string {
	var parts []string
	for p := range strings.SplitSeq(name, "/") {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	if path := strings.Join(parts, "/"); path != DefaultSpace {
		return path
	}
	return ""
}

// ParsePath parses a path typed to create something: "Work/" is the space
// Work, "Work/Ideas" the note Ideas in Work and "Ideas" the note Ideas in the
// default space. ok is false for an empty path.
func ParsePath(path string) (space, title string, ok bool) {
	path = strings.TrimSpace(path)
	i := strings.LastIndexByte(path, '/')
	return SpacePath(path[:max(i, 0)]), strings.TrimSpace(path[i+1:]), path != ""
}

// Merge returns the remote notes without the locally deleted ones and with
// the local version of notes that still have pending changes.
func Merge(remote, local []Note, dirty map[string]int, deleted []string) []Note {
	notes := []Note{}
	for _, n := range local {
		if _, ok := dirty[n.ID]; ok {
			notes = append(notes, n)
		}
	}
	for _, n := range remote {
		if _, ok := dirty[n.ID]; !ok && !slices.Contains(deleted, n.ID) {
			notes = append(notes, n)
		}
	}
	Sort(notes)
	return notes
}
