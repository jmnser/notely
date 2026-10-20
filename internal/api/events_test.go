package api

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// stream opens /api/events on srv and returns its lines without the retry
// field and blank lines; "closed" once it ends.
func stream(t *testing.T, srv *httptest.Server, query string) (*http.Response, func(time.Duration) string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/api/events"+query, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req) //nolint:bodyclose // closed by the caller; read in a goroutine
	if err != nil {
		t.Fatal(err)
	}
	lines := make(chan string)
	go func() {
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			if l := sc.Text(); l != "" && !strings.HasPrefix(l, "retry:") {
				lines <- l
			}
		}
		close(lines)
	}()
	return res, func(wait time.Duration) string {
		select {
		case l, ok := <-lines:
			if !ok {
				return "closed"
			}
			return l
		case <-time.After(wait):
			return "timeout"
		}
	}
}

func TestEvents(t *testing.T) {
	s, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ev := newEvents()
	h := newHandler(only(s), ev)
	var signedOn atomic.Bool
	signedOn.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), userKey{}, r.URL.Query().Get("user"))
		ctx = context.WithValue(ctx, sessionKey{}, signedOn.Load)
		h.ServeHTTP(w, r.WithContext(ctx))
	}))
	defer srv.Close()

	res, next := stream(t, srv, "?client=me")
	defer res.Body.Close() //nolint:errcheck // test
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}
	put := func(client string) {
		w := request(t, h, http.MethodPut, "/api/notes/a", `{"title":"a"}`, clientHeader, client)
		if w.Code != http.StatusOK {
			t.Fatalf("put: %d %s", w.Code, w.Body)
		}
	}
	put("me")
	if l := next(200 * time.Millisecond); l != "timeout" {
		t.Fatalf("told about its own change: %q", l)
	}
	put("other")
	if l := next(5 * time.Second); l != "data: changed" {
		t.Fatalf("after a change: %q", l)
	}
	signedOn.Store(false)
	put("other")
	if l := next(5 * time.Second); l != "closed" {
		t.Fatalf("after signing out: %q", l)
	}
	signedOn.Store(true)

	for i := range maxPerUser {
		res, next := stream(t, srv, "?user=ann")
		defer res.Body.Close() //nolint:errcheck // test
		if res.StatusCode != http.StatusOK {
			t.Fatalf("stream %d: %d", i, res.StatusCode)
		}
		if i == 0 {
			defer func() {
				ev.close()
				ev.close() // twice is fine
				if l := next(5 * time.Second); l != "closed" {
					t.Errorf("after close: %q", l)
				}
			}()
		}
	}
	res, _ = stream(t, srv, "?user=ann")
	_, _ = io.Copy(io.Discard, res.Body)
	res.Body.Close() //nolint:errcheck,gosec // test
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("stream over the limit: %d", res.StatusCode)
	}
	res, _ = stream(t, srv, "?user=bob")
	defer res.Body.Close() //nolint:errcheck // test
	if res.StatusCode != http.StatusOK {
		t.Errorf("other user: %d", res.StatusCode)
	}

	if w := request(t, h, http.MethodGet, "/api/events?client=a/b", ""); w.Code != http.StatusBadRequest {
		t.Errorf("invalid client: %d", w.Code)
	}
}
