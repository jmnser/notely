package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	maxImage = 2 << 20
	// imageGrace keeps unreferenced images this long, for notes that
	// reference them but are still waiting on an offline device to sync.
	imageGrace = 7 * 24 * time.Hour
)

// imageTypes maps the image types accepted for upload to their file
// extension. SVG is left out: it can carry scripts.
var imageTypes = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

var (
	imageName = regexp.MustCompile(`^[0-9a-f]{32}\.(png|jpg|gif|webp)$`)
	// imageRef finds the image names a note body mentions, in any form.
	imageRef = regexp.MustCompile(`[0-9a-f]{32}\.(?:png|jpg|gif|webp)`)
)

// Images live next to the notes that use them, in their space directory,
// named after a hash of their content: an image is stored once per
// directory, and a name never changes its content. Notes reference them by
// name, relative to themselves, so the Markdown also works in the git backup.

// noteDir returns the directory, relative to s.dir, of note id. Call with s.mu held.
func (s *store) noteDir(id string) (string, bool) {
	rel, ok := s.paths[id]
	return filepath.Dir(rel), ok
}

// putImage stores data next to note id, or in the top directory for no id,
// and returns its name. Storing an image again restarts its grace period.
func (s *store) putImage(id string, data []byte) (string, error) {
	ext, ok := imageTypes[http.DetectContentType(data)]
	if !ok {
		return "", errBadImage
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:16]) + ext

	s.mu.Lock()
	defer s.mu.Unlock()
	dir := "."
	if id != "" {
		var ok bool
		if dir, ok = s.noteDir(id); !ok {
			return "", fs.ErrNotExist
		}
	}
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return "", err
	}
	defer root.Close() //nolint:errcheck
	rel := path.Join(filepath.ToSlash(dir), name)
	if _, err := root.Stat(rel); err == nil {
		now := time.Now()
		return name, root.Chtimes(rel, now, now)
	}
	return name, writeFile(root, rel, data)
}

var errBadImage = errors.New("use a PNG, JPEG, GIF or WebP image")

// openImage opens image name next to note id.
func (s *store) openImage(id, name string) (*os.File, error) {
	if !imageName.MatchString(name) {
		return nil, fs.ErrNotExist
	}
	s.mu.Lock()
	dir, ok := s.noteDir(id)
	s.mu.Unlock()
	if !ok {
		return nil, fs.ErrNotExist
	}
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return nil, err
	}
	defer root.Close() //nolint:errcheck
	return root.Open(path.Join(filepath.ToSlash(dir), name))
}

// findImage opens the first image called name in the user's directories,
// for references of the former layout, /images/<name>.
func (s *store) findImage(name string) (*os.File, error) {
	if !imageName.MatchString(name) {
		return nil, fs.ErrNotExist
	}
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return nil, err
	}
	defer root.Close() //nolint:errcheck
	found := ""
	err = fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case rel != "." && strings.HasPrefix(d.Name(), "."):
			if d.IsDir() {
				return fs.SkipDir
			}
		case d.Name() == name && d.Type().IsRegular():
			found = rel
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if found == "" {
		return nil, fs.ErrNotExist
	}
	return root.Open(found)
}

// images returns the names of the images next to note id.
func (s *store) images(id string) ([]string, error) {
	s.mu.Lock()
	dir, ok := s.noteDir(id)
	s.mu.Unlock()
	if !ok {
		return nil, fs.ErrNotExist
	}
	entries, err := os.ReadDir(filepath.Join(s.dir, dir))
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, e := range entries {
		if e.Type().IsRegular() && imageName.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// copyImages copies the images body references from directory from to to,
// when a note moves there. Call with s.mu held.
func (s *store) copyImages(body, from, to string) {
	if from == to {
		return
	}
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		log.Print(err)
		return
	}
	defer root.Close() //nolint:errcheck
	for _, name := range slices.Compact(slices.Sorted(slices.Values(imageRef.FindAllString(body, -1)))) {
		src, dst := path.Join(filepath.ToSlash(from), name), path.Join(filepath.ToSlash(to), name)
		if _, err := root.Stat(dst); err == nil {
			continue
		}
		data, err := root.ReadFile(src)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err == nil {
			err = writeFile(root, dst, data)
		}
		if err != nil {
			log.Printf("copy image %s: %v", src, err)
		}
	}
}

// cleanImages deletes the images no note in their directory references and
// that were not stored within imageGrace, then directories left empty.
func (s *store) cleanImages(now time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Lower-case slash-separated paths relative to s.dir: directories
	// differing in case may be the same on the file system.
	used := map[string]bool{}
	anywhere := map[string]bool{} // names referenced as /images/<name>, found in any directory
	for id, n := range s.notes {
		dir, _ := s.noteDir(id)
		for _, name := range imageRef.FindAllString(n.Body, -1) {
			used[strings.ToLower(path.Join(filepath.ToSlash(dir), name))] = true
		}
		for _, m := range oldImageRef.FindAllStringSubmatch(n.Body, -1) {
			anywhere[m[2]] = true
		}
	}
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return 0, err
	}
	defer root.Close() //nolint:errcheck
	removed := 0
	var dirs []string
	err = fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case rel != "." && strings.HasPrefix(d.Name(), "."):
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		case d.IsDir():
			if rel != "." {
				dirs = append(dirs, rel)
			}
			return nil
		case !d.Type().IsRegular() || !imageName.MatchString(d.Name()) || used[strings.ToLower(rel)] || anywhere[d.Name()]:
			return nil
		}
		info, err := d.Info()
		if err != nil || now.Sub(info.ModTime()) < imageGrace {
			return err
		}
		if err := root.Remove(rel); err != nil {
			return err
		}
		removed++
		return nil
	})
	for _, dir := range slices.Backward(dirs) {
		_ = root.Remove(dir) // fails while not empty
	}
	return removed, err
}

// nextCleanup returns the next Sunday 23:00 after now, in now's time zone.
func nextCleanup(now time.Time) time.Time {
	t := time.Date(now.Year(), now.Month(), now.Day(), 23, 0, 0, 0, now.Location())
	t = t.AddDate(0, 0, int(time.Sunday-t.Weekday()+7)%7)
	if !t.After(now) {
		t = t.AddDate(0, 0, 7)
	}
	return t
}

func (s *store) uploadImage(w http.ResponseWriter, r *http.Request) {
	// The server's read timeout is too short for 2 MiB over a slow phone link.
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(2 * time.Minute))
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxImage))
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		http.Error(w, "image larger than 2 MiB", http.StatusBadRequest)
		return
	} else if err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	name, err := s.putImage(id, data)
	switch {
	case errors.Is(err, errBadImage):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, fs.ErrNotExist):
		http.Error(w, "no such note", http.StatusNotFound)
	case err != nil:
		serverError(w, err)
	case id == "": // POST /api/images of app versions before images moved next to notes
		writeJSON(w, map[string]string{"path": "/images/" + name})
	default:
		writeJSON(w, map[string]string{"name": name})
	}
}

func (s *store) serveImage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var f *os.File
	var err error
	if id := r.PathValue("id"); id != "" {
		f, err = s.openImage(id, name)
	} else {
		f, err = s.findImage(name) // GET /api/images/{name}: a reference of the former layout
	}
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

func (s *store) listImages(w http.ResponseWriter, r *http.Request) {
	names, err := s.images(r.PathValue("id"))
	if errors.Is(err, fs.ErrNotExist) {
		http.Error(w, "no such note", http.StatusNotFound)
		return
	} else if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, names)
}
