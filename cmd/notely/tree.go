//go:build !wasm

package main

import (
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jmnser/notely/internal/note"
)

// row is a line of the list: a space, or a note inside one.
type row struct {
	space *note.Space // nil for a note
	note  note.Note
	depth int
	open  bool // the space shows its content
}

// FilterValue is unused: the model filters the notes, not the rows.
func (row) FilterValue() string { return "" }

// key identifies the row across refreshes.
func (r row) key() string {
	if r.space != nil {
		return "space:" + r.space.Path
	}
	return "note:" + r.note.ID
}

// rows lists the space tree as the web app's sidebar: subspaces first, then
// notes. Collapsed spaces hide their content, except while searching.
func rows(spaces []*note.Space, collapsed map[string]bool, searching bool) []list.Item {
	var items []list.Item
	var walk func([]*note.Space, int)
	walk = func(spaces []*note.Space, depth int) {
		for _, s := range spaces {
			open := searching || !collapsed[s.Path]
			items = append(items, row{space: s, depth: depth, open: open})
			if !open {
				continue
			}
			walk(s.Spaces, depth+1)
			for _, n := range s.Notes {
				items = append(items, row{note: n, depth: depth + 1})
			}
		}
	}
	walk(spaces, 0)
	return items
}

// matches reports whether n contains every word of query in its title, space
// or body, ignoring case.
func matches(n note.Note, query string) bool {
	text := strings.ToLower(n.Label() + "\n" + n.Space + "\n" + n.Body)
	return !slices.ContainsFunc(strings.Fields(strings.ToLower(query)), func(w string) bool {
		return !strings.Contains(text, w)
	})
}

// delegate draws a row on one line.
type delegate struct{ dark bool }

func (delegate) Height() int                         { return 1 }
func (delegate) Spacing() int                        { return 0 }
func (delegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }

func (d delegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	r, ok := item.(row)
	if !ok {
		return
	}
	ld := lipgloss.LightDark(d.dark)
	accent := ld(lipgloss.Color("#6750A4"), lipgloss.Color("#D0BCFF")) // M3 primary, as the web app
	faint := lipgloss.NewStyle().Faint(true)
	var text, meta string
	if r.space != nil {
		icon := "▸"
		if r.open {
			icon = "▾"
		}
		text, meta = icon+" "+lipgloss.NewStyle().Bold(true).Render(r.space.Name), strconv.Itoa(r.space.Count)
	} else {
		text, meta = "  "+r.note.Label(), r.note.Updated.Local().Format("2006-01-02")
		if r.note.Pinned {
			text = "  📌 " + r.note.Label()
		}
	}
	line := strings.Repeat("  ", r.depth) + text
	bar := "  "
	if index == m.Index() {
		bar = lipgloss.NewStyle().Foreground(accent).Render("│ ")
		line = lipgloss.NewStyle().Foreground(accent).Render(line)
	}
	width := m.Width() - lipgloss.Width(meta) - 4
	line = lipgloss.NewStyle().MaxWidth(max(width, 1)).Render(line)
	gap := max(width-lipgloss.Width(line), 0) + 1
	fmt.Fprint(w, bar+line+strings.Repeat(" ", gap)+faint.Render(meta)) //nolint:errcheck // the list's buffer
}
