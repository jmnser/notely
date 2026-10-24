package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
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
	mux.HandleFunc("GET /api/events", ev.serve)
	handle("POST /api/notes/{id}/images", func(v *view, w http.ResponseWriter, r *http.Request) {
		data, ok := readImage(w, r)
		if !ok {
			return
		}
		name, users, err := v.putImage(r.PathValue("id"), r.URL.Query().Get("name"), data)
		if err != nil {
			imageError(w, r, err)
			return
		}
		ev.notify(users, r.Header.Get(clientHeader))
		writeJSON(w, map[string]string{"name": name})
	})
	// Images by note, for app versions still cached in browsers.
	byNote("GET /api/notes/{id}/images", func(s *store, w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		dir, _ := s.noteDir(r.PathValue("id"))
		s.mu.Unlock()
		names, err := s.imageNames(dir)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, names)
	})
	byNote("GET /api/notes/{id}/images/{name}", func(s *store, w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		n := s.notes[r.PathValue("id")]
		s.mu.Unlock()
		name := r.PathValue("name")
		rel, ok := s.imageFile(n.Space, name)
		if !ok {
			http.NotFound(w, r)
			return
		}
		f, err := os.Open(filepath.Join(s.dir, rel)) //nolint:gosec // a checked regular file under the -data flag
		if err != nil {
			imageError(w, r, err)
			return
		}
		serveImage(w, r, f, name)
	})
	handle("GET /api/images", func(v *view, w http.ResponseWriter, _ *http.Request) {
		list, err := v.images()
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, list)
	})
	// POST /api/images?space=<space>&name=<file name>
	handle("POST /api/images", func(v *view, w http.ResponseWriter, r *http.Request) {
		data, ok := readImage(w, r)
		if !ok {
			return
		}
		q := r.URL.Query()
		file, legacy := q.Get("name"), !q.Has("name")
		if legacy { // app versions before images moved next to notes: named by hash, referenced as /images/<name>
			sum := sha256.Sum256(data)
			file = hex.EncodeToString(sum[:16])
		}
		img, users, err := v.addImage(note.SpacePath(q.Get("space")), file, data)
		if err != nil {
			imageError(w, r, err)
			return
		}
		ev.notify(users, r.Header.Get(clientHeader))
		if legacy {
			writeJSON(w, map[string]string{"path": "/images/" + img.Name})
			return
		}
		writeJSON(w, img)
	})
	handle("GET /api/images/{path...}", func(v *view, w http.ResponseWriter, r *http.Request) {
		space, name := imageOf(r)
		var f *os.File
		var err error
		if s, rel, ok := v.image(space, name); ok {
			f, err = os.Open(filepath.Join(s.dir, rel)) //nolint:gosec // a checked name under the -data flag
		} else {
			f, err = v.own.findImage(name) // a reference of the former layout, /images/<name>
		}
		if err != nil {
			imageError(w, r, err)
			return
		}
		serveImage(w, r, f, name)
	})
	handle("PUT /api/images/{path...}", func(v *view, w http.ResponseWriter, r *http.Request) {
		stem, to, ok := decodeMove(w, r)
		if !ok {
			return
		}
		space, name := imageOf(r)
		img, users, err := v.moveImage(note.Image{Space: space, Name: name}, to, stem)
		if err != nil {
			imageError(w, r, err)
			return
		}
		ev.notify(users, r.Header.Get(clientHeader))
		writeJSON(w, img)
	})
	handle("DELETE /api/images/{path...}", func(v *view, w http.ResponseWriter, r *http.Request) {
		users, err := v.deleteImage(imageOf(r))
		if err != nil {
			serverError(w, err)
			return
		}
		ev.notify(users, r.Header.Get(clientHeader))
		w.WriteHeader(http.StatusNoContent)
	})
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
