package main

import (
	"crypto/rand"
	"errors"
	"maps"
	"net/http"
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
	drawerKey    = "gnotes-drawer" // the sidebar is collapsed
	pinnedKey    = "gnotes-pinned" // pinned space paths
	defaultSpace = "Notes"         // name shown for notes without a space
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
	input  bool // asks for a text, passed to run
	list   bool // suggests space paths
	danger bool
	run    func(string)
}

// ui is the whole app: spaces with their notes and a viewer/editor. Changes
// are applied locally first and then synced to the backend.
type ui struct {
	app.Compo

	ctx       app.Context // mount context; outlives the elements that trigger events
	mounted   bool
	notes     []note.Note     // sorted
	dirty     map[string]int  // note ID -> seq of its last change not yet pushed
	deleted   []string        // note IDs whose deletion is not yet pushed
	seq       int             // counts local changes
	current   string          // ID of the open note
	editing   bool            // the open note shows its Markdown source
	collapsed map[string]bool // space paths whose content is hidden
	drawer    bool            // the sidebar is collapsed to a rail on large screens
	pinned    map[string]bool // pinned space paths, kept on this device
	dialog    prompt
	html      string // rendered htmlSrc
	mermaid   int    // Mermaid loading state: 0 not loaded, 1 loading, 2 ready
	htmlSrc   string
	query     string
	status    string
	signedOut bool   // the backend asks for a sign-on
	signingIn bool   // a sign-on request is running
	loginErr  string // why the last sign-on failed
	backup    syncInfo
	backupMsg string // git backup status shown in its dialog
	syncing   bool
	again     bool // a sync was requested while one was running
	retries   bool // a retry of a failed sync is scheduled
	update    bool // a new app version is available
}

func init() {
	app.Route("/", func() app.Composer { return &ui{} })
}

