package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jmnser/notely/internal/note"
)

const (
	maxBody    = 4 << 20
	maxTitle   = 200     // characters, as the app's title field
	maxText    = 100000  // characters, as the app's note field
	maxSpace   = 1000    // characters of a space path
	maxDrawing = 2 << 20 // bytes of a drawing's scene
)

var validID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// newHandler serves the notes API on the view viewOf returns for a request:
// the signed-on user's.
func newHandler(viewOf func(*http.Request) *view, ev *events) http.Handler {
	mux := http.NewServeMux()
	handle := func(pattern string, h func(*view, http.ResponseWriter, *http.Request)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			v := viewOf(r)
			if v == nil {
				http.Error(w, "signed out", http.StatusUnauthorized)
				return
			}
			h(v, w, r)
		})
	}
	// byNote serves requests for note {id} on the store holding it.
	byNote := func(pattern string, h func(*store, http.ResponseWriter, *http.Request)) {
		handle(pattern, func(v *view, w http.ResponseWriter, r *http.Request) {
			s := v.find(r.PathValue("id"))
			if s == nil {
				http.Error(w, "no such note", http.StatusNotFound)
				return
			}
			h(s, w, r)
		})
	}
	// own serves requests on the user's own store.
	own := func(pattern string, h func(*store, http.ResponseWriter, *http.Request)) {
		handle(pattern, func(v *view, w http.ResponseWriter, r *http.Request) { h(v.own, w, r) })
	}
	mux.HandleFunc("GET /api/events", ev.serve)
	handle("POST /api/notes/{id}/images", func(v *view, w http.ResponseWriter, r *http.Request) {
		if data, ok := readImage(w, r); ok {
			name, err := v.putImage(r.PathValue("id"), data)
			if err == nil {
				v.own.listAsset(r, data, name) // also for notes in shared spaces
			}
			writeImage(w, r, name, err)
		}
	})
	byNote("GET /api/notes/{id}/images", (*store).listImages)
	byNote("GET /api/notes/{id}/images/{name}", (*store).serveImage)
	// The former layout's image API, for /images/<name> references and apps
	// still cached in browsers.
	own("POST /api/images", (*store).uploadImage)
	own("GET /api/images/{name}", (*store).serveImage)
	own("GET /api/assets", (*store).listAssets)
	own("POST /api/assets", (*store).uploadAsset)
	own("GET /api/assets/{path...}", (*store).serveAsset)
	own("PUT /api/assets/{path...}", (*store).putAsset)
	own("DELETE /api/assets/{path...}", (*store).removeAsset)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handle("GET /api/me", func(v *view, w http.ResponseWriter, r *http.Request) {
		writeJSON(w, struct {
			User   string   `json:"user"`
			Shared []string `json:"shared"`
		}{userOf(r), v.spaces()})
	})
	handle("GET /api/notes", func(v *view, w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, v.list())
	})
	handle("PUT /api/notes/{id}", func(v *view, w http.ResponseWriter, r *http.Request) {
		var n note.Note
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&n); err != nil {
			http.Error(w, "invalid note", http.StatusBadRequest)
			return
		}
		if n.ID = r.PathValue("id"); !validID.MatchString(n.ID) {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		n.Space = note.SpacePath(n.Space) // as the apps clean it
		if err := validate(n); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if strings.ContainsFunc(n.Title+n.Space, unicode.IsControl) { // titles and spaces are one line
			http.Error(w, "control characters in title or space", http.StatusBadRequest)
			return
		}
		n, users, err := v.put(n)
		if err != nil {
			serverError(w, err)
			return
		}
		ev.notify(users, r.Header.Get(clientHeader))
		writeJSON(w, n)
	})
	handle("DELETE /api/notes/{id}", func(v *view, w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !validID.MatchString(id) {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		users, err := v.delete(id)
		if err != nil {
			serverError(w, err)
			return
		}
		ev.notify(users, r.Header.Get(clientHeader))
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

// validate checks a note's size, and that a drawing's body is a scene.
func validate(n note.Note) error {
	switch {
	case n.Kind != "" && n.Kind != note.Drawing:
		return errors.New("invalid kind")
	case utf8.RuneCountInString(n.Title) > maxTitle || utf8.RuneCountInString(n.Space) > maxSpace:
		return errors.New("note too long")
	case n.Kind == "" && utf8.RuneCountInString(n.Body) > maxText:
		return errors.New("note too long")
	case n.Kind == "":
		return nil
	case len(n.Body) > maxDrawing:
		return errors.New("drawing too large")
	case n.Body != "" && !scene(n.Body):
		return errors.New("invalid drawing")
	}
	return nil
}

// scene reports whether s is a JSON object.
func scene(s string) bool {
	var v map[string]json.RawMessage
	return json.Unmarshal([]byte(s), &v) == nil && v != nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Print(err)
	}
}

func serverError(w http.ResponseWriter, err error) {
	log.Print(err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}
