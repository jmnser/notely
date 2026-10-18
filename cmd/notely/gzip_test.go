//go:build !wasm

package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestGzipFiles(t *testing.T) {
	big := strings.Repeat("wasm ", 1000)
	fsys := fstest.MapFS{"web/app.wasm": {Data: []byte(big)}, "web/icon.png": {Data: []byte("png")}}
	h := &gzipFiles{fsys: fsys, next: http.FileServerFS(fsys)}
	get := func(path, accept string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		if accept != "" {
			r.Header.Set("Accept-Encoding", accept)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	w := get("/web/app.wasm", "br, gzip;q=0.8")
	if w.Header().Get("Content-Encoding") != "gzip" || w.Header().Get("Content-Type") != "application/wasm" ||
		w.Body.Len() >= len(big) {
		t.Fatalf("gzip: %v, %d bytes", w.Header(), w.Body.Len())
	}
	zr, err := gzip.NewReader(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := io.ReadAll(zr); string(data) != big {
		t.Error("gzip body does not decompress to the file")
	}
	for _, c := range []struct{ path, accept string }{
		{"/web/app.wasm", ""},
		{"/web/app.wasm", "gzip;q=0"},
		{"/web/icon.png", "gzip"},
	} {
		w := get(c.path, c.accept)
		if w.Header().Get("Content-Encoding") != "" || w.Code != http.StatusOK {
			t.Errorf("%s %q: %d %v", c.path, c.accept, w.Code, w.Header())
		}
		if !strings.Contains(w.Header().Get("Vary"), "Accept-Encoding") {
			t.Errorf("%s %q: no Vary", c.path, c.accept)
		}
	}
	if w := get("/web/missing.js", "gzip"); w.Code != http.StatusNotFound {
		t.Errorf("missing file: %d", w.Code)
	}
	n := 0
	h.cache.Range(func(any, any) bool { n++; return true })
	if n != 1 {
		t.Errorf("cache holds %d files, want 1", n)
	}
}
