package api

import (
	"io"
	"net/http"
	"sync"
	"time"
)

const (
	maxListeners = 100
	heartbeat    = 25 * time.Second // keeps proxies from closing idle streams
	clientHeader = "X-Notely-Client"
)

// events tells open apps when notes change, as server-sent events, so they
// sync at once. Each app names itself with a client ID and is not told about
// its own changes.
type events struct {
	mu        sync.Mutex
	listeners map[chan struct{}]string // channel → client ID
	done      chan struct{}            // closed on shutdown
}

func newEvents() *events {
	return &events{listeners: map[chan struct{}]string{}, done: make(chan struct{})}
}

// notify tells all listeners but client, which made the change.
func (e *events) notify(client string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for c, id := range e.listeners {
		if client == "" || id != client {
			select {
			case c <- struct{}{}:
			default: // a change is already pending
			}
		}
	}
}

// close ends all streams, so they do not hold up the server's shutdown.
func (e *events) close() { close(e.done) }

func (e *events) serve(w http.ResponseWriter, r *http.Request) {
	client := r.URL.Query().Get("client")
	if client != "" && !validID.MatchString(client) {
		http.Error(w, "invalid client", http.StatusBadRequest)
		return
	}
	c := make(chan struct{}, 1)
	e.mu.Lock()
	full := len(e.listeners) >= maxListeners
	if !full {
		e.listeners[c] = client
	}
	e.mu.Unlock()
	if full {
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
	send := func(msg string) bool {
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
