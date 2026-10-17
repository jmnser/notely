package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuth(t *testing.T) {
	users := map[string]argonHash{}
	h, err := parseHash(hashPassword("pw"))
	if err != nil {
		t.Fatal(err)
	}
	users["ann"] = h
	notes := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	srv := httptest.NewServer(newAuth(users).handler(notes))
	defer srv.Close()

	do := func(method, path, body, cookie string) (int, []*http.Cookie) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode, res.Cookies()
	}
	for _, c := range []struct {
		name, method, path, body string
		want                     int
	}{
		{"notes signed out", "GET", "/api/notes", "", http.StatusUnauthorized},
		{"session signed out", "GET", "/api/session", "", http.StatusUnauthorized},
		{"healthz open", "GET", "/healthz", "", http.StatusTeapot},
		{"wrong password", "POST", "/api/login", `{"user":"ann","password":"x"}`, http.StatusUnauthorized},
		{"unknown user", "POST", "/api/login", `{"user":"bob","password":"pw"}`, http.StatusUnauthorized},
		{"bad body", "POST", "/api/login", `{`, http.StatusBadRequest},
	} {
		if status, _ := do(c.method, c.path, c.body, ""); status != c.want {
			t.Errorf("%s: status %d, want %d", c.name, status, c.want)
		}
	}

	status, cookies := do("POST", "/api/login", `{"user":"ann","password":"pw"}`, "")
	if status != http.StatusOK || len(cookies) != 1 {
		t.Fatalf("login: status %d, cookies %v", status, cookies)
	}
	c := cookies[0]
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Value == "" {
		t.Errorf("cookie = %+v", c)
	}
	if status, _ := do("GET", "/api/notes", "", c.Value); status != http.StatusTeapot {
		t.Errorf("notes signed on: status %d", status)
	}
	if status, _ := do("GET", "/api/notes", "", "forged"); status != http.StatusUnauthorized {
		t.Errorf("forged cookie: status %d", status)
	}
	do("POST", "/api/logout", "", c.Value)
	if status, _ := do("GET", "/api/notes", "", c.Value); status != http.StatusUnauthorized {
		t.Errorf("after logout: status %d", status)
	}
}

func TestLoadUsers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	users, err := loadUsers(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := users[defaultUser]; !ok || len(users) != 1 {
		t.Errorf("users = %v", users)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("users file: %v %v", fi, err)
	}
	if err := os.WriteFile(path, []byte(`{"x":"plain"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadUsers(path); err == nil {
		t.Error("accepted an invalid hash")
	}
	if err := os.WriteFile(path, []byte(`{"../x":"`+defaultHash+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadUsers(path); err == nil {
		t.Error("accepted a user name that is no directory name")
	}
}
