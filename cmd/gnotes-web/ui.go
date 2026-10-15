package main

import (
	"crypto/rand"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/maxence-charriere/go-app/v10/pkg/app"

	"github.com/jmnser/gnotes/internal/note"
)

const (
	debounce     = 500 * time.Millisecond
	retry        = 30 * time.Second
	storageKey   = "gnotes"
	defaultSpace = "Notes" // name shown for notes without a space
)

// cache is the state kept in the browser's local storage.
type cache struct {
	Notes   []note.Note
	Dirty   []string
	Deleted []string
}

// ui is the whole app: spaces with their notes and a viewer/editor. Changes
// are applied locally first and then synced to the backend.
type ui struct {
	app.Compo

	ctx       app.Context     // mount context; outlives the elements that trigger events
	notes     []note.Note     // sorted
	dirty     map[string]int  // note ID -> seq of its last change not yet pushed
	deleted   []string        // note IDs whose deletion is not yet pushed
	seq       int             // counts local changes
	current   string          // ID of the open note
	editing   bool            // the open note shows its Markdown source
	collapsed map[string]bool // spaces whose notes are hidden
	html      string          // rendered htmlSrc
	htmlSrc   string
	query     string
	status    string
	syncing   bool
	again     bool // a sync was requested while one was running
	retries   bool // a retry of a failed sync is scheduled
	update    bool // a new app version is available
}

func init() {
	app.Route("/", func() app.Composer { return &ui{} })
}

func (u *ui) OnMount(ctx app.Context) {
	u.ctx = ctx
	u.dirty = map[string]int{}
	u.collapsed = map[string]bool{}

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
	spaces := map[string][]note.Note{}
	for _, n := range u.notes {
		if strings.Contains(strings.ToLower(n.Space+"\n"+n.Title+"\n"+n.Body), query) {
			spaces[n.Space] = append(spaces[n.Space], n)
		}
	}
	names := slices.Sorted(maps.Keys(spaces))

	cur := u.find(u.current)
	class, pin, mode, space, body := "app", "Pin", "Edit", "", ""
	if cur != nil {
		class += " open"
		if cur.Pinned {
			pin = "Unpin"
		}
		space, body = cur.Space, cur.Body
	}
	if u.editing {
		mode = "View"
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
				app.Button().Text("New space").OnClick(u.createSpace),
			),
			app.Ul().Body(app.Range(names).Slice(func(i int) app.UI {
				s := names[i]
				return u.renderSpace(s, spaces[s], query != "" || !u.collapsed[s])
			})),
		),
		app.Main().Body(
			app.Header().Body(
				app.Button().Class("back").Text("Back").OnClick(func(app.Context, app.Event) { u.open("") }),
				app.Span().Class("path").Text(spaceName(space)+" /"),
				app.Button().Text(mode).OnClick(func(ctx app.Context, _ app.Event) { u.setEditing(ctx, !u.editing) }),
				app.Button().Text("Move").OnClick(u.move),
				app.Button().Text(pin).OnClick(func(app.Context, app.Event) {
					u.edit(func(n *note.Note) { n.Pinned = !n.Pinned })
				}),
				app.Button().Text("Delete").OnClick(u.remove),
			),
			app.Input().ID("title").Placeholder("Title").MaxLength(200).OnInput(func(ctx app.Context, _ app.Event) {
				v := ctx.JSSrc().Get("value").String()
				u.edit(func(n *note.Note) { n.Title = v })
			}),
			app.Div().ID("view").Title("Double-click to edit").Hidden(u.editing).
				OnClick(u.check).OnDblClick(u.startEdit).Body(app.Raw(u.rendered(body))),
			app.Textarea().ID("body").Placeholder("Markdown").MaxLength(100000).Hidden(!u.editing).
				OnInput(func(ctx app.Context, _ app.Event) {
					v := ctx.JSSrc().Get("value").String()
					u.edit(func(n *note.Note) { n.Body = v })
				}).
				OnKeyDown(func(ctx app.Context, e app.Event) {
					if e.Get("key").String() == "Escape" {
						u.setEditing(ctx, false)
					}
				}),
		),
	)
}

