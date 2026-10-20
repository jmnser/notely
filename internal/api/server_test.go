package api

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpen(t *testing.T) {
	dir := t.TempDir()
	users := filepath.Join(t.TempDir(), "users.json")
	if err := os.WriteFile(users, []byte(`{"ann":"`+hashPassword("pw")+`","bob":"`+hashPassword("pw2")+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir, users)
	if err != nil {
		t.Fatal(err)
	}
	annImage := pngName(t)
	srv := httptest.NewServer(s)
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	do := func(method, path, body string, header ...string) int {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		for i := 0; i+1 < len(header); i += 2 {
			req.Header.Set(header[i], header[i+1])
		}
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	for _, tt := range []struct {
		name, method, path, body string
		header                   []string
		want                     int
	}{
		{"healthz", "GET", "/healthz", "", nil, http.StatusNoContent},
		{"notes signed out", "GET", "/api/notes", "", nil, http.StatusUnauthorized},
		{"sync signed out", "GET", "/api/sync", "", nil, http.StatusUnauthorized},
		{"images signed out", "POST", "/api/notes/a/images", "x", nil, http.StatusUnauthorized},
		{"login", "POST", "/api/login", `{"user":"ann","password":"pw"}`, nil, http.StatusOK},
		{"notes", "GET", "/api/notes", "", nil, http.StatusOK},
		{"sync", "GET", "/api/sync", "", nil, http.StatusOK},
		{"put", "PUT", "/api/notes/a", `{"title":"a"}`, nil, http.StatusOK},
		{"image", "POST", "/api/notes/a/images", string(pngData(t)), nil, http.StatusOK},
		{"cross origin", "PUT", "/api/notes/a", "{}", []string{"Origin", "https://evil.example"}, http.StatusForbidden},
		{"logout", "POST", "/api/logout", "", nil, http.StatusNoContent},
		{"login bob", "POST", "/api/login", `{"user":"bob","password":"pw2"}`, nil, http.StatusOK},
		{"other user's note: upload", "POST", "/api/notes/a/images", string(pngData(t)), nil, http.StatusNotFound},
		{"other user's note: images", "GET", "/api/notes/a/images", "", nil, http.StatusNotFound},
		{"other user's image", "GET", "/api/notes/a/images/" + annImage, "", nil, http.StatusNotFound},
		{"other user's image, old form", "GET", "/api/images/" + annImage, "", nil, http.StatusNotFound},
	} {
		if got := do(tt.method, tt.path, tt.body, tt.header...); got != tt.want {
			t.Errorf("%s: %d, want %d", tt.name, got, tt.want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "users", "ann", "a.md")); err != nil {
		t.Errorf("ann's note: %v", err)
	}
	if notes := s.stores["bob"].list(); len(notes) != 0 {
		t.Errorf("bob sees %v", notes)
	}
}
