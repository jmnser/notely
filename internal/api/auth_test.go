package api

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	var logged bytes.Buffer
	log.SetOutput(&logged)
	defer log.SetOutput(os.Stderr)
	users, _, err := loadUsers(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := users[defaultUser]; !ok || len(users) != 1 {
		t.Errorf("users = %v", users)
	}
	_, password, _ := strings.Cut(logged.String(), "with password ")
	password, _, _ = strings.Cut(password, ",")
	if !users[defaultUser].check(password) {
		t.Errorf("the logged password %q does not sign in: %s", password, logged.String())
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("users file: %v %v", fi, err)
	}
	if err := os.WriteFile(path, []byte(`{"x":"plain"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadUsers(path); err == nil {
		t.Error("accepted an invalid hash")
	}
	if err := os.WriteFile(path, []byte(`{"../x":"`+dummyHash+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadUsers(path); err == nil {
		t.Error("accepted a user name that is no directory name")
	}
}

func TestSignInLimits(t *testing.T) {
	h, _ := parseHash(hashPassword("pw"))
	a := newAuth(map[string]argonHash{"ann": h})
	srv := httptest.NewServer(a.handler(http.NotFoundHandler()))
	defer srv.Close()
	login := func() int {
		t.Helper()
		res, err := http.Post(srv.URL+"/api/login", "application/json", strings.NewReader(`{"user":"ann","password":"pw"}`))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}

	for range freeFailures - 1 {
		a.failed("ann", true)
	}
	if got := login(); got != http.StatusOK || a.backoff("ann") != 0 {
		t.Fatalf("sign-in within the free failures: %d", got)
	}
	for range freeFailures + 3 {
		a.failed("ann", true)
	}
	if got := login(); got != http.StatusTooManyRequests || a.backoff("bob") != 0 {
		t.Fatalf("sign-in after many failures: %d", got)
	}
	a.failed("ann", false)

	defer func(d time.Duration) { checkWait = d }(checkWait)
	checkWait = 10 * time.Millisecond
	a.checking <- struct{}{}
	a.checking <- struct{}{}
	if got := login(); got != http.StatusServiceUnavailable {
		t.Fatalf("sign-in while checks are busy: %d", got)
	}
}
