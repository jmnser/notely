package api

import (
	"io"
	"net/http"
	"slices"
	"sync"
	"time"
)

const (
	maxListeners = 100 // in all
	maxPerUser   = 10
	heartbeat    = 25 * time.Second // keeps proxies from closing idle streams
	writeTimeout = 10 * time.Second
	clientHeader = "X-Notely-Client"
)

// events tells open apps when notes change, as server-sent events, so they
// sync at once. Each app names itself with a client ID and is not told about
// its own changes.
type events struct {
	mu        sync.Mutex
	listeners map[chan struct{}]listener
	done      chan struct{} // closed on shutdown
	closeOnce sync.Once
}

type listener struct{ client, user string }

func newEvents() *events {
	return &events{listeners: map[chan struct{}]listener{}, done: make(chan struct{})}
}

// notify tells the listeners of users but client, which made the change.
func (e *events) notify(users []string, client string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for c, l := range e.listeners {
		if slices.Contains(users, l.user) && (client == "" || l.client != client) {
			select {
			case c <- struct{}{}:
			default: // a change is already pending
			}
		}
	}
}

// close ends all streams, so they do not hold up the server's shutdown.
func (e *events) close() { e.closeOnce.Do(func() { close(e.done) }) }

func (e *events) serve(w http.ResponseWriter, r *http.Request) {
	client := r.URL.Query().Get("client")
	if client != "" && !validID.MatchString(client) {
		http.Error(w, "invalid client", http.StatusBadRequest)
		return
	}
	user := userOf(r)
	c := make(chan struct{}, 1)
	e.mu.Lock()
	full := len(e.listeners) >= maxListeners
	n := 0
	for _, l := range e.listeners {
		if l.user == user {
			n++
		}
	}
	if full = full || n >= maxPerUser; !full {
		e.listeners[c] = listener{client: client, user: user}
	}
	e.mu.Unlock()
	if full {
		w.Header().Set("Retry-After", "30")
		http.Error(w, "too many listeners", http.StatusServiceUnavailable)
		return
	}
	defer func() {
		e.mu.Lock()
		delete(e.listeners, c)
		e.mu.Unlock()
	}()

	rc := http.NewResponseController(w)
	// The server's read timeout would cancel the request after 30 s.
	if err := rc.SetReadDeadline(time.Time{}); err != nil {
		serverError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	// Auth is checked when the stream opens; signing out or the session
	// expiring ends it before the next message.
	signedOn, _ := r.Context().Value(sessionKey{}).(func() bool)
	send := func(msg string) bool {
		if signedOn != nil && !signedOn() {
			return false
		}
		// A client that stops reading must not hold up the shutdown.
		if err := rc.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
			return false
		}
		_, err := io.WriteString(w, msg)
		return err == nil && rc.Flush() == nil
	}
	if !send("retry: 5000\n\n") {
		return
	}
	tick := time.NewTicker(heartbeat)
	defer tick.Stop()
	for {
		var msg string
		select {
		case <-r.Context().Done():
			return
		case <-e.done:
			return
		case <-c:
			msg = "data: changed\n\n"
		case <-tick.C:
			msg = ": ping\n\n"
		}
		if !send(msg) {
			return
		}
	}
}
