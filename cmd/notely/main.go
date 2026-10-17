//go:build !wasm

// Command notely serves the notely PWA and its API. Only this file and others
// built with !wasm may import internal/api, so it stays out of the browser.
package main

import (
	"bufio"
	"context"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/maxence-charriere/go-app/v10/pkg/app"

	"github.com/jmnser/notely/internal/api"
	"github.com/jmnser/notely/internal/serve"
)

//go:embed web
var web embed.FS

// version is set at release time; when empty go-app generates one per start.
var version string

func main() {
	addr := flag.String("addr", "localhost:8000", "listen address")
	data := flag.String("data", "notes", "notes directory")
	usersFile := flag.String("users", "",
		"users file, created with the default user if missing (default <data>/.users.json)")
	interval := flag.Duration("sync-interval", time.Hour, "git backup interval; 0 syncs on request only")
	base := flag.String("base", "", "path prefix the app is served under, e.g. /notely")
	hash := flag.Bool("hash", false, "read a password from stdin, print its argon2id hash and exit")
	flag.Parse()

	if *hash {
		password, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if password = strings.TrimRight(password, "\r\n"); password == "" {
			log.Fatal("no password: ", err)
		}
		fmt.Println(api.HashPassword(password))
		return
	}
	if *base = strings.Trim(*base, "/"); *base != "" {
		*base = "/" + *base
	}

	wasm, err := fs.Stat(web, "web/app.wasm")
	if err != nil {
		log.Fatal("frontend is not embedded, build with: task build")
	}
	srv, err := api.Open(*data, *usersFile)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	synced := make(chan struct{})
	go func() {
		srv.RunSync(ctx, *interval)
		close(synced)
	}()

	mux := http.NewServeMux()
	mux.Handle("/api/", srv)
	mux.Handle("GET /healthz", srv)
	mux.Handle("/web/", &gzipFiles{fsys: web, next: http.FileServerFS(web)})
	mux.Handle("/", &app.Handler{
		Name:               "notely",
		Title:              "notely",
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
		Resources:         app.PrefixedLocation(*base),
	})
	err = serve.Run(*addr, http.NewCrossOriginProtection().Handler(withBase(*base, mux)))
	stop()
	<-synced // pushes the last changes
	if err != nil {
		log.Fatal(err)
	}
}

// withBase serves h under the path prefix base, which itself is the app's
// root as go-app links it (start URL, service worker).
func withBase(base string, h http.Handler) http.Handler {
	if base == "" {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest, ok := strings.CutPrefix(r.URL.Path, base)
		if !ok || rest != "" && rest[0] != '/' {
			http.NotFound(w, r)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path, r2.URL.RawPath = "/"+strings.TrimPrefix(rest, "/"), ""
		h.ServeHTTP(w, r2)
	})
}
