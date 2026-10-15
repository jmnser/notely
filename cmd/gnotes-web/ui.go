package main

import (
	"crypto/rand"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/maxence-charriere/go-app/v10/pkg/app"

	"github.com/jmnser/gnotes/internal/note"
)

const (
	debounce   = 500 * time.Millisecond
	retry      = 30 * time.Second
	storageKey = "gnotes"
)

// cache is the state kept in the browser's local storage.
type cache struct {
	Notes   []note.Note
	Dirty   []string
	Deleted []string
}

// ui is the whole app: a note list and an editor. Changes are applied
// locally first and then synced to the backend.
type ui struct {
	app.Compo

	ctx     app.Context    // mount context; outlives the elements that trigger events
	notes   []note.Note    // sorted
	dirty   map[string]int // note ID -> seq of its last change not yet pushed
	deleted []string       // note IDs whose deletion is not yet pushed
	seq     int            // counts local changes
	current string         // ID of the note in the editor
	query   string
	status  string
	syncing bool
	again   bool // a sync was requested while one was running
	retries bool // a retry of a failed sync is scheduled
	update  bool // a new app version is available
}

func init() {
	app.Route("/", func() app.Composer { return &ui{} })
}

func (u *ui) OnMount(ctx app.Context) {
	u.ctx = ctx
	u.dirty = map[string]int{}

	var c cache
	if err := ctx.LocalStorage().Get(storageKey, &c); err != nil {
		app.Log(err)
	}
	u.notes, u.deleted = c.Notes, c.Deleted
	for _, id := range c.Dirty {
		u.dirty[id] = 0
	}

	for _, event := range []string{"online", "focus"} {
		app.Window().Call("addEventListener", event, app.FuncOf(func(app.Value, []app.Value) any {
			ctx.Dispatch(u.sync)
			return nil
		}))
	}
	u.sync(ctx)
}

func (u *ui) OnAppUpdate(ctx app.Context) {
	u.update = ctx.AppUpdateAvailable()
}

func (u *ui) Render() app.UI {
	query := strings.ToLower(u.query)
	var shown []note.Note
	for _, n := range u.notes {
		if strings.Contains(strings.ToLower(n.Title+"\n"+n.Body), query) {
			shown = append(shown, n)
		}
	}
	cur := u.find(u.current)
	class, pin := "app", "Pin"
	if cur != nil {
		class += " editing"
		if cur.Pinned {
			pin = "Unpin"
		}
	}

	// The tree keeps its shape so that go-app never replaces the editor
	// fields, whose values live in the DOM only.
	return app.Div().Class(class).Body(
		app.Div().Class("banner").Hidden(u.status == "" && !u.update).Body(
			app.Span().Text(u.status),
			app.Button().Text("Update app").Hidden(!u.update).OnClick(func(ctx app.Context, _ app.Event) {
				ctx.Reload()
			}),
		),
		app.Aside().Body(
			app.Header().Body(
				app.Input().Type("search").Placeholder("Search").OnInput(u.ValueTo(&u.query)),
				app.Button().Text("New").OnClick(u.create),
			),
			app.Ul().Body(app.Range(shown).Slice(func(i int) app.UI {
				n := shown[i]
				class := "note"
				if n.ID == u.current {
					class += " current"
				}
				if n.Pinned {
					class += " pinned"
				}
				return app.Li().Body(app.Button().Class(class).Body(
					app.B().Text(label(n)),
					app.Small().Text(n.Updated.Local().Format("Jan 2 15:04")),
				).OnClick(func(app.Context, app.Event) { u.open(n.ID) }, app.EventScope(n.ID)))
			})),
		),
		app.Main().Body(
			app.Header().Body(
				app.Button().Class("back").Text("Back").OnClick(func(app.Context, app.Event) { u.open("") }),
				app.Button().Text(pin).OnClick(func(app.Context, app.Event) {
					u.edit(func(n *note.Note) { n.Pinned = !n.Pinned })
				}),
				app.Button().Text("Delete").OnClick(u.remove),
			),
			app.Input().ID("title").Placeholder("Title").MaxLength(200).OnInput(func(ctx app.Context, _ app.Event) {
				v := ctx.JSSrc().Get("value").String()
				u.edit(func(n *note.Note) { n.Title = v })
			}),
			app.Textarea().ID("body").Placeholder("Note").MaxLength(100000).OnInput(func(ctx app.Context, _ app.Event) {
				v := ctx.JSSrc().Get("value").String()
				u.edit(func(n *note.Note) { n.Body = v })
			}),
		),
	)
}

