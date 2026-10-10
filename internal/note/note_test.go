package note

import (
	"testing"
	"time"
)

func TestSort(t *testing.T) {
	now := time.Now()
	notes := []Note{
		{ID: "old", Updated: now.Add(-time.Hour)},
		{ID: "new", Updated: now},
		{ID: "pinned", Pinned: true, Updated: now.Add(-2 * time.Hour)},
	}
	Sort(notes)
	for i, want := range []string{"pinned", "new", "old"} {
		if notes[i].ID != want {
			t.Errorf("notes[%d] = %s, want %s", i, notes[i].ID, want)
		}
	}
}
