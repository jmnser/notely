//go:build !wasm

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
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
		if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Content-Security-Policy") == "" {
			t.Errorf("base %q, path %q: security headers missing: %v", tc.base, tc.path, w.Header())
		}
	}
}

func TestRootResolver(t *testing.T) {
	for _, base := range []string{"", "/notely"} {
		r := rootResolver{ResourceResolver: app.PrefixedLocation(base), root: base + "/"}
		if got := r.Resolve("/"); got != base+"/" {
			t.Errorf("base %q: Resolve(/) = %q, want %q", base, got, base+"/")
		}
		if got := r.Resolve("/web/app.css"); got != base+"/web/app.css" {
			t.Errorf("base %q: Resolve(/web/app.css) = %q", base, got)
		}
	}
}

func TestManifestID(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/manifest+json")
		_, _ = w.Write([]byte(`{"start_url":"/notely/","theme_color":"#fff","background_color":"#fff"}`))
	})
	h := &manifestID{id: "/notely", next: next}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/manifest.webmanifest", nil))
	var m map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil || m["theme_color"] != "#fff" || m["background_color"] != barColor || m["id"] != "/notely" || m["start_url"] != "/notely/" {
		t.Errorf("manifest = %s (%v)", w.Body, err)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/manifest+json" {
		t.Errorf("Content-Type = %q", ct)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/app.js", nil))
	if w.Body.String() != `{"start_url":"/notely/","theme_color":"#fff","background_color":"#fff"}` {
		t.Errorf("other paths changed: %s", w.Body)
	}
}

func TestSecurityHeaders(t *testing.T) {
	w := httptest.NewRecorder()
	securityHeaders(http.NotFoundHandler()).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "img-src 'self' data: blob:") ||
		!strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("Content-Security-Policy = %q", csp)
	}
}