// renderSpace renders space s and, if open, its notes.
func (u *ui) renderSpace(s string, notes []note.Note, open bool) app.UI {
	class := "space"
	if !open {
		class += " collapsed"
	}
	return app.Li().Body(
		app.Div().Class(class).Body(
			app.Button().Class("name").Text(spaceName(s)).OnClick(func(app.Context, app.Event) {
				u.collapsed[s] = !u.collapsed[s]
			}, app.EventScope(s)),
			app.Button().Title("New note").Text("+").OnClick(func(ctx app.Context, _ app.Event) {
				u.create(ctx, s)
			}, app.EventScope(s)),
			app.Button().Title("Rename space").Text("✎").OnClick(func(app.Context, app.Event) {
				u.renameSpace(s)
			}, app.EventScope(s)),
		),
		app.If(open, func() app.UI {
			return app.Ul().Body(app.Range(notes).Slice(func(i int) app.UI {
				n := notes[i]
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
			}))
		}),
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

// spaceName is the name shown for space s.
func spaceName(s string) string {
	if s == "" {
		return defaultSpace
	}
	return s
}

// askSpace prompts for a space name; ok is false if the prompt was cancelled.
func askSpace(msg, s string) (space string, ok bool) {
	v := app.Window().Call("prompt", msg, spaceName(s))
	if v.IsNull() || v.IsUndefined() {
		return "", false
	}
	space = strings.TrimSpace(v.String())
	if space == defaultSpace {
		space = ""
	}
	return space, true
}

// rendered returns src as HTML, rendering it only when it changed.
func (u *ui) rendered(src string) string {
	if u.html == "" || src != u.htmlSrc {
		u.html, u.htmlSrc = render(src), src
	}
	return u.html
}

func (u *ui) find(id string) *note.Note {
	for i := range u.notes {
		if u.notes[i].ID == id {
			return &u.notes[i]
		}
	}
	return nil
}

// open shows note id, or closes the open note if id does not exist. Another
// note opens in view mode.
func (u *ui) open(id string) {
	n := u.find(id)
	if n == nil {
		id, n = "", &note.Note{}
	}
	if id != u.current {
		u.editing = false
	}
	u.current = id
	app.Window().GetElementByID("title").Set("value", n.Title)
	app.Window().GetElementByID("body").Set("value", n.Body)
}

// setEditing switches the open note between view and edit mode.
func (u *ui) setEditing(ctx app.Context, on bool) {
	u.editing = on
	if on {
		ctx.Defer(func(app.Context) { app.Window().GetElementByID("body").Call("focus") })
	}
}

func (u *ui) startEdit(ctx app.Context, e app.Event) {
	if e.Get("target").Get("tagName").String() != "INPUT" { // not a double click on a checkbox
		u.setEditing(ctx, true)
	}
}

// check toggles the task whose checkbox was clicked in view mode.
func (u *ui) check(_ app.Context, e app.Event) {
	attr := e.Get("target").Call("getAttribute", "data-task")
	cur := u.find(u.current)
	if attr.IsNull() || cur == nil {
		return
	}
	i, err := strconv.Atoi(attr.String())
	body, ok := toggle(cur.Body, i)
	if err != nil || !ok {
		e.PreventDefault()
		return
	}
	u.edit(func(n *note.Note) { n.Body = body })
	app.Window().GetElementByID("body").Set("value", body)
}

func (u *ui) create(ctx app.Context, space string) {
	n := note.Note{ID: rand.Text(), Space: space, Updated: time.Now()}
	u.notes = append(u.notes, n)
	delete(u.collapsed, space)
	u.changed(n.ID)
	u.open(n.ID)
	u.editing = true
	ctx.Defer(func(app.Context) { app.Window().GetElementByID("title").Call("focus") })
}

// createSpace creates a space with a first note in it. A space exists as long
// as it has notes.
func (u *ui) createSpace(ctx app.Context, _ app.Event) {
	if s, ok := askSpace("Name of the new space", "New space"); ok && s != "" {
		u.create(ctx, s)
	}
}

// renameSpace moves all notes of space s to another space.
func (u *ui) renameSpace(s string) {
	name, ok := askSpace("Rename space", s)
	if !ok || name == s {
		return
	}
	var ids []string
	for _, n := range u.notes {
		if n.Space == s {
			ids = append(ids, n.ID)
		}
	}
	for _, id := range ids {
		u.find(id).Space = name
		u.changed(id)
	}
	delete(u.collapsed, name)
}

// move moves the open note to another space.
func (u *ui) move(app.Context, app.Event) {
	cur := u.find(u.current)
	if cur == nil {
		return
	}
	if s, ok := askSpace("Move to space", cur.Space); ok {
		u.edit(func(n *note.Note) { n.Space = s })
		delete(u.collapsed, s)
	}
}

// edit applies f to the open note.
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
