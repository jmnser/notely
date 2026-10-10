//go:build !wasm

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

// store keeps notes in memory and persists them to a JSON file.
type store struct {
	mu    sync.Mutex
	path  string
	Next  int    `json:"next"`
	Notes []Note `json:"notes"`
}

func (s *store) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch r.Method {
	case http.MethodPost:
		var n Note
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&n); err != nil || n.Text == "" {
			http.Error(w, "invalid note", http.StatusBadRequest)
			return
		}
		s.Next++
		s.Notes = append(s.Notes, Note{ID: s.Next, Text: n.Text})
	case http.MethodDelete:
		id, _ := strconv.Atoi(r.PathValue("id"))
		s.Notes = slices.DeleteFunc(s.Notes, func(n Note) bool { return n.ID == id })
	}

	if r.Method != http.MethodGet {
		data, err := json.Marshal(s)
		if err == nil {
			err = os.WriteFile(s.path, data, 0o600)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(s.Notes); err != nil {
		log.Print(err)
	}
}

func main() {
	addr := flag.String("addr", "localhost:8000", "listen address")
	path := flag.String("data", "notes.json", "notes file")
	flag.Parse()

	s := &store{path: *path}
	if data, err := os.ReadFile(s.path); err == nil {
		if err := json.Unmarshal(data, s); err != nil {
			log.Fatal(err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/notes", s.handle)
	mux.HandleFunc("POST /api/notes", s.handle)
	mux.HandleFunc("DELETE /api/notes/{id}", s.handle)
	mux.Handle("/", &app.Handler{Name: "gnotes", Description: "A note taking app"})

	srv := &http.Server{Addr: *addr, Handler: http.NewCrossOriginProtection().Handler(mux), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("listening on http://%s", *addr)
	log.Fatal(srv.ListenAndServe())
}
