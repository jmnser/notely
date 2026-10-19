package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmnser/notely/internal/note"
)

func request(t *testing.T, h http.Handler, method, path, body string, header ...string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func list(t *testing.T, h http.Handler) []note.Note {
	t.Helper()
	w := request(t, h, http.MethodGet, "/api/notes", "")
	var notes []note.Note
	if err := json.Unmarshal(w.Body.Bytes(), &notes); err != nil || notes == nil {
		t.Fatalf("list: %v, body %q", err, w.Body)
	}
	return notes
}

func TestAPI(t *testing.T) {
	dir := t.TempDir()
	s, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	h := newHandler(s)

	if got := list(t, h); len(got) != 0 {
		t.Fatalf("new store has notes: %v", got)
	}
	for _, body := range []string{`{"title":"a"}`, `{"title":"b","body":"text","pinned":true}`} {
		if w := request(t, h, http.MethodPut, "/api/notes/a", body); w.Code != http.StatusOK {
			t.Fatalf("put: %d %s", w.Code, w.Body)
		}
	}
	request(t, h, http.MethodPut, "/api/notes/c", `{"title":"c"}`)

	got := list(t, h)
	if len(got) != 2 || got[0].ID != "a" || got[0].Title != "b" || !got[0].Pinned || got[0].Updated.IsZero() {
		t.Fatalf("list = %+v", got)
	}

	for i := range 2 { // deleting is idempotent
		if w := request(t, h, http.MethodDelete, "/api/notes/c", ""); w.Code != http.StatusNoContent {
			t.Fatalf("delete %d: %d", i, w.Code)
		}
	}

	s, err = openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := list(t, newHandler(s)); len(got) != 1 || got[0].Body != "text" {
		t.Fatalf("after reopen = %+v", got)
	}
}

func TestAPIRejects(t *testing.T) {
	s, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := newHandler(s)

	tests := []struct {
		name, path, body string
		header           []string
		want             int
	}{
		{"bad json", "/api/notes/a", "{", nil, http.StatusBadRequest},
		{"bad id", "/api/notes/a.b", "{}", nil, http.StatusBadRequest},
		{"too large", "/api/notes/a", `{"body":"` + strings.Repeat("x", maxBody) + `"}`, nil, http.StatusBadRequest},
		{"long title", "/api/notes/a", `{"title":"` + strings.Repeat("é", maxTitle+1) + `"}`, nil, http.StatusBadRequest},
		{"long body", "/api/notes/a", `{"body":"` + strings.Repeat("é", maxText+1) + `"}`, nil, http.StatusBadRequest},
		{"long space", "/api/notes/a", `{"space":"` + strings.Repeat("é", maxSpace+1) + `"}`, nil, http.StatusBadRequest},
		{"newline in title", "/api/notes/a", `{"title":"a\nb"}`, nil, http.StatusBadRequest},
		{"tab in space", "/api/notes/a", `{"space":"a\tb"}`, nil, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if w := request(t, h, http.MethodPut, tt.path, tt.body, tt.header...); w.Code != tt.want {
				t.Errorf("status = %d, want %d", w.Code, tt.want)
			}
		})
	}
	if got := list(t, h); len(got) != 0 {
		t.Errorf("rejected requests stored notes: %+v", got)
	}
	if w := request(t, h, http.MethodPut, "/api/notes/a", `{"title":"`+strings.Repeat("é", maxTitle)+`"}`); w.Code != http.StatusOK {
		t.Errorf("title at the limit: %d", w.Code)
	}
	if w := request(t, h, http.MethodPut, "/api/notes/b", `{"space":" Work // Plans /"}`); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `"space":"Work/Plans"`) {
		t.Errorf("space is not cleaned: %d %s", w.Code, w.Body)
	}
	if w := request(t, h, http.MethodDelete, "/api/notes/a.b", ""); w.Code != http.StatusBadRequest {
		t.Errorf("delete bad id: %d", w.Code)
	}
}

func TestStoreRollback(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	s, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "work"), 0o500); err != nil {
		t.Fatal(err)
	}
	if _, err := s.put(note.Note{ID: "a", Space: "work"}); err == nil {
		t.Fatal("put into a read-only space directory succeeded")
	}
	if got := s.list(); len(got) != 0 {
		t.Errorf("failed put kept notes: %+v", got)
	}
}

func TestStoreSpaceFile(t *testing.T) {
	dir := t.TempDir()
	s, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "work"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.put(note.Note{ID: "a", Space: "work", Title: "x"}); err != nil {
		t.Fatal(err)
	}
	if got := s.paths["a"]; got != filepath.Join("work-2", "x.md") {
		t.Errorf("path = %s, want work-2/x.md", got)
	}
}
