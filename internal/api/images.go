package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"regexp"
	"time"
)

const maxImage = 10 << 20

// imageTypes maps the image types accepted for upload to their file
// extension. SVG is left out: it can carry scripts.
var imageTypes = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

var imageName = regexp.MustCompile(`^[0-9a-f]{32}\.(png|jpg|gif|webp)$`)

// images stores uploaded images in dir, named after a hash of their content,
// so the same image is stored once and a name never changes its content.
type images struct {
	dir string
}

// root opens the image directory, creating it.
func (im *images) root() (*os.Root, error) {
	if err := os.MkdirAll(im.dir, 0o700); err != nil {
		return nil, err
	}
	return os.OpenRoot(im.dir)
}

// handler serves POST /api/images, which answers {"path": "/images/<name>"}
// for use in Markdown, and GET /api/images/{name}.
func (im *images) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/images", im.upload)
	mux.HandleFunc("GET /api/images/{name}", im.serve)
	return mux
}

func (im *images) upload(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxImage))
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		http.Error(w, "image larger than 10 MiB", http.StatusBadRequest)
		return
	} else if err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	ext, ok := imageTypes[http.DetectContentType(data)]
	if !ok {
		http.Error(w, "use a PNG, JPEG, GIF or WebP image", http.StatusBadRequest)
		return
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:16]) + ext
	root, err := im.root()
	if err != nil {
		serverError(w, err)
		return
	}
	defer root.Close() //nolint:errcheck
	if _, err = root.Stat(name); errors.Is(err, fs.ErrNotExist) {
		err = writeFile(root, name, data)
	}
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, map[string]string{"path": "/images/" + name})
}

func (im *images) serve(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !imageName.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	root, err := im.root()
	if err != nil {
		serverError(w, err)
		return
	}
	defer root.Close() //nolint:errcheck
	f, err := root.Open(name)
	if errors.Is(err, fs.ErrNotExist) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		serverError(w, err)
		return
	}
	defer f.Close() //nolint:errcheck
	for typ, ext := range imageTypes {
		if ext == path.Ext(name) {
			w.Header().Set("Content-Type", typ)
		}
	}
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable") // the name is the content
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox; frame-ancestors 'none'")
	http.ServeContent(w, r, name, time.Time{}, f)
}
