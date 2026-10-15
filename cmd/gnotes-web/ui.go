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

// prompt describes the dialog.
type prompt struct {
	title  string
	text   string
	ok     string
	input  bool // asks for a space name, passed to run
	danger bool
	run    func(string)
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
	dialog    prompt
	html      string // rendered htmlSrc
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
	known := map[string]bool{}
	for _, n := range u.notes {
		known[spaceName(n.Space)] = true
	}
	all := slices.Sorted(maps.Keys(known)) // offered in the dialog

	cur := u.find(u.current)
	class, pin, space, body := "app", "icon-button", "", ""
	if cur != nil {
		class += " open"
		if cur.Pinned {
			pin += " selected"
		}
		space, body = cur.Space, cur.Body
	}
	view, edit := "", ""
	if u.editing {
		edit = "selected"
	} else {
		view = "selected"
	}
	danger := "text-button"
	if u.dialog.danger {
		danger += " danger"
	}

	// The tree keeps its shape so that go-app never replaces the editor
	// fields, whose values live in the DOM only.
	return app.Div().Class(class).Body(
		app.Nav().Class("drawer").Body(
			app.Div().Class("drawer-top").Body(
				app.Label().Class("search").Body(
					icon("search"),
					app.Input().Type("search").Placeholder("Search notes").Aria("label", "Search notes").
						OnInput(u.ValueTo(&u.query)),
				),
				iconButton("add", "New space").Class("icon-button tonal").OnClick(u.createSpace),
			),
			app.Ul().Class("tree").Body(app.Range(names).Slice(func(i int) app.UI {
				s := names[i]
				return u.renderSpace(s, spaces[s], query != "" || !u.collapsed[s])
			})),
		),
		app.Main().Body(
			app.Div().Class("empty").Body(
				icon("description"),
				app.P().Text("Select a note, or create a space to start writing."),
			),
			app.Header().Class("top-bar").Body(
				iconButton("arrow_back", "Back").Class("icon-button back").OnClick(func(app.Context, app.Event) { u.open("") }),
				app.Span().Class("path").Body(icon("folder"), app.Span().Text(spaceName(space))),
				app.Div().Class("segmented").Body(
					app.Button().Class(view).Title("View").Aria("pressed", !u.editing).
						Body(icon("visibility"), app.Span().Text("View")).
						OnClick(func(ctx app.Context, _ app.Event) { u.setEditing(ctx, false) }),
					app.Button().Class(edit).Title("Edit").Aria("pressed", u.editing).
						Body(icon("edit"), app.Span().Text("Edit")).
						OnClick(func(ctx app.Context, _ app.Event) { u.setEditing(ctx, true) }),
				),
				iconButton("move", "Move to space").OnClick(u.move),
				iconButton("push_pin", "Pin").Class(pin).Aria("pressed", cur != nil && cur.Pinned).
					OnClick(func(app.Context, app.Event) {
						u.edit(func(n *note.Note) { n.Pinned = !n.Pinned })
					}),
				iconButton("delete", "Delete").OnClick(u.remove),
			),
			app.Input().ID("title").Placeholder("Untitled").MaxLength(200).Aria("label", "Title").
				OnInput(func(ctx app.Context, _ app.Event) {
					v := ctx.JSSrc().Get("value").String()
					u.edit(func(n *note.Note) { n.Title = v })
				}),
			app.Div().ID("view").Title("Double-click to edit").Hidden(u.editing).
				OnClick(u.check).OnDblClick(u.startEdit).Body(app.Raw(u.rendered(body))),
			app.Textarea().ID("body").Placeholder("Write Markdown…").MaxLength(100000).Aria("label", "Note").
				Hidden(!u.editing).
				OnInput(func(ctx app.Context, _ app.Event) {
					v := ctx.JSSrc().Get("value").String()
					u.edit(func(n *note.Note) { n.Body = v })
				}).
				OnKeyDown(func(ctx app.Context, e app.Event) {
					switch e.Get("key").String() {
					case "Escape":
						u.setEditing(ctx, false)
					case "Enter":
						u.newline(ctx, e)
					}
				}),
		),
		app.Div().Class("snackbar").Hidden(u.status == "" && !u.update).Body(
			app.Span().Text(u.status),
			app.Button().Class("text-button").Text("Update").Hidden(!u.update).OnClick(func(ctx app.Context, _ app.Event) {
				ctx.Reload()
			}),
		),
		app.Dialog().ID("dialog").Body(
			app.Form().OnSubmit(u.submit).Body(
				app.H2().Text(u.dialog.title),
				app.P().Text(u.dialog.text).Hidden(u.dialog.text == ""),
				app.Input().ID("dialog-input").Class("field").Aria("label", u.dialog.title).
					Attr("list", "spaces").MaxLength(100).Hidden(!u.dialog.input),
				app.DataList().ID("spaces").Body(app.Range(all).Slice(func(i int) app.UI {
					return app.Option().Value(all[i])
				})),
				app.Div().Class("actions").Body(
					app.Button().Type("button").Class("text-button").Text("Cancel").OnClick(func(app.Context, app.Event) {
						app.Window().GetElementByID("dialog").Call("close")
					}),
					app.Button().Type("submit").Class(danger).Text(u.dialog.ok),
				),
			),
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
			app.Button().Class("space-name").Aria("expanded", open).Body(
				app.Span().Class("chevron").Body(icon("expand_more")),
				app.Span().Class("label").Text(spaceName(s)),
				app.Span().Class("count").Text(len(notes)),
			).OnClick(func(app.Context, app.Event) {
				u.collapsed[s] = !u.collapsed[s]
			}, app.EventScope(s)),
			iconButton("add", "New note").OnClick(func(app.Context, app.Event) {
				u.create(s)
			}, app.EventScope(s)),
			iconButton("edit", "Rename space").OnClick(func(app.Context, app.Event) {
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
					app.Span().Class("note-text").Body(
						app.Span().Class("note-title").Text(label(n)),
						app.Span().Class("note-meta").Text(n.Updated.Local().Format("Jan 2, 15:04")),
					),
					app.Span().Class("pin").Body(icon("push_pin")),
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

// spaceOf is the space for name entered by the user.
func spaceOf(name string) string {
	if name == defaultSpace {
		return ""
	}
	return name
}

// ask opens the dialog; value prefills its text field.
func (u *ui) ask(p prompt, value string) {
	u.dialog = p
	in := app.Window().GetElementByID("dialog-input")
	in.Set("value", value)
	u.ctx.Defer(func(app.Context) {
		app.Window().GetElementByID("dialog").Call("showModal")
		if p.input {
			in.Call("select")
		}
	})
}

func (u *ui) submit(_ app.Context, e app.Event) {
	e.PreventDefault()
	v := strings.TrimSpace(app.Window().GetElementByID("dialog-input").Get("value").String())
	app.Window().GetElementByID("dialog").Call("close")
	run := u.dialog.run
	u.dialog.run = nil
	if run != nil {
		run(v)
	}
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

// newline continues or ends a Markdown list when Enter is pressed in the editor.
func (u *ui) newline(ctx app.Context, e app.Event) {
	for _, k := range []string{"shiftKey", "ctrlKey", "altKey", "metaKey", "isComposing"} {
		if e.Get(k).Bool() {
			return
		}
	}
	el := ctx.JSSrc()
	v := el.Get("value").String()
	start, end := el.Get("selectionStart").Int(), el.Get("selectionEnd").Int()
	if start != end {
		return
	}
	from, to, insert, ok := continueList(v, byteOffset(v, start))
	if !ok {
		return
	}
	e.PreventDefault()
	el.Call("setSelectionRange", utf16Offset(v, from), utf16Offset(v, to))
	cmd := "insertText"
	if insert == "" {
		cmd = "delete"
	}
	// execCommand keeps the browser's undo history and fires the input event.
	if app.Window().Get("document").Call("execCommand", cmd, false, insert).Bool() {
		return
	}
	v = v[:from] + insert + v[to:]
	el.Set("value", v)
	c := utf16Offset(v, from+len(insert))
	el.Call("setSelectionRange", c, c)
	u.edit(func(n *note.Note) { n.Body = v })
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

// create adds a note to space and opens it in edit mode.
func (u *ui) create(space string) {
	n := note.Note{ID: rand.Text(), Space: space, Updated: time.Now()}
	u.notes = append(u.notes, n)
	delete(u.collapsed, space)
	u.changed(n.ID)
	u.open(n.ID)
	u.editing = true
	u.ctx.Defer(func(app.Context) { app.Window().GetElementByID("title").Call("focus") })
}

// createSpace creates a space with a first note in it. A space exists as long
// as it has notes.
func (u *ui) createSpace(app.Context, app.Event) {
	u.ask(prompt{
		title: "New space", text: "A space holds notes, like a folder.", ok: "Create", input: true,
		run: func(name string) {
			if name != "" {
				u.create(spaceOf(name))
			}
		},
	}, "")
}

// renameSpace moves all notes of space s to another space.
func (u *ui) renameSpace(s string) {
	u.ask(prompt{title: "Rename space", ok: "Rename", input: true, run: func(name string) {
		name = spaceOf(name)
		if name == s {
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
	}}, spaceName(s))
}

// move moves the open note to another space.
func (u *ui) move(app.Context, app.Event) {
	cur := u.find(u.current)
	if cur == nil {
		return
	}
	u.ask(prompt{
		title: "Move to space", text: "Pick a space or type a new name.", ok: "Move", input: true,
		run: func(name string) {
			s := spaceOf(name)
			u.edit(func(n *note.Note) { n.Space = s })
			delete(u.collapsed, s)
		},
	}, spaceName(cur.Space))
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
	cur := u.find(u.current)
	if cur == nil {
		return
	}
	id := cur.ID
	u.ask(prompt{
		title: "Delete note?", text: "“" + label(*cur) + "” will be deleted on all devices.", ok: "Delete", danger: true,
		run: func(string) {
			u.notes = slices.DeleteFunc(u.notes, func(n note.Note) bool { return n.ID == id })
			u.deleted = append(u.deleted, id)
			u.changed(id)
			delete(u.dirty, id)
			u.open("")
		},
	}, "")
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
				u.status = "Offline. Changes are kept on this device."
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
