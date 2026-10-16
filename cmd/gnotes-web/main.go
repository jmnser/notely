//go:build !wasm

// Command gnotes-web serves the gnotes PWA and proxies /api/ to the backend.
package main

import (
	"embed"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"

	"github.com/maxence-charriere/go-app/v10/pkg/app"

	"github.com/jmnser/gnotes/internal/serve"
)

//go:embed web
var web embed.FS

// version is set at release time; when empty go-app generates one per start.
var version string

func main() {
	addr := flag.String("addr", "localhost:8000", "listen address")
	api := flag.String("api", "http://localhost:8001", "backend URL")
	flag.Parse()

	target, err := url.Parse(*api)
	if err != nil {
		log.Fatal(err)
	}
	wasm, err := fs.Stat(web, "web/app.wasm")
	if err != nil {
		log.Fatal("frontend is not embedded, build with: task build")
	}

	mux := http.NewServeMux()
	mux.Handle("/api/", &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
		r.SetURL(target)
		r.SetXForwarded()      // lets the backend mark cookies Secure behind HTTPS
		r.Out.Host = r.In.Host // keeps the backend's same-origin check valid
	}})
	mux.Handle("/web/", &gzipFiles{fsys: web, next: http.FileServerFS(web)})
	mux.Handle("/", &app.Handler{
		Name:               "gnotes",
		Title:              "gnotes",
		Description:        "Offline-first Markdown notes",
		ThemeColor:         "#f3edf7",
		BackgroundColor:    "#f3edf7",
		CacheableResources: []string{"/web/mermaid/mermaid.min.js"}, // loaded on demand, kept for offline use
		Icon: app.Icon{
			Default:  "/web/icon-192.png",
			Large:    "/web/icon-512.png",
			Maskable: "/web/icon-512.png",
			SVG:      "/web/icon.svg",
		},
		Styles:            []string{"/web/app.css"},
		WasmContentLength: strconv.FormatInt(wasm.Size(), 10), // the response may be gzip-compressed
		Version:           version,
	})
	if err := serve.Run(*addr, http.NewCrossOriginProtection().Handler(mux)); err != nil {
		log.Fatal(err)
	}
}
