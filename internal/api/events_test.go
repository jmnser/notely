package api

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEvents(t *testing.T) {
	s, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ev := newEvents()
	srv := httptest.NewServer(newHandler(s, ev))
	defer srv.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/api/events?client=me", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req) //nolint:bodyclose // closed below; read in a goroutine
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close() //nolint:errcheck // test
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}
	lines := make(chan string)
	go func() {
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	next := func(wait time.Duration) string {
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					return "closed"
				}
				if l != "" && !strings.HasPrefix(l, "retry:") {
					return l
				}
			case <-time.After(wait):
				return "timeout"
			}
		}
	}

	put := func(client string) {
		r := httptest.NewRequest(http.MethodPut, "/api/notes/a", strings.NewReader(`{"title":"a"}`))
		r.Header.Set(clientHeader, client)
		newHandler(s, ev).ServeHTTP(httptest.NewRecorder(), r)
	}
	put("me")
	if l := next(200 * time.Millisecond); l != "timeout" {
		t.Fatalf("told about its own change: %q", l)
	}
	put("other")
	if l := next(5 * time.Second); l != "data: changed" {
		t.Fatalf("after a change: %q", l)
	}
	ev.close()
	if l := next(5 * time.Second); l != "closed" {
		t.Fatalf("after close: %q", l)
	}

	w := request(t, newHandler(s, ev), http.MethodGet, "/api/events?client=a/b", "")
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid client: %d", w.Code)
	}
}
