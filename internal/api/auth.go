package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

const (
	defaultUser = "joe"
	// dummyHash is checked for unknown users, so they take as long as known
	// ones. Its password is irrelevant: no user has this hash.
	dummyHash = "$argon2id$v=19$m=65536,t=3,p=1$YmUyNzQ5MjdjYjExMGNiM2JkYzA5YTQ3YjRiZDM5NGY$" +
		"WKqtCW5E9T1E1Klb1kKlaC3Qi660yEtZnc67iibmWZw"
	cookieName   = "notely_session"
	sessionTTL   = 30 * 24 * time.Hour
	failureDelay = time.Second
	freeFailures = 5 // per user name before backing off
	maxBackoff   = time.Minute
)

// checkWait is how long a sign-in waits for a free password check slot.
var checkWait = 5 * time.Second

// errBusy reports that all password check slots stayed taken.
var errBusy = errors.New("busy")

// loadUsers reads the users file, a JSON object mapping user names to argon2id
// hashes. A missing file is created with the default user and a random
// password, which is logged once.
func loadUsers(path string) (map[string]argonHash, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is the -users flag
	if errors.Is(err, fs.ErrNotExist) {
		password := rand.Text()
		data, _ = json.MarshalIndent(map[string]string{defaultUser: hashPassword(password)}, "", "  ")
		if err = os.WriteFile(path, append(data, '\n'), 0o600); err == nil {
			log.Printf("created %s: sign in as %s with password %s, then change it", path, defaultUser, password)
		}
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

// sessionKey holds a func() bool reporting whether the request's session is
// still signed on, for long-lived responses.
type sessionKey struct{}

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
	sessions map[string]session  // token → session
	failures map[string]failures // user name → recent failed sign-ins
}

// failures counts a user name's failed sign-ins since the last success.
type failures struct {
	count int
	last  time.Time
}

// backoff is how long sign-ins for user wait after their failures: nothing
// for the first few, then doubling up to maxBackoff. It slows guessing a
// password to about one try a minute. Behind a proxy all clients share an
// address, so it counts per user name, not per address.
func (a *auth) backoff(user string) time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	f := a.failures[user]
	if f.count < freeFailures {
		return 0
	}
	wait := min(time.Second<<min(f.count-freeFailures, 10), maxBackoff)
	return time.Until(f.last.Add(wait))
}

// failed records a failed sign-in for user, or forgets them after a success.
func (a *auth) failed(user string, failed bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !failed {
		delete(a.failures, user)
		return
	}
	now := time.Now()
	if len(a.failures) > 10000 { // made-up names
		for u, f := range a.failures {
			if now.Sub(f.last) > maxBackoff {
				delete(a.failures, u)
			}
		}
	}
	f := a.failures[user]
	a.failures[user] = failures{count: f.count + 1, last: now}
}

func newAuth(users map[string]argonHash) *auth {
	dummy, _ := parseHash(dummyHash)
	return &auth{
		users: users, dummy: dummy, checking: make(chan struct{}, 2),
		sessions: map[string]session{}, failures: map[string]failures{},
	}
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
		ctx := context.WithValue(r.Context(), userKey{}, user)
		ctx = context.WithValue(ctx, sessionKey{}, func() bool { _, ok := a.user(r); return ok })
		next.ServeHTTP(w, r.WithContext(ctx))
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
	if wait := a.backoff(in.User); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		http.Error(w, "too many failed sign-ins, try again later", http.StatusTooManyRequests)
		return
	}
	ok, err := a.check(r, in.User, in.Password)
	switch {
	case errors.Is(err, errBusy):
		w.Header().Set("Retry-After", "5")
		http.Error(w, "busy, try again later", http.StatusServiceUnavailable)
		return
	case err != nil: // the client is gone
		return
	}
	a.failed(in.User, !ok)
	if !ok {
		select {
		case <-time.After(failureDelay):
		case <-r.Context().Done():
		}
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

// check verifies a password, waiting up to checkWait for a free slot first.
func (a *auth) check(r *http.Request, user, password string) (bool, error) {
	timer := time.NewTimer(checkWait)
	defer timer.Stop()
	select {
	case a.checking <- struct{}{}:
		defer func() { <-a.checking }()
	case <-timer.C:
		return false, errBusy
	case <-r.Context().Done():
		return false, r.Context().Err()
	}
	h, ok := a.users[user]
	if !ok {
		a.dummy.check(password)
		return false, nil
	}
	return h.check(password), nil
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
