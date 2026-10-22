package main

import (
	"strconv"

	"github.com/maxence-charriere/go-app/v10/pkg/app"

	"github.com/jmnser/notely/internal/note"
)

// editor is the Excalidraw editor mounted in #canvas.
type editor struct {
	id     string    // the drawing shown
	body   string    // its scene as last passed to or from the editor
	handle app.Value // window.notelyExcalidraw.mount's result
	change app.Func  // receives the editor's changes
}

// dark reports whether the dark color scheme is in use.
func dark() bool {
	return app.Window().Call("matchMedia", "(prefers-color-scheme: dark)").Get("matches").Bool()
}

// loadExcalidraw loads the Excalidraw bundle (web/excalidraw) on first use and
// reports whether it is ready. Once loaded, the drawings are shown.
func (u *ui) loadExcalidraw() bool {
	if u.excalidraw != 0 {
		return u.excalidraw == 2
	}
	u.excalidraw = 1
	base := app.Getenv("GOAPP_STATIC_RESOURCES_URL") + "/excalidraw/"
	app.Window().Set("EXCALIDRAW_ASSET_PATH", base) // fonts
	doc := app.Window().Get("document")
	style := doc.Call("createElement", "link")
	style.Set("rel", "stylesheet")
	style.Set("href", base+"index.css")
	script := doc.Call("createElement", "script")
	script.Set("type", "module")
	script.Set("src", base+"index.js")
	script.Call("addEventListener", "load", app.FuncOf(func(app.Value, []app.Value) any {
		u.ctx.Dispatch(func(app.Context) {
			u.excalidraw = 2
			app.Window().Call("matchMedia", "(prefers-color-scheme: dark)").Call("addEventListener", "change",
				app.FuncOf(func(app.Value, []app.Value) any {
					u.ctx.Dispatch(func(app.Context) {
						if u.editor != nil {
							u.editor.handle.Call("theme", dark())
						}
						u.drawings()
					})
					return nil
				}))
			u.canvas()
			u.drawings()
		})
		return nil
	}))
	script.Call("addEventListener", "error", app.FuncOf(func(app.Value, []app.Value) any {
		u.ctx.Dispatch(func(app.Context) {
			script.Call("remove")
			style.Call("remove")
		})
		// Retried later, e.g. once back online; at once would loop while offline.
		u.ctx.After(retry, func(app.Context) { u.excalidraw = 0 })
		return nil
	}))
	doc.Get("head").Call("appendChild", style)
	doc.Get("head").Call("appendChild", script)
	return false
}

// canvas shows the open drawing in the editor, or removes the editor when no
// drawing is open.
func (u *ui) canvas() {
	cur := u.find(u.current)
	if cur == nil || cur.Kind != note.Drawing {
		u.closeEditor()
		return
	}
	if !u.loadExcalidraw() {
		return
	}
	if u.editor != nil && u.editor.id == cur.ID {
		if cur.Body != u.editor.body { // changed on another device
			u.editor.body = cur.Body
			u.editor.handle.Call("update", cur.Body)
		}
		return
	}
	u.closeEditor()
	e := &editor{id: cur.ID, body: cur.Body}
	e.change = app.FuncOf(func(_ app.Value, args []app.Value) any {
		body := args[0].String()
		u.ctx.Dispatch(func(app.Context) {
			if u.saveDrawing(e.id, body) && u.editor == e {
				e.body = body
			}
		})
		return nil
	})
	el := app.Window().GetElementByID("canvas")
	e.handle = app.Window().Get("notelyExcalidraw").Call("mount", el, cur.Body, dark(), e.change)
	u.editor = e
}

// flushEditor applies the editor's pending changes at once, e.g. before the
// page is hidden, when a dispatched change would come too late.
func (u *ui) flushEditor() {
	if u.editor == nil {
		return
	}
	if body := u.editor.handle.Call("flush"); body.Truthy() && u.saveDrawing(u.editor.id, body.String()) {
		u.editor.body = body.String()
	}
}

// maxDrawing is the server's limit on a drawing's scene, in bytes.
const maxDrawing = 2 << 20

// saveDrawing stores body as drawing id's scene unless the server would
// reject it as too large; it reports whether it did.
func (u *ui) saveDrawing(id, body string) bool {
	if len(body) > maxDrawing {
		u.status = "Drawing too large to save (over 2 MiB). Remove images from it."
		return false
	}
	u.editNote(id, func(n *note.Note) { n.Body = body })
	return true
}

// closeEditor removes the editor; it saves pending changes first.
func (u *ui) closeEditor() {
	if u.editor == nil {
		return
	}
	u.editor.handle.Call("unmount")
	u.editor.change.Release()
	u.editor = nil
}

// drawings fills the drawing embeds of the view. Each embed remembers what it
// shows in data-key, so it is only drawn again when that changes.
func (u *ui) drawings() {
	cur := u.find(u.current)
	embeds := app.Window().Get("document").Call("querySelectorAll", "#view .drawing")
	if cur == nil || embeds.Length() == 0 {
		return
	}
	isDark := dark()
	for i := range embeds.Length() {
		el := embeds.Index(i)
		space, title := drawingPath(el.Get("dataset").Get("drawing").String(), cur.Space)
		d := findDrawing(u.notes, space, title)
		key := "missing"
		if d != nil {
			key = d.ID + "@" + strconv.FormatInt(d.Updated.UnixNano(), 36) + "@" + strconv.FormatBool(isDark)
		}
		if el.Get("dataset").Get("key").String() == key {
			continue
		}
		if d == nil {
			el.Get("dataset").Set("key", key)
			el.Set("title", "Create drawing")
			el.Call("replaceChildren", "Create drawing “"+title+"”")
			continue
		}
		if !u.loadExcalidraw() {
			return
		}
		el.Get("dataset").Set("key", key)
		el.Set("title", "Open drawing")
		app.Window().Get("notelyExcalidraw").Call("embed", el, d.Body, isDark)
	}
}

// openEmbed opens the drawing of embed el, creating it if missing. The back
// gesture returns to the note.
func (u *ui) openEmbed(el app.Value) {
	cur := u.find(u.current)
	if cur == nil {
		return
	}
	from := cur.ID
	space, title := drawingPath(el.Get("dataset").Get("drawing").String(), cur.Space)
	if d := findDrawing(u.notes, space, title); d != nil {
		u.show(d.ID, from)
		return
	}
	u.create(space, title, note.Drawing)
	u.from = from
	u.syncBack()
}
