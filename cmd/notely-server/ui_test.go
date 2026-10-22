package main

import (
	"slices"
	"testing"
	"time"

	"github.com/jmnser/notely/internal/note"
)

func TestWithPending(t *testing.T) {
	now := time.Now()
	stored := cache{ // what another tab saved
		Notes: []note.Note{
			{ID: "theirs", Title: "B", Updated: now},
			{ID: "both", Title: "B", Updated: now},
			{ID: "synced", Title: "B", Updated: now.Add(-time.Hour)},
			{ID: "clean", Title: "B", Updated: now},
			{ID: "gone", Title: "B", Updated: now},
		},
		Dirty: []string{"theirs", "both", "synced", "gone"},
	}
	mine := cache{
		Notes: []note.Note{
			{ID: "both", Title: "A", Updated: now.Add(time.Minute)},
			{ID: "synced", Title: "server", Updated: now},
		},
		Dirty:   []string{"both"},
		Deleted: []string{"gone"},
	}
	got := withPending(mine, stored)
	titles := map[string]string{}
	for _, n := range got.Notes {
		titles[n.ID] = n.Title
	}
	want := map[string]string{"theirs": "B", "both": "A", "synced": "server"}
	if len(titles) != len(want) {
		t.Fatalf("notes = %v, want %v", titles, want)
	}
	for id, title := range want {
		if titles[id] != title {
			t.Errorf("note %s = %q, want %q", id, titles[id], title)
		}
	}
	slices.Sort(got.Dirty)
	if !slices.Equal(got.Dirty, []string{"both", "theirs"}) {
		t.Errorf("dirty = %v", got.Dirty)
	}
	if len(mine.Notes) != 2 {
		t.Error("withPending changed its argument")
	}
}

func TestWithPendingDeleted(t *testing.T) {
	stored := cache{User: "ann", Deleted: []string{"old", "edited"}}
	mine := cache{
		User:  "ann",
		Notes: []note.Note{{ID: "old"}, {ID: "edited"}, {ID: "kept"}},
		Dirty: []string{"edited"},
	}
	got := withPending(mine, stored)
	if !slices.Equal(got.Deleted, []string{"old"}) || len(got.Notes) != 2 || got.Notes[0].ID != "edited" {
		t.Errorf("got %+v", got)
	}
	if len(mine.Notes) != 3 {
		t.Error("withPending changed its argument")
	}
	if got := withPending(mine, cache{User: "bob", Deleted: []string{"kept"}}); len(got.Notes) != 3 || len(got.Deleted) != 0 {
		t.Errorf("merged another user's cache: %+v", got)
	}
}

func TestWithoutSyncedDrawings(t *testing.T) {
	c := cache{Notes: []note.Note{{ID: "a"}, {ID: "d", Kind: note.Drawing}, {ID: "e", Kind: note.Drawing}}, Dirty: []string{"e"}}
	got := withoutSyncedDrawings(c)
	if len(got.Notes) != 2 || got.Notes[0].ID != "a" || got.Notes[1].ID != "e" || len(c.Notes) != 3 {
		t.Errorf("withoutSyncedDrawings() = %+v, input %+v", got.Notes, c.Notes)
	}
}
