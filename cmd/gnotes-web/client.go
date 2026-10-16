package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/maxence-charriere/go-app/v10/pkg/app"

	"github.com/jmnser/gnotes/internal/note"
)

const timeout = 15 * time.Second // per backend request

// HTTP methods, spelled out to keep net/http's client out of the wasm binary.
const (
	get  = "GET"
	put  = "PUT"
	post = "POST"
	del  = "DELETE"
)

// errSignedOut is returned when the backend needs a sign-on.
var errSignedOut = errors.New("signed out")

// badRequest is the backend's explanation of a rejected request.
type badRequest string

func (e badRequest) Error() string { return string(e) }

// syncInfo is the signed-on user's git backup configuration and last result.
type syncInfo struct {
	Remote string `json:"remote"`
	Branch string `json:"branch"`
	Token  bool   `json:"token"` // a token is stored
	Last   *struct {
		Time   time.Time `json:"time"`
		Commit string    `json:"commit"`
		Error  string    `json:"error"`
	} `json:"last"`
}

// signIn starts a session; the backend sets its cookie.
func signIn(user, password string) error {
	return call(post, "/api/login", map[string]string{"user": user, "password": password}, nil)
}

// exchange pushes local changes to the backend and returns its notes.
func exchange(push []note.Note, deleted []string) ([]note.Note, error) {
	for _, n := range push {
		if err := call(put, "/api/notes/"+n.ID, n, nil); err != nil {
			return nil, err
		}
	}
	for _, id := range deleted {
		if err := call(del, "/api/notes/"+id, nil, nil); err != nil {
			return nil, err
		}
	}
	var notes []note.Note
	return notes, call(get, "/api/notes", nil, &notes)
}

// call sends a JSON request to the backend with the browser's fetch and
// decodes the JSON response into out. It blocks: run it in Async. fetch,
// unlike net/http, keeps Go's TLS stack out of the wasm binary.
func call(method, path string, in, out any) error {
	init := map[string]any{
		"method":      method,
		"credentials": "same-origin",
		"signal":      app.Window().Get("AbortSignal").Call("timeout", timeout.Milliseconds()),
	}
	if in != nil {
		body, err := json.Marshal(in)
		if err != nil {
			return err
		}
		init["body"] = string(body)
		init["headers"] = map[string]any{"Content-Type": "application/json"}
	}
	res, err := await(app.Window().Call("fetch", app.Getenv("GOAPP_ROOT_PREFIX")+path, init))
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	text, err := await(res.Call("text"))
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	switch status := res.Get("status").Int(); {
	case status == 401:
		return errSignedOut
	case status == 400:
		return badRequest(strings.TrimSpace(text.String()))
	case status >= 300:
		return fmt.Errorf("%s %s: %d %s", method, path, status, res.Get("statusText").String())
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal([]byte(text.String()), out)
}

// await waits for a JavaScript promise.
func await(promise app.Value) (app.Value, error) {
	type result struct {
		v   app.Value
		err error
	}
	done := make(chan result, 1)
	resolve := app.FuncOf(func(_ app.Value, args []app.Value) any {
		done <- result{v: args[0]}
		return nil
	})
	reject := app.FuncOf(func(_ app.Value, args []app.Value) any {
		done <- result{err: errors.New(args[0].Call("toString").String())}
		return nil
	})
	defer resolve.Release()
	defer reject.Release()
	promise.Call("then", resolve, reject)
	r := <-done
	return r.v, r.err
}
