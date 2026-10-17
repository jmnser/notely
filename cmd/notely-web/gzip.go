//go:build !wasm

package main

import (
	"bytes"
	"compress/gzip"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"
)

// compressible lists the extensions of files worth compressing.
var compressible = map[string]bool{".wasm": true, ".js": true, ".css": true, ".svg": true, ".json": true, ".md": true}

// gzipFiles serves the files of fsys gzip-compressed to clients that accept
// it and passes other requests to next. The files are embedded and never
// change, so each one is compressed once, on first request.
type gzipFiles struct {
	fsys  fs.FS
	next  http.Handler
	cache sync.Map // name → *gzipped
}

type gzipped struct {
	once sync.Once
	data []byte
	err  error
}

func (g *gzipFiles) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Vary", "Accept-Encoding")
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if !compressible[path.Ext(name)] || !acceptsGzip(r.Header.Get("Accept-Encoding")) {
		g.next.ServeHTTP(w, r)
		return
	}
	v, _ := g.cache.LoadOrStore(name, &gzipped{})
	z := v.(*gzipped)
	z.once.Do(func() { z.data, z.err = g.compress(name) })
	if z.err != nil { // missing files and directories: next answers
		g.next.ServeHTTP(w, r)
		return
	}
	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Set("Content-Type", mime.TypeByExtension(path.Ext(name)))
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(z.data))
}

func (g *gzipFiles) compress(name string) ([]byte, error) {
	data, err := fs.ReadFile(g.fsys, name)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if _, err := zw.Write(data); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// acceptsGzip reports whether an Accept-Encoding header allows gzip.
func acceptsGzip(header string) bool {
	for enc := range strings.SplitSeq(header, ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(enc), ";")
		if strings.EqualFold(strings.TrimSpace(name), "gzip") {
			q := strings.ReplaceAll(params, " ", "")
			return q != "q=0" && q != "q=0.0" && q != "q=0.00" && q != "q=0.000"
		}
	}
	return false
}
