package main

import (
	"encoding/json"
	"log"
	"net/http"
	"regexp"

	"github.com/jmnser/notely/internal/note"
)

const maxBody = 1 << 20

var validID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func newHandler(s *store) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/notes", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, s.list())
	})
	mux.HandleFunc("PUT /api/notes/{id}", func(w http.ResponseWriter, r *http.Request) {
		var n note.Note
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&n); err != nil {
			http.Error(w, "invalid note", http.StatusBadRequest)
			return
		}
		if n.ID = r.PathValue("id"); !validID.MatchString(n.ID) {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		n, err := s.put(n)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, n)
	})
	mux.HandleFunc("DELETE /api/notes/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := s.delete(r.PathValue("id")); err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return http.NewCrossOriginProtection().Handler(mux)
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
