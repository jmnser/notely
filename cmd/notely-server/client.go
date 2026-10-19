package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/maxence-charriere/go-app/v10/pkg/app"

	"github.com/jmnser/notely/internal/note"
)

const (
	timeout       = 15 * time.Second // per backend request
	uploadTimeout = 2 * time.Minute  // per image upload
)

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

// exchange pushes local changes to the backend and returns its notes and the
// changes it rejected, which do not stop the others.
func exchange(push []note.Note, deleted []string) ([]note.Note, map[string]error, error) {
	rejected := map[string]error{}
	for _, n := range push {
		var bad badRequest
		if err := call(put, "/api/notes/"+n.ID, n, nil); errors.As(err, &bad) {
			rejected[n.ID] = err
		} else if err != nil {
			return nil, nil, err
		}
	}
	for _, id := range deleted {
		var bad badRequest
		if err := call(del, "/api/notes/"+id, nil, nil); errors.As(err, &bad) {
			rejected[id] = err // dropped, so it does not block every sync
		} else if err != nil {
			return nil, nil, err
		}
	}
	var notes []note.Note
	return notes, rejected, call(get, "/api/notes", nil, &notes)
}

// uploadImage stores an image file (a JavaScript Blob) on the backend and
// returns its path for Markdown, /images/<name>.
func uploadImage(file app.Value) (string, error) {
	var out struct{ Path string }
	err := send(post, "/api/images", file, file.Get("type").String(), uploadTimeout, &out)
	return out.Path, err
}

// rootPrefix is the path the app is served under, without a trailing slash.
func rootPrefix() string {
	return strings.TrimSuffix(app.Getenv("GOAPP_ROOT_PREFIX"), "/")
}

// call sends a JSON request to the backend with the browser's fetch and
// decodes the JSON response into out. It blocks: run it in Async. fetch,
// unlike net/http, keeps Go's TLS stack out of the wasm binary.
func call(method, path string, in, out any) error {
	if in == nil {
		return send(method, path, nil, "", timeout, out)
	}
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return send(method, path, string(body), "application/json", timeout, out)
}

// send fetches path with body, if not nil, and decodes the JSON response into out.
func send(method, path string, body any, contentType string, limit time.Duration, out any) error {
	init := map[string]any{
		"method":      method,
		"credentials": "same-origin",
		"signal":      app.Window().Get("AbortSignal").Call("timeout", limit.Milliseconds()),
	}
	if body != nil {
		init["body"] = body
		init["headers"] = map[string]any{"Content-Type": contentType}
	}
	res, err := await(app.Window().Call("fetch", rootPrefix()+path, init))
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
