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