func (u *ui) OnMount(ctx app.Context) {
	u.ctx, u.mounted = ctx, true
	u.dirty = map[string]int{}
	u.collapsed = map[string]bool{}
	var pinned []string
	for k, v := range map[string]any{drawerKey: &u.drawer, pinnedKey: &pinned} {
		if err := ctx.LocalStorage().Get(k, v); err != nil {
			app.Log(err)
		}
	}
	u.pinned = map[string]bool{}
	for _, s := range pinned {
		u.pinned[s] = true
	}

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
	known := map[string]bool{}
	for _, n := range u.notes {
		if strings.Contains(strings.ToLower(n.Space+"\n"+n.Title+"\n"+n.Body), query) {
			shown = append(shown, n)
		}
		for s := n.Space; s != ""; s = parentOf(s) {
			known[s] = true
		}
		known[spaceName(n.Space)] = true // offered as "Work/" for paths
	}
	spaces := tree(shown, u.pinned)
	all := slices.Sorted(maps.Keys(known)) // offered in the dialog

	cur := u.find(u.current)
	class, space, body := "app", "", ""
	if cur != nil {
		class += " open"
		space, body = cur.Space, cur.Body
	}
	toggle, chevron := "Collapse sidebar", "chevron_left"
	if u.drawer {
		class += " drawer-hidden"
		toggle, chevron = "Expand sidebar", "chevron_right"
	}
	view, edit := "", ""
	if u.editing {
		edit = "selected"
	} else {
		view = "selected"
	}
	listID := ""
	if u.dialog.list {
		listID = "spaces"
	}
	danger := "text-button"
	if u.dialog.danger {
		danger += " danger"
	}

	if u.mounted && !u.editing {
		u.ctx.Defer(func(app.Context) { u.diagrams() })
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
				iconButton("add", "New note or space").Class("icon-button tonal").OnClick(func(app.Context, app.Event) {
					u.createNew(space)
				}, app.EventScope(space)),
			),
			app.Ul().Class("tree").Body(app.Range(spaces).Slice(func(i int) app.UI {
				return u.renderSpace(spaces[i], query != "")
			})),
			app.Div().Class("drawer-bottom").Body(
				app.Button().Class("drawer-toggle").Title(toggle).Aria("label", toggle).Aria("expanded", !u.drawer).
					Body(icon(chevron), app.Span().Text("Collapse")).OnClick(u.toggleDrawer),
				iconButton("cloud_upload", "Git backup").OnClick(u.openBackup),
				iconButton("logout", "Sign out").OnClick(u.signOut),
			),
		),
		app.Main().Body(
			app.Div().Class("empty").Body(
				icon("description"),
				app.P().Text("Select a note, or create a space to start writing."),
			),
			app.Header().Class("top-bar").Body(
				app.Div().Class("crumbs").Body(
					iconButton("arrow_back", "Back").Class("icon-button back").OnClick(func(app.Context, app.Event) { u.open("") }),
					app.Span().Class("path").Body(icon("folder"), app.Span().Text(strings.ReplaceAll(spaceName(space), "/", " / "))),
				),
				app.Div().Class("tools").Body(
					app.Div().Class("segmented").Body(
						app.Button().Class(view).Title("View").Aria("pressed", !u.editing).
							Body(icon("visibility"), app.Span().Text("View")).
							OnClick(func(ctx app.Context, _ app.Event) { u.setEditing(ctx, false) }),
						app.Button().Class(edit).Title("Edit").Aria("pressed", u.editing).
							Body(icon("edit"), app.Span().Text("Edit")).
							OnClick(func(ctx app.Context, _ app.Event) { u.setEditing(ctx, true) }),
					),
					iconButton("move", "Move to space").OnClick(u.move),
					pinButton("Pin", cur != nil && cur.Pinned).
						OnClick(func(app.Context, app.Event) { u.togglePin(u.current) }),
					iconButton("delete", "Delete").OnClick(func(app.Context, app.Event) { u.remove(u.current) }),
				),
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
		app.Div().Class("login").Hidden(!u.signedOut).Body(
			app.Form().Class("login-card").OnSubmit(u.signIn).Body(
				icon("description"),
				app.H1().Text("Sign in"),
				app.Input().ID("login-user").Class("field").Placeholder("User name").Aria("label", "User name").
					Attr("autocomplete", "username").Attr("autocapitalize", "none").Required(true),
				app.Input().ID("login-password").Class("field").Type("password").Placeholder("Password").
					Aria("label", "Password").Attr("autocomplete", "current-password").Required(true),
				app.P().Class("login-error").Attr("role", "alert").Text(u.loginErr).Hidden(u.loginErr == ""),
				app.Button().Type("submit").Class("filled-button").Text("Sign in").Disabled(u.signingIn),
			),
		),
		u.renderBackup(),
		app.Dialog().ID("dialog").Body(
			app.Form().OnSubmit(u.submit).Body(
				app.H2().Text(u.dialog.title),
				app.P().Text(u.dialog.text).Hidden(u.dialog.text == ""),
				app.Input().ID("dialog-input").Class("field").Aria("label", u.dialog.title).
					Attr("list", listID).MaxLength(200).Hidden(!u.dialog.input),
				app.DataList().ID("spaces").Body(app.Range(all).Slice(func(i int) app.UI {
					return app.Option().Value(all[i] + "/")
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

// renderSpace renders space s and, if open, its subspaces and notes. While
// searching every space is open.
func (u *ui) renderSpace(s *space, searching bool) app.UI {
	open := searching || !u.collapsed[s.path]
	class := "space"
	if !open {
		class += " collapsed"
	}
	return app.Li().Body(
		app.Div().Class(class).Body(
			app.Button().Class("space-name").Aria("expanded", open).Body(
				app.Span().Class("chevron").Body(icon("expand_more")),
				app.Span().Class("label").Text(s.name),
				app.Span().Class("count").Text(s.count),
			).OnClick(func(app.Context, app.Event) {
				u.collapsed[s.path] = !u.collapsed[s.path]
			}, app.EventScope(s.path)),
			iconButton("add", "New note or subspace").OnClick(func(app.Context, app.Event) {
				u.createNew(s.path)
			}, app.EventScope(s.path)),
			iconButton("edit", "Rename space").OnClick(func(app.Context, app.Event) {
				u.renameSpace(s.path)
			}, app.EventScope(s.path)),
			pinButton("Pin space", u.pinned[s.path]).
				OnClick(func(app.Context, app.Event) {
					u.pinSpace(s.path, !u.pinned[s.path])
				}, app.EventScope(s.path)),
		),
		app.If(open, func() app.UI {
			return app.Ul().Body(
				app.Range(s.spaces).Slice(func(i int) app.UI {
					return u.renderSpace(s.spaces[i], searching)
				}),
				app.Range(s.notes).Slice(func(i int) app.UI {
					return u.renderNote(s.notes[i])
				}),
			)
		}),
	)
}

// renderNote renders note n with its actions.
func (u *ui) renderNote(n note.Note) app.UI {
	row := "note-row"
	if n.ID == u.current {
		row += " current"
	}
	return app.Li().Class(row).Body(
		app.Button().Class("note").Body(
			app.Span().Class("note-title").Text(label(n)),
			app.Span().Class("note-meta").Text(n.Updated.Local().Format("Jan 2, 15:04")),
		).OnClick(func(app.Context, app.Event) { u.open(n.ID) }, app.EventScope(n.ID)),
		iconButton("edit", "Rename").OnClick(func(app.Context, app.Event) { u.rename(n.ID) }, app.EventScope(n.ID)),
		iconButton("delete", "Delete").OnClick(func(app.Context, app.Event) { u.remove(n.ID) }, app.EventScope(n.ID)),
		pinButton("Pin", n.Pinned).
			OnClick(func(app.Context, app.Event) { u.togglePin(n.ID) }, app.EventScope(n.ID)),
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

// parentOf returns the parent of space path s, or "" for a top-level space.
func parentOf(s string) string {
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		return s[:i]
	}
	return ""
}

// expand opens space path s and its parents.
func (u *ui) expand(s string) {
	for ; s != ""; s = parentOf(s) {
		delete(u.collapsed, s)
	}
	delete(u.collapsed, "")
}

// pinSpace pins or unpins space path s on this device.
func (u *ui) pinSpace(s string, on bool) {
	if on {
		u.pinned[s] = true
	} else {
		delete(u.pinned, s)
	}
	if err := u.ctx.LocalStorage().Set(pinnedKey, slices.Sorted(maps.Keys(u.pinned))); err != nil {
		app.Log(err)
	}
}

func (u *ui) toggleDrawer(app.Context, app.Event) {
	u.drawer = !u.drawer
	if err := u.ctx.LocalStorage().Set(drawerKey, u.drawer); err != nil {
		app.Log(err)
	}
}

// ask opens the dialog; value prefills its text field.
func (u *ui) ask(p prompt, value string) {
	u.dialog = p
	in := app.Window().GetElementByID("dialog-input")
	in.Set("value", value)
	u.ctx.Defer(func(app.Context) {
		app.Window().GetElementByID("dialog").Call("showModal")
		switch {
		case !p.input:
		case strings.HasSuffix(value, "/"): // continue typing after the space
			end := utf16Offset(value, len(value))
			in.Call("setSelectionRange", end, end)
		default:
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

// diagrams draws the Mermaid diagrams of the view, loading Mermaid on first use.
// Mermaid needs a visible view to measure text.
func (u *ui) diagrams() {
	const pending = "#view pre.mermaid:not([data-processed])"
	doc := app.Window().Get("document")
	if doc.Call("querySelector", pending).IsNull() {
		return
	}
	switch u.mermaid {
	case 0:
		u.mermaid = 1
		script := doc.Call("createElement", "script")
		script.Set("src", "/web/mermaid/mermaid.min.js")
		script.Call("addEventListener", "load", app.FuncOf(func(app.Value, []app.Value) any {
			u.ctx.Dispatch(func(app.Context) {
				theme := "default"
				if app.Window().Call("matchMedia", "(prefers-color-scheme: dark)").Get("matches").Bool() {
					theme = "dark"
				}
				app.Window().Get("mermaid").Call("initialize", map[string]any{
					"startOnLoad": false, "securityLevel": "strict", "theme": theme,
				})
				u.mermaid = 2
				u.diagrams()
			})
			return nil
		}))
		doc.Get("head").Call("appendChild", script)
	case 2:
		// Mermaid draws errors into the diagram; the rejected promise needs no handling.
		app.Window().Get("mermaid").Call("run", map[string]any{"querySelector": pending}).
			Call("catch", app.Window().Get("Boolean"))
	}
}

// renderBackup renders the git backup dialog. Its fields are filled when opened.
func (u *ui) renderBackup() app.UI {
	configured := u.backup.Remote != ""
	hint := "Paste a token"
	if u.backup.Token {
		hint = "Saved; leave empty to keep it"
	}
	field := func(label string, in app.HTMLInput) app.UI {
		return app.Label().Class("labeled").Body(app.Span().Text(label), in)
	}
	return app.Dialog().ID("backup").Body(
		app.Form().OnSubmit(u.saveBackup).Body(
			app.H2().Text("Git backup"),
			app.P().Text("The notes are pushed to a branch of your repository every hour. "+
				"Use a token that can write to this repository only."),
			field("Repository", app.Input().ID("backup-remote").Class("field").Type("url").
				Placeholder("https://github.com/you/notes.git").Required(true)),
			field("Branch", app.Input().ID("backup-branch").Class("field").Required(true)),
			field("Token", app.Input().ID("backup-token").Class("field").Type("password").
				Placeholder(hint).Attr("autocomplete", "off")),
			app.P().Class("backup-status").Attr("role", "status").Text(u.backupMsg),
			app.Div().Class("actions").Body(
				app.Button().Type("button").Class("text-button danger").Text("Remove").Hidden(!configured).
					OnClick(u.removeBackup),
				app.Button().Type("button").Class("text-button").Text("Sync now").Hidden(!configured).
					OnClick(func(app.Context, app.Event) { u.runBackup() }),
				app.Span().Class("spacer"),
				app.Button().Type("button").Class("text-button").Text("Close").OnClick(func(app.Context, app.Event) {
					app.Window().GetElementByID("backup").Call("close")
				}),
				app.Button().Type("submit").Class("text-button").Text("Save"),
			),
		),
	)
}

// openBackup loads the git backup settings into its dialog.
func (u *ui) openBackup(app.Context, app.Event) {
	u.backupMsg = "Loading…"
	app.Window().GetElementByID("backup").Call("showModal")
	u.loadBackup(func(info syncInfo) {
		branch := info.Branch
		if branch == "" {
			branch = "main"
		}
		for id, v := range map[string]string{"backup-remote": info.Remote, "backup-branch": branch, "backup-token": ""} {
			app.Window().GetElementByID(id).Set("value", v)
		}
	})
}

// loadBackup fetches the git backup settings, shows their status and passes
// them to then.
func (u *ui) loadBackup(then func(syncInfo)) {
	u.ctx.Async(func() {
		var info syncInfo
		err := call(http.MethodGet, "/api/sync", nil, &info)
		u.ctx.Dispatch(func(app.Context) {
			if err != nil {
				u.backupMsg = "Can't load the settings: " + err.Error()
				return
			}
			u.backup, u.backupMsg = info, backupStatus(info)
			if then != nil {
				then(info)
			}
		})
	})
}

// backupStatus describes the last git backup.
func backupStatus(info syncInfo) string {
	switch {
	case info.Remote == "":
		return "Not set up."
	case info.Last == nil:
		return "Not synced yet."
	}
	when := info.Last.Time.Local().Format("Jan 2, 15:04")
	switch {
	case info.Last.Error != "":
		return "Last sync " + when + " failed: " + info.Last.Error
	case info.Last.Commit != "":
		return "Last sync " + when + ": pushed " + info.Last.Commit[:7] + "."
	}
	return "Last sync " + when + ": no changes."
}

func (u *ui) saveBackup(ctx app.Context, e app.Event) {
	e.PreventDefault()
	value := func(id string) string { return app.Window().GetElementByID(id).Get("value").String() }
	in := map[string]string{
		"remote": value("backup-remote"), "branch": value("backup-branch"), "token": value("backup-token"),
	}
	u.backupMsg = "Saving…"
	ctx.Async(func() {
		err := call(http.MethodPut, "/api/sync", in, nil)
		ctx.Dispatch(func(app.Context) {
			var bad badRequest
			switch {
			case errors.As(err, &bad):
				u.backupMsg = "Not saved: " + string(bad) + "."
			case err != nil:
				u.backupMsg = "Not saved: " + err.Error()
			default:
				app.Window().GetElementByID("backup-token").Set("value", "")
				u.pollBackup()
			}
		})
	})
}

// runBackup asks the backend to sync now.
func (u *ui) runBackup() {
	u.ctx.Async(func() {
		err := call(http.MethodPost, "/api/sync/run", nil, nil)
		u.ctx.Dispatch(func(app.Context) {
			if err != nil {
				u.backupMsg = "Can't start a sync: " + err.Error()
				return
			}
			u.pollBackup()
		})
	})
}

// pollBackup shows "Syncing…" until the backend reports a newer sync.
func (u *ui) pollBackup() {
	var since time.Time
	if u.backup.Last != nil {
		since = u.backup.Last.Time
	}
	u.backupMsg = "Syncing…"
	var poll func(tries int)
	poll = func(tries int) {
		u.ctx.After(2*time.Second, func(app.Context) {
			u.loadBackup(func(info syncInfo) {
				if (info.Last == nil || !info.Last.Time.After(since)) && tries > 0 {
					u.backupMsg = "Syncing…"
					poll(tries - 1)
				}
			})
		})
	}
	poll(15)
}

func (u *ui) removeBackup(app.Context, app.Event) {
	u.ask(prompt{
		title: "Remove git backup?", ok: "Remove", danger: true,
		text: "The settings and token are deleted from the server. The repository stays as it is.",
		run: func(string) {
			u.ctx.Async(func() {
				err := call(http.MethodDelete, "/api/sync", nil, nil)
				u.ctx.Dispatch(func(app.Context) {
					if err != nil {
						u.backupMsg = "Not removed: " + err.Error()
						return
					}
					u.backup = syncInfo{}
					app.Window().GetElementByID("backup").Call("close")
				})
			})
		},
	}, "")
}

// signIn sends the sign-in form and syncs once signed on.
func (u *ui) signIn(ctx app.Context, e app.Event) {
	e.PreventDefault()
	if u.signingIn {
		return
	}
	user := strings.TrimSpace(app.Window().GetElementByID("login-user").Get("value").String())
	password := app.Window().GetElementByID("login-password").Get("value").String()
	u.signingIn, u.loginErr = true, ""
	ctx.Async(func() {
		err := signIn(user, password)
		ctx.Dispatch(func(ctx app.Context) {
			u.signingIn = false
			switch {
			case errors.Is(err, errSignedOut):
				u.loginErr = "Wrong user name or password."
			case err != nil:
				app.Log(err)
				u.loginErr = "Can't reach the server. Try again."
			default:
				u.signedOut = false
				app.Window().GetElementByID("login-password").Set("value", "")
				u.sync(ctx)
			}
		})
	})
}

// signOut ends the session and removes the notes from this device.
func (u *ui) signOut(app.Context, app.Event) {
	text := "Notes are removed from this device until you sign in again."
	unsynced := len(u.dirty) > 0 || len(u.deleted) > 0
	if unsynced {
		text = "Changes not synced yet will be lost. " + text
	}
	u.ask(prompt{
		title: "Sign out?", text: text, ok: "Sign out", danger: unsynced,
		run: func(string) {
			u.ctx.Async(func() {
				if err := call("POST", "/api/logout", nil, nil); err != nil {
					app.Log(err)
				}
			})
			u.notes, u.dirty, u.deleted = nil, map[string]int{}, nil
			u.ctx.LocalStorage().Del(storageKey)
			u.open("")
			u.signedOut = true
		},
	}, "")
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

// create adds a note titled title to space and opens it in edit mode.
func (u *ui) create(space, title string) {
	n := note.Note{ID: rand.Text(), Space: space, Title: title, Updated: time.Now()}
	u.notes = append(u.notes, n)
	u.expand(space)
	u.changed(n.ID)
	u.open(n.ID)
	u.editing = true
	focus := "body"
	if title == "" {
		focus = "title"
	}
	u.ctx.Defer(func(app.Context) { app.Window().GetElementByID(focus).Call("focus") })
}

// createNew asks for a path and creates a note, or with a trailing "/" a space
// with a first note; a space exists as long as it has notes. The path starts
// in space.
func (u *ui) createNew(space string) {
	value := ""
	if space != "" {
		value = space + "/"
	}
	u.ask(prompt{
		title: "New", text: "Type a note name, or end with / for a space: Ideas, Work/, Work/Ideas.", ok: "Create",
		input: true, list: true,
		run: func(path string) {
			if space, title, ok := newPath(path); ok {
				u.create(space, title)
			}
		},
	}, value)
}

// renameSpace moves space s with its subspaces and notes to another path.
func (u *ui) renameSpace(s string) {
	u.ask(prompt{
		title: "Rename space", text: "Use / for subspaces, e.g. Work/Projects.", ok: "Rename", input: true, list: true,
		run: func(name string) {
			name = spacePath(name)
			if name == s || s == "" && name == "" {
				return
			}
			var ids []string
			for _, n := range u.notes {
				if within(n.Space, s) {
					ids = append(ids, n.ID)
				}
			}
			for _, id := range ids {
				n := u.find(id)
				n.Space = name + n.Space[len(s):]
				u.changed(id)
			}
			for p := range maps.Clone(u.pinned) {
				if within(p, s) {
					u.pinSpace(p, false)
					u.pinSpace(name+p[len(s):], true)
				}
			}
			u.expand(name)
		},
	}, spaceName(s))
}

// move moves the open note to another space.
func (u *ui) move(app.Context, app.Event) {
	cur := u.find(u.current)
	if cur == nil {
		return
	}
	u.ask(prompt{
		title: "Move to space", text: "Pick a space or type a new path, e.g. Work/Projects.", ok: "Move",
		input: true, list: true,
		run: func(name string) {
			s := spacePath(name)
			u.edit(func(n *note.Note) { n.Space = s })
			u.expand(s)
		},
	}, spaceName(cur.Space))
}

// edit applies f to the open note.
func (u *ui) edit(f func(*note.Note)) {
	u.editNote(u.current, f)
}

// editNote applies f to note id.
func (u *ui) editNote(id string, f func(*note.Note)) {
	if n := u.find(id); n != nil {
		f(n)
		n.Updated = time.Now()
		u.changed(n.ID)
	}
}

func (u *ui) togglePin(id string) {
	u.editNote(id, func(n *note.Note) { n.Pinned = !n.Pinned })
}

// rename asks for a new title of note id.
func (u *ui) rename(id string) {
	n := u.find(id)
	if n == nil {
		return
	}
	u.ask(prompt{
		title: "Rename note", ok: "Rename", input: true,
		run: func(title string) {
			u.editNote(id, func(n *note.Note) { n.Title = title })
			if id == u.current {
				app.Window().GetElementByID("title").Set("value", title)
			}
		},
	}, n.Title)
}

// remove asks to delete note id.
func (u *ui) remove(id string) {
	n := u.find(id)
	if n == nil {
		return
	}
	u.ask(prompt{
		title: "Delete note?", text: "“" + label(*n) + "” will be deleted on all devices.", ok: "Delete", danger: true,
		run: func(string) {
			u.notes = slices.DeleteFunc(u.notes, func(n note.Note) bool { return n.ID == id })
			u.deleted = append(u.deleted, id)
			u.changed(id)
			delete(u.dirty, id)
			if id == u.current {
				u.open("")
			}
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
	if u.signedOut {
		return
	}
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
			if errors.Is(err, errSignedOut) || u.signedOut {
				u.signedOut, u.status = true, "" // local changes stay pending until signed on
				return
			}
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
