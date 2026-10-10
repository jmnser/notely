// Package note defines the note model shared by the backend and the frontend.
package note

import (
	"slices"
	"strings"
	"time"
)

// Note is a single note.
type Note struct {
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Body    string    `json:"body"`
	Pinned  bool      `json:"pinned"`
	Updated time.Time `json:"updated"`
}

// Sort orders notes pinned first, then most recently updated.
func Sort(notes []Note) {
	slices.SortFunc(notes, func(a, b Note) int {
		switch {
		case a.Pinned != b.Pinned && a.Pinned:
			return -1
		case a.Pinned != b.Pinned:
			return 1
		case !a.Updated.Equal(b.Updated):
			return b.Updated.Compare(a.Updated)
		}
		return strings.Compare(a.ID, b.ID)
	})
}
