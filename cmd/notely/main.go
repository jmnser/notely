//go:build !wasm

// Command notely serves the notely PWA and its API. Only this file and others
// built with !wasm may import internal/api, so it stays out of the browser.
package main

import (
	"bufio"
	"cmp"
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"net/http"
	"net/http/httptest"
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

	err = serve.Run(*addr, routes(*base, srv, &manifestID{id: cmp.Or(*base, "/"), next: &app.Handler{
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
		Resources:         rootResolver{ResourceResolver: app.PrefixedLocation(*base), root: *base + "/"},
		RawHeaders:        []string{themeColorScript},
	}}))
	stop()
	<-synced // pushes the last changes
	if err != nil {
		log.Fatal(err)
	}
}

// routes serves the API, the embedded files and the PWA under base. /healthz
// also answers without base, for probes that bypass the proxy in front.
func routes(base string, backend, pwa http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/", backend)
	mux.Handle("GET /healthz", backend)
	mux.Handle("/web/", &gzipFiles{fsys: web, next: http.FileServerFS(web)})
	mux.Handle("/", pwa)
	root := http.NewServeMux()
	root.Handle("GET /healthz", backend)
	root.Handle("/", withBase(base, mux))
	return http.NewCrossOriginProtection().Handler(root)
}

// rootResolver resolves the app's root to root, which ends in "/" so that the
// start URL lies in the service worker's scope (base/) and opens offline.
type rootResolver struct {
	app.ResourceResolver
	root string
}

func (r rootResolver) Resolve(location string) string {
	if location == "/" {
		return r.root
	}
	return r.ResourceResolver.Resolve(location)
}

// manifestID adds id to the web app manifest. Without one, Chrome identifies
// an installed app by its start URL; the id keeps installs from before the
// start URL gained its trailing slash the same app, so they update in place.
type manifestID struct {
	id   string
	next http.Handler
}

func (m *manifestID) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/manifest.webmanifest" {
		m.next.ServeHTTP(w, r)
		return
	}
	rec := httptest.NewRecorder()
	m.next.ServeHTTP(rec, r)
	var manifest map[string]any
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &manifest) != nil {
		maps.Copy(w.Header(), rec.Header())
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
		return
	}
	manifest["id"] = m.id
	maps.Copy(w.Header(), rec.Header())
	w.Header().Del("Content-Length")
	_ = json.NewEncoder(w).Encode(manifest)
}

// themeColorScript colors the browser and Android system bars like the app's
// surface, following the light or dark color scheme. Manifest colors cannot
// depend on the scheme.
const themeColorScript = `<script>
(() => {
  const dark = matchMedia("(prefers-color-scheme: dark)");
  const set = () => document.querySelector('meta[name="theme-color"]')
    ?.setAttribute("content", dark.matches ? "#211f26" : "#f3edf7");
  set();
  dark.addEventListener("change", set);
})();
</script>`

// withBase serves h under the path prefix base. The app's root is base/, so
// base redirects there.
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
		if rest == "" {
			to := base + "/"
			if r.URL.RawQuery != "" {
				to += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, to, http.StatusMovedPermanently) //nolint:gosec // fixed local path

			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path, r2.URL.RawPath = "/"+strings.TrimPrefix(rest, "/"), ""
		h.ServeHTTP(w, r2)
	})
}
