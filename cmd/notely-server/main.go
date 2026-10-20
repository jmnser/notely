//go:build !wasm

// Command notely-server serves the notely PWA and its API. Only this file and others
// built with !wasm may import internal/api, so it stays out of the browser.
package main

import (
	"bufio"
	"bytes"
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
		password, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if password = strings.TrimRight(password, "\r\n"); password == "" {
			log.Fatal("no password on stdin")
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
		Name:        "Notely",
		Title:       "Notely",
		Description: "Offline-first Markdown notes",
		// Android colors the navigation bar from the manifest theme color only,
		// in both schemes; themeColorScript follows the scheme elsewhere.
		ThemeColor:         barColor,
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
	}}), srv.Shutdown)
	if err != nil { // e.g. the address is in use: there is nothing new to back up
		log.Fatal(err)
	}
	stop()
	<-synced // pushes the last changes
}

// routes serves the API, the embedded files and the PWA under base. /healthz
// also answers without base, for probes that bypass the proxy in front. The
// API guards itself against cross-origin requests.
func routes(base string, backend, pwa http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/", backend)
	mux.Handle("GET /healthz", backend)
	mux.Handle("/web/", &gzipFiles{fsys: web, next: http.FileServerFS(web)})
	mux.Handle("/", pwa)
	root := http.NewServeMux()
	root.Handle("GET /healthz", backend)
	root.Handle("/", withBase(base, mux))
	return securityHeaders(root)
}

// securityHeaders forbids framing, MIME sniffing and images from other sites
// and keeps paths out of the Referer sent to other sites.
func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr := w.Header()
		// Images only from here: notes cannot make readers' browsers request
		// other sites, which could track when a note is read.
		hdr.Set("Content-Security-Policy", "frame-ancestors 'none'; img-src 'self' data: blob:")
		hdr.Set("X-Frame-Options", "DENY")
		hdr.Set("X-Content-Type-Options", "nosniff")
		hdr.Set("Referrer-Policy", "same-origin")
		h.ServeHTTP(w, r)
	})
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

// barColor is the dark surface, used for the Android system bars.
const barColor = "#211f26"

// manifestID adds id and the dark background_color to the web app manifest.
// Without an id, Chrome identifies
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
	buf := &bufferedResponse{header: http.Header{}, code: http.StatusOK}
	m.next.ServeHTTP(buf, r)
	maps.Copy(w.Header(), buf.header)
	var manifest map[string]any
	if buf.code != http.StatusOK || json.Unmarshal(buf.body.Bytes(), &manifest) != nil {
		w.WriteHeader(buf.code)
		_, _ = w.Write(buf.body.Bytes())
		return
	}
	manifest["id"] = m.id
	// Android 15+ ignores the navigation bar color and shows the window
	// background, which Chrome takes from background_color. The page keeps
	// the light BackgroundColor, so light mode does not flash dark on load.
	manifest["background_color"] = barColor
	w.Header().Del("Content-Length")
	_ = json.NewEncoder(w).Encode(manifest)
}

// bufferedResponse keeps a response in memory.
type bufferedResponse struct {
	header http.Header
	code   int
	body   bytes.Buffer
}

func (b *bufferedResponse) Header() http.Header         { return b.header }
func (b *bufferedResponse) WriteHeader(code int)        { b.code = code }
func (b *bufferedResponse) Write(p []byte) (int, error) { return b.body.Write(p) }

// themeColorScript colors the browser and Android system bars like the app's
// surface, following the light or dark color scheme.
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
