package main

import (
	"crypto/rand"
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

// clientID names this page to the backend, which then does not send it
// events about its own changes.
var clientID = rand.Text()

// errSignedOut is returned when the backend needs a sign-on.
var errSignedOut = errors.New("signed out")

// errNotFound is returned when the backend does not have what was asked for.
var errNotFound = errors.New("not found")

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

// me is the signed-on user with their shared spaces.
type me struct {
	User   string
	Shared []string
}

func whoAmI() (me, error) {
	var out me
	return out, call(get, "/api/me", nil, &out)
}

// uploadImage stores an image file (a JavaScript Blob) next to note n on the
// backend, pushing n first if the backend does not know it yet, and returns
// the image's name, which n references it by.
func uploadImage(n note.Note, file app.Value) (string, error) {
	var out struct{ Name string }
	upload := func() error {
		p := "/api/notes/" + n.ID + "/images?name=" + escape(file.Get("name").String())
		return send(post, p, file, file.Get("type").String(), uploadTimeout, &out)
	}
	err := upload()
	if errors.Is(err, errNotFound) {
		if err = call(put, "/api/notes/"+n.ID, n, nil); err == nil {
			err = upload()
		}
	}
	return out.Name, err
}

// listImages returns the names of the images next to note id; none if the
// backend does not have the note yet.
func listImages(id string) ([]string, error) {
	var names []string
	err := call(get, "/api/notes/"+id+"/images", nil, &names)
	if errors.Is(err, errNotFound) {
		return nil, nil
	}
	return names, err
}

// asset is an image in the Assets space.
type asset struct {
	Name     string    `json:"name"` // DNS label and extension
	Space    string    `json:"space"`
	Uploaded time.Time `json:"uploaded"`
	Version  string    `json:"version"`
}

// key is the asset's path under /api/assets/.
func (a asset) key() string {
	if a.Space == "" {
		return a.Name
	}
	return a.Space + "/" + a.Name
}

// stem is the asset's name without its extension.
func (a asset) stem() string {
	stem, _, _ := strings.Cut(a.Name, ".") // labels hold no dots
	return stem
}

// assetURL loads asset a; naming its version lets the browser cache it.
func assetURL(a asset) string { return rootPrefix() + "/api/assets/" + a.key() + "?v=" + a.Version }

func listAssets() ([]asset, error) {
	var list []asset
	return list, call(get, "/api/assets", nil, &list)
}

// uploadAsset stores an image file (a JavaScript File) in subspace space of
// the Assets space, named after the file.
func uploadAsset(file app.Value, space string) (asset, error) {
	var a asset
	q := "?name=" + escape(file.Get("name").String()) + "&space=" + escape(space)
	return a, send(post, "/api/assets"+q, file, file.Get("type").String(), uploadTimeout, &a)
}

// putAsset renames asset a to name in subspace space.
func putAsset(a asset, name, space string) (asset, error) {
	var out asset
	return out, call(put, "/api/assets/"+a.key(), map[string]string{"name": name, "space": space}, &out)
}

// escape encodes s for a URL query, keeping net/url out of the wasm binary.
func escape(s string) string {
	return app.Window().Call("encodeURIComponent", s).String()
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
	headers := map[string]any{"X-Notely-Client": clientID}
	if body != nil {
		init["body"] = body
		headers["Content-Type"] = contentType
	}
	init["headers"] = headers
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
	case status == 404:
		return fmt.Errorf("%s %s: %w", method, path, errNotFound)
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
