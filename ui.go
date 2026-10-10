package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

// Note is a single note.
type Note struct {
	ID   int    `json:"id"`
	Text string `json:"text"`
}

type notes struct {
	app.Compo
	list []Note
	text string
}

func init() {
	app.Route("/", func() app.Composer { return &notes{} })
}

func (n *notes) OnMount(ctx app.Context) {
	n.call(ctx, http.MethodGet, "/api/notes", nil)
}

func (n *notes) Render() app.UI {
	return app.Main().Body(
		app.H1().Text("gnotes"),
		app.Textarea().ID("text").Placeholder("New note").OnInput(n.ValueTo(&n.text)),
		app.Button().Text("Add").OnClick(func(ctx app.Context, _ app.Event) {
			n.call(ctx, http.MethodPost, "/api/notes", Note{Text: n.text})
			n.text = ""
			// go-app drops empty value attributes, so reset the element directly.
			app.Window().GetElementByID("text").Set("value", "")
		}),
		app.Ul().Body(app.Range(n.list).Slice(func(i int) app.UI {
			note := n.list[i]
			return app.Li().Body(
				app.Text(note.Text+" "),
				app.Button().Text("×").OnClick(func(ctx app.Context, _ app.Event) {
					n.call(ctx, http.MethodDelete, "/api/notes/"+strconv.Itoa(note.ID), nil)
				}, app.EventScope(note.ID)),
			)
		})),
	)
}

// call sends a request to the backend and replaces the list with the response.
func (n *notes) call(ctx app.Context, method, path string, body any) {
	u := *app.Window().URL()
	u.Path = path
	ctx.Async(func() {
		var buf bytes.Buffer
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			app.Log(err)
			return
		}
		req, err := http.NewRequest(method, u.String(), &buf)
		if err != nil {
			app.Log(err)
			return
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			app.Log(err)
			return
		}
		defer res.Body.Close() //nolint:errcheck
		var list []Note
		if err := json.NewDecoder(res.Body).Decode(&list); err != nil {
			app.Log(err)
			return
		}
		ctx.Dispatch(func(app.Context) { n.list = list })
	})
}