// label is the name of a note in the list.
func label(n note.Note) string {
	if t := strings.TrimSpace(n.Title); t != "" {
		return t
	}
	if l, _, _ := strings.Cut(strings.TrimSpace(n.Body), "\n"); l != "" {
		return l
	}
	return "Untitled"
}

func (u *ui) find(id string) *note.Note {
	for i := range u.notes {
		if u.notes[i].ID == id {
			return &u.notes[i]
		}
	}
	return nil
}

// open shows note id in the editor, or closes the editor if it does not exist.
func (u *ui) open(id string) {
	n := u.find(id)
	if n == nil {
		id, n = "", &note.Note{}
	}
	u.current = id
	app.Window().GetElementByID("title").Set("value", n.Title)
	app.Window().GetElementByID("body").Set("value", n.Body)
}

func (u *ui) create(ctx app.Context, _ app.Event) {
	n := note.Note{ID: rand.Text(), Updated: time.Now()}
	u.notes = append(u.notes, n)
	u.changed(n.ID)
	u.open(n.ID)
	ctx.Defer(func(app.Context) { app.Window().GetElementByID("title").Call("focus") })
}

// edit applies f to the current note.
func (u *ui) edit(f func(*note.Note)) {
	if n := u.find(u.current); n != nil {
		f(n)
		n.Updated = time.Now()
		u.changed(n.ID)
	}
}

func (u *ui) remove(app.Context, app.Event) {
	if !app.Window().Call("confirm", "Delete this note?").Bool() {
		return
	}
	id := u.current
	u.notes = slices.DeleteFunc(u.notes, func(n note.Note) bool { return n.ID == id })
	u.deleted = append(u.deleted, id)
	u.changed(id)
	delete(u.dirty, id)
	u.open("")
}

// changed marks note id as modified and saves once changes pause.
func (u *ui) changed(id string) {
	u.seq++
	seq := u.seq
	u.dirty[id] = seq
	note.Sort(u.notes)
	u.ctx.After(debounce, func(ctx app.Context) {
		if seq == u.seq {
			u.persist()
			u.sync(ctx)
		}
	})
}

// persist stores the notes and pending changes in the browser.
func (u *ui) persist() {
	c := cache{Notes: u.notes, Dirty: slices.Collect(maps.Keys(u.dirty)), Deleted: u.deleted}
	if err := u.ctx.LocalStorage().Set(storageKey, c); err != nil {
		app.Log(err)
		u.status = "Could not save on this device."
	}
}

// sync pushes pending changes and then adopts the backend's notes. Only one
// sync runs at a time.
func (u *ui) sync(app.Context) {
	if u.syncing {
		u.again = true
		return
	}
	u.syncing, u.again = true, false

	var push []note.Note
	for _, n := range u.notes {
		if _, ok := u.dirty[n.ID]; ok {
			push = append(push, n)
		}
	}
	dirty, deleted := maps.Clone(u.dirty), slices.Clone(u.deleted)

	u.ctx.Async(func() {
		remote, err := exchange(push, deleted)
		u.ctx.Dispatch(func(ctx app.Context) {
			u.syncing = false
			if err != nil {
				app.Log(err)
				u.status = "Not synced. Changes are kept on this device."
				if !u.retries {
					u.retries = true
					ctx.After(retry, func(ctx app.Context) {
						u.retries = false
						u.sync(ctx)
					})
				}
				return
			}
			u.status = ""
			for id, seq := range dirty {
				if u.dirty[id] == seq {
					delete(u.dirty, id)
				}
			}
			u.deleted = u.deleted[len(deleted):]
			u.notes = merge(remote, u.notes, u.dirty, u.deleted)
			if _, ok := u.dirty[u.current]; !ok {
				u.open(u.current) // show changes made elsewhere
			}
			u.persist()
			if u.again {
				u.sync(ctx)
			}
		})
	})
}

// merge returns the remote notes without the locally deleted ones and with
// the local version of notes that still have pending changes.
func merge(remote, local []note.Note, dirty map[string]int, deleted []string) []note.Note {
	notes := []note.Note{}
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
	note.Sort(notes)
	return notes
}
