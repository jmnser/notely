//go:build !wasm

package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWithBase(t *testing.T) {
	echo := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(r.URL.Path)) })
	for _, tc := range []struct{ base, path, want string }{
		{"", "/web/app.css", "/web/app.css"},
		{"/notely", "/notely", "301 /notely/"},
		{"/notely", "/notely?x=1", "301 /notely/?x=1"},
		{"/notely", "/notely/", "/"},
		{"/notely", "/notely/api/notes", "/api/notes"},
		{"/notely", "/notelyx", "404"},
		{"/notely", "/api/notes", "404"},
	} {
		w := httptest.NewRecorder()
		withBase(tc.base, echo).ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
		got := w.Body.String()
		switch w.Code {
		case http.StatusNotFound:
			got = "404"
		case http.StatusMovedPermanently:
			got = "301 " + w.Header().Get("Location")
		}
		if got != tc.want {
			t.Errorf("base %q, path %q: got %q, want %q", tc.base, tc.path, got, tc.want)
		}
	}
}

func TestRoutes(t *testing.T) {
	named := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(name + " " + r.URL.Path)) })
	}
	for _, tc := range []struct{ base, path, want string }{
		{"", "/", "pwa /"},
		{"", "/api/notes", "api /api/notes"},
		{"", "/healthz", "api /healthz"},
		{"/notely", "/notely/", "pwa /"},
		{"/notely", "/notely/api/notes", "api /api/notes"},
		{"/notely", "/notely/healthz", "api /healthz"},
		{"/notely", "/healthz", "api /healthz"},
		{"/notely", "/api/notes", "404"},
	} {
		w := httptest.NewRecorder()
		routes(tc.base, named("api"), named("pwa")).ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
		got := w.Body.String()
		if w.Code == http.StatusNotFound {
			got = "404"
		}
		if got != tc.want {
			t.Errorf("base %q, path %q: got %q, want %q", tc.base, tc.path, got, tc.want)
		}
	}
}
