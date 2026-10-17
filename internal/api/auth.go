package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"sync"
	"time"
)

const (
	defaultUser = "joe"
	defaultHash = "$argon2id$v=19$m=65536,t=3,p=1$YmUyNzQ5MjdjYjExMGNiM2JkYzA5YTQ3YjRiZDM5NGY$" +
		"WKqtCW5E9T1E1Klb1kKlaC3Qi660yEtZnc67iibmWZw"
	cookieName   = "notely_session"
	sessionTTL   = 30 * 24 * time.Hour
	failureDelay = time.Second
)

// loadUsers reads the users file, a JSON object mapping user names to argon2id
// hashes. A missing file is created with the default user.
func loadUsers(path string) (map[string]argonHash, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is the -users flag
	if errors.Is(err, fs.ErrNotExist) {
		data, _ = json.MarshalIndent(map[string]string{defaultUser: defaultHash}, "", "  ")
		err = os.WriteFile(path, append(data, '\n'), 0o600)
	}
	if err != nil {
		return nil, err
	}
	var hashes map[string]string
	if err := json.Unmarshal(data, &hashes); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	users := map[string]argonHash{}
	for name, s := range hashes {
		if !validID.MatchString(name) { // names become directory names
			return nil, fmt.Errorf("read %s: user name %q: use letters, digits, - and _", path, name)
		}
		h, err := parseHash(s)
		if err != nil {
			return nil, fmt.Errorf("read %s: user %s: %w", path, name, err)
		}
		users[name] = h
	}
	if len(users) == 0 {
		return nil, fmt.Errorf("read %s: no users", path)
	}
	return users, nil
}

type userKey struct{}

// userOf returns the signed-on user of a request passed on by auth.
func userOf(r *http.Request) string {
	user, _ := r.Context().Value(userKey{}).(string)
	return user
}

type session struct {
	user    string
	expires time.Time
}

// auth signs users on with a session cookie and guards the API.
type auth struct {
	users    map[string]argonHash
	dummy    argonHash     // checked for unknown users, so they take as long as known ones
	checking chan struct{} // limits concurrent password checks, which take 64 MiB each

	mu       sync.Mutex
	sessions map[string]session // token → session
}

func newAuth(users map[string]argonHash) *auth {
	dummy, _ := parseHash(defaultHash)
	return &auth{users: users, dummy: dummy, checking: make(chan struct{}, 2), sessions: map[string]session{}}
}

// handler serves sign-on under /api/ and passes signed-on requests to next.
// Other paths, such as /healthz, go to next directly.
func (a *auth) handler(next http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", a.login)
	mux.HandleFunc("POST /api/logout", a.logout)
	mux.HandleFunc("GET /api/session", func(w http.ResponseWriter, r *http.Request) {
		if user, ok := a.user(r); ok {
			writeJSON(w, map[string]string{"user": user})
			return
		}
		http.Error(w, "signed out", http.StatusUnauthorized)
	})
	mux.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := a.user(r)
		if !ok {
			http.Error(w, "signed out", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, user)))
	}))
	mux.Handle("/", next)
	return http.NewCrossOriginProtection().Handler(mux)
}

func (a *auth) login(w http.ResponseWriter, r *http.Request) {
	var in struct{ User, Password string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if !a.check(r, in.User, in.Password) {
		time.Sleep(failureDelay)
		http.Error(w, "wrong user name or password", http.StatusUnauthorized)
		return
	}
	token := rand.Text()
	a.mu.Lock()
	now := time.Now()
	for t, s := range a.sessions {
		if now.After(s.expires) {
			delete(a.sessions, t)
		}
	}
	a.sessions[token] = session{user: in.User, expires: now.Add(sessionTTL)}
	a.mu.Unlock()
	http.SetCookie(w, a.cookie(r, token, int(sessionTTL.Seconds())))
	writeJSON(w, map[string]string{"user": in.User})
}

// check verifies a password, waiting for a free slot first.
func (a *auth) check(r *http.Request, user, password string) bool {
	select {
	case a.checking <- struct{}{}:
		defer func() { <-a.checking }()
	case <-r.Context().Done():
		return false
	}
	h, ok := a.users[user]
	if !ok {
		a.dummy.check(password)
		return false
	}
	return h.check(password)
}

func (a *auth) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		a.mu.Lock()
		delete(a.sessions, c.Value)
		a.mu.Unlock()
	}
	http.SetCookie(w, a.cookie(r, "", -1))
	w.WriteHeader(http.StatusNoContent)
}

// user returns the user signed on by the request's session cookie.
func (a *auth) user(r *http.Request) (string, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return "", false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.sessions[c.Value]
	if !ok || time.Now().After(s.expires) {
		return "", false
	}
	return s.user, true
}

// cookie is the session cookie. It is Secure when served over HTTPS; plain
// HTTP is only fit for localhost.
func (a *auth) cookie(r *http.Request, token string, maxAge int) *http.Cookie {
	return &http.Cookie{ //nolint:gosec // Secure is set whenever the request came over HTTPS
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteStrictMode,
	}
}
