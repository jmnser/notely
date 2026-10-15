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
	if _, err := fs.Stat(web, "web/app.wasm"); err != nil {
		log.Fatal("frontend is not embedded, build with: task build")
	}

	mux := http.NewServeMux()
	mux.Handle("/api/", &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
		r.SetURL(target)
		r.Out.Host = r.In.Host // keeps the backend's same-origin check valid
	}})
	mux.Handle("/web/", http.FileServerFS(web))
	mux.Handle("/", &app.Handler{
		Name:            "gnotes",
		Title:           "gnotes",
		Description:     "Offline-first Markdown notes",
		ThemeColor:      "#f3edf7",
		BackgroundColor: "#f3edf7",
		Icon: app.Icon{
			Default:  "/web/icon-192.png",
			Large:    "/web/icon-512.png",
			Maskable: "/web/icon-512.png",
			SVG:      "/web/icon.svg",
		},
		Styles:  []string{"/web/app.css"},
		Version: version,
	})
	if err := serve.Run(*addr, http.NewCrossOriginProtection().Handler(mux)); err != nil {
		log.Fatal(err)
	}
}
