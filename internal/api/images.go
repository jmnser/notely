package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
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
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jmnser/notely/internal/atomicfile"
	"github.com/jmnser/notely/internal/note"
)

// Images are files in the space directories, next to the notes, named by a
// DNS label, as Kubernetes names, and their extension (note.ImageName).
// Notes reference them as drawings: by name in their own space, else by
// space path, so the Markdown also works in the git backup.
const (
	maxImage      = 2 << 20
	maxLabel      = 63
	fallbackLabel = "image" // when nothing of a file name is left
)

// imageTypes maps the image types accepted for upload to their file
// extension. SVG is left out: it can carry scripts.
var imageTypes = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

// hashName matches the names of images before they had readable ones: the
// first 128 bits of their SHA-256. They are valid image names too.
var hashName = regexp.MustCompile(`^[0-9a-f]{32}\.(png|jpg|gif|webp)$`)

var (
	errBadImage   = errors.New("use a PNG, JPEG, GIF or WebP image")
	errBadLabel   = errors.New("use up to 63 lower case letters, digits and -, starting and ending with a letter or digit")
	errImageTaken = errors.New("an image of that name is in that space")
)

// label turns a file name without its extension into a DNS label.
func label(file string) string {
	file = strings.TrimSuffix(file, path.Ext(file))
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(file) {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			dash = b.Len() > 0
			continue
		}
		if dash {
			b.WriteByte('-')
			dash = false
		}
		b.WriteRune(r)
	}
	s := strings.TrimRight(b.String()[:min(b.Len(), maxLabel)], "-")
	if s == "" {
		return fallbackLabel
	}
	return s
}

// version identifies the content of an image file; renames keep it.
func version(info fs.FileInfo) string {
	return strconv.FormatInt(info.ModTime().UnixNano(), 36) + "-" + strconv.FormatInt(info.Size(), 36)
}

func newImage(space, name string, info fs.FileInfo) note.Image {
	return note.Image{Space: space, Name: name, Uploaded: info.ModTime().UTC(), Version: version(info)}
}

// relSpace returns space without the shared space's name, the path below s;
// ok is false if space is not in s.
func (s *store) relSpace(space string) (string, bool) {
	if s.space == "" {
		return space, true
	}
	top, rest := topSpace(space)
	return rest, strings.EqualFold(top, s.space)
}

// dirOf returns the directory, relative to s.dir, of space: where its notes
// are, else where its first note would go. Call with s.mu held.
func (s *store) dirOf(space string) string {
	for id, n := range s.notes {
		if n.Space == space {
			return filepath.Dir(s.paths[id])
		}
	}
	rel, _ := s.relSpace(space)
	if rel == "" {
		return "."
	}
	return s.spaceDir(fileName(rel, "unnamed"))
}

// spaceFile holds, in a space directory with images, the space's path below
// the store: directory names lose the "/" of subspaces, and a space without
// notes has only its images.
const spaceFile = ".space"

// spaceOf returns the space of directory dir, relative to s.dir: that of its
// notes, else the one its space file names, else its name. Call with s.mu held.
func (s *store) spaceOf(dir string) string {
	found, ok := "", false
	for id, n := range s.notes {
		if filepath.Dir(s.paths[id]) == dir && (!ok || n.Space < found) {
			found, ok = n.Space, true
		}
	}
	if ok {
		return found
	}
	rel := ""
	if dir != "." {
		rel = dir
		data, err := os.ReadFile(filepath.Join(s.dir, dir, spaceFile)) //nolint:gosec // under the -data flag
		if p := note.SpacePath(string(data)); err == nil && p != "" && !strings.ContainsFunc(p, unicode.IsControl) {
			rel = p
		}
	}
	switch {
	case s.space == "":
		return rel
	case rel == "":
		return s.space
	}
	return s.space + "/" + rel
}

// markDir records space in directory dir's space file, relative to s.dir.
// Call with s.mu held.
func (s *store) markDir(dir, space string) {
	rel, _ := s.relSpace(space)
	p := filepath.Join(s.dir, dir, spaceFile)
	if dir == "." || rel == "" {
		return
	}
	if old, err := os.ReadFile(p); err == nil && string(old) == rel { //nolint:gosec // under the -data flag
		return
	}
	if _, err := os.Stat(filepath.Dir(p)); err != nil {
		return
	}
	if err := atomicfile.WriteFile(p, []byte(rel)); err != nil {
		log.Print(err)
	}
}

// removeDir removes directory dir, relative to s.dir, if it holds nothing
// but its space file.
func (s *store) removeDir(dir string) {
	if dir == "." || dir == "" {
		return
	}
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return
	}
	defer root.Close() //nolint:errcheck
	entries, err := fs.ReadDir(root.FS(), filepath.ToSlash(dir))
	if err == nil && len(entries) == 1 && entries[0].Name() == spaceFile {
		_ = root.Remove(filepath.Join(dir, spaceFile))
	}
	_ = root.Remove(dir) // fails while not empty
}

// images lists the images of s, newest first.
func (s *store) images() ([]note.Image, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	dirs := []string{"."}
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			dirs = append(dirs, e.Name())
		}
	}
	list := []note.Image{}
	for _, dir := range dirs {
		entries, err := os.ReadDir(filepath.Join(s.dir, dir))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		space := s.spaceOf(dir)
		for _, e := range entries {
			if !e.Type().IsRegular() || !note.ImageName.MatchString(e.Name()) {
				continue
			}
			info, err := e.Info()
			if errors.Is(err, fs.ErrNotExist) {
				continue
			} else if err != nil {
				return nil, err
			}
			list = append(list, newImage(space, e.Name(), info))
		}
	}
	slices.SortStableFunc(list, func(a, b note.Image) int { return b.Uploaded.Compare(a.Uploaded) })
	return list, nil
}

// imageNames returns the names of the images in dir, relative to s.dir.
func (s *store) imageNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.dir, dir))
	if errors.Is(err, fs.ErrNotExist) {
		return []string{}, nil
	} else if err != nil {
		return nil, err
	}
	names := []string{}
	for _, e := range entries {
		if e.Type().IsRegular() && note.ImageName.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// stemTaken reports whether an image in names, other than except, has stem.
func stemTaken(names []string, stem, except string) bool {
	return slices.ContainsFunc(names, func(name string) bool {
		return strings.TrimSuffix(name, path.Ext(name)) == stem && name != except
	})
}

// addImage stores image data in dir, relative to s.dir, the directory of
// space, named after file, with -2, -3, … on collisions, unless dir has the
// same image already. It returns the image's name. Call with s.mu held.
func (s *store) addImage(dir, space, file string, data []byte) (string, error) {
	ext, ok := imageTypes[http.DetectContentType(data)]
	if !ok {
		return "", errBadImage
	}
	names, err := s.imageNames(dir)
	if err != nil {
		return "", err
	}
	for _, name := range names {
		p := filepath.Join(s.dir, dir, name)
		if fi, err := os.Stat(p); err != nil || fi.Size() != int64(len(data)) {
			continue
		}
		if old, err := os.ReadFile(p); err == nil && bytes.Equal(old, data) { //nolint:gosec // under the -data flag
			return name, nil
		}
	}
	base := label(file)
	stem := base
	for i := 2; stemTaken(names, stem, ""); i++ {
		suffix := "-" + strconv.Itoa(i)
		stem = strings.TrimRight(base[:min(len(base), maxLabel-len(suffix))], "-") + suffix
	}
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return "", err
	}
	defer root.Close() //nolint:errcheck
	if err := root.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := writeFile(root, path.Join(filepath.ToSlash(dir), stem+ext), data); err != nil {
		return "", err
	}
	s.markDir(dir, space)
	return stem + ext, nil
}

// imageFile returns the path, relative to s.dir, of image name in space, if
// s has it.
func (s *store) imageFile(space, name string) (string, bool) {
	if !note.ImageName.MatchString(name) {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.relSpace(space); !ok {
		return "", false
	}
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return "", false
	}
	defer root.Close() //nolint:errcheck
	rel := filepath.Join(s.dirOf(space), name)
	if fi, err := root.Lstat(rel); err != nil || !fi.Mode().IsRegular() {
		return "", false
	}
	return rel, true
}

// findImage opens the first image called name in the user's directories,
// for references of the former layout, /images/<name>.
func (s *store) findImage(name string) (*os.File, error) {
	if !hashName.MatchString(name) {
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

// uses reports whether a note of s other than except references image img.
// Call with s.mu held.
func (s *store) uses(img note.Image, except string) bool {
	for id, n := range s.notes {
		if id != except && n.Kind != note.Drawing && slices.Contains(note.ImageRefs(n.Body, n.Space), img) {
			return true
		}
	}
	return false
}

// carryImages brings the images that body, of note id now in space,
// references there along from directory from, of any store, to directory
// to, relative to s.dir, when the note moves there: moved if no other note
// of the former space was uses them and from is in s, else copied. An image
// of the same name but other content in to makes it take another name; the
// returned body references it so. Call with s.mu held.
func (s *store) carryImages(id, body, space, from, to, was string, src *store) string {
	if filepath.Clean(from) == filepath.Join(s.dir, to) {
		return body
	}
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		log.Print(err)
		return body
	}
	defer root.Close() //nolint:errcheck
	for _, img := range slices.Compact(slices.SortedFunc(slices.Values(note.ImageRefs(body, space)), compareImages)) {
		if img.Space != space {
			continue // referenced by space path
		}
		p := filepath.Join(from, img.Name)
		data, err := os.ReadFile(p) //nolint:gosec // under the -data flag
		if errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			log.Printf("carry image %s: %v", p, err)
			continue
		}
		name := img.Name
		if old, err := root.ReadFile(path.Join(filepath.ToSlash(to), name)); err != nil || !bytes.Equal(old, data) {
			if name, err = s.addImage(to, space, name, data); err != nil {
				log.Printf("carry image %s: %v", p, err)
				continue
			}
		}
		if name != img.Name {
			body, _ = note.RewriteImageRefs(body, space, img, note.Image{Space: space, Name: name})
		}
		if src == s && !s.uses(note.Image{Space: was, Name: img.Name}, id) {
			if err := os.Remove(p); err != nil {
				log.Print(err)
			}
		}
	}
	return body
}

func compareImages(a, b note.Image) int {
	return strings.Compare(a.Space+"/"+a.Name, b.Space+"/"+b.Name)
}

// importImages copies the images body references from note id's directory in
// store from, or anywhere in it for references of the former layout, to its
// directory in s, when the note moves between stores. It returns body with
// the references to images that took another name changed.
func (s *store) importImages(id, body string, from *store) string {
	from.mu.Lock()
	dir, ok := from.noteDir(id)
	from.mu.Unlock()
	if !ok {
		return body
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	to, ok := s.noteDir(id)
	if !ok {
		return body
	}
	body = s.carryImages(id, body, s.notes[id].Space, filepath.Join(from.dir, dir), to, "", from)
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		log.Print(err)
		return body
	}
	defer root.Close() //nolint:errcheck
	for _, img := range note.ImageRefs(body, s.notes[id].Space) {
		dst := path.Join(filepath.ToSlash(to), img.Name)
		if _, err := root.Stat(dst); err == nil || !hashName.MatchString(img.Name) {
			continue
		}
		f, err := from.findImage(img.Name)
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(f, maxImage+1))
		_ = f.Close()
		if err == nil {
			err = writeFile(root, dst, data)
		}
		if err != nil {
			log.Printf("copy image %s: %v", img.Name, err)
		}
	}
	return body
}

// noteDir returns the directory, relative to s.dir, of note id. Call with s.mu held.
func (s *store) noteDir(id string) (string, bool) {
	rel, ok := s.paths[id]
	return filepath.Dir(rel), ok
}

// imageStores returns the stores that may hold images of space: a shared
// space's, then the user's own, which keeps notes a user had in a space
// before it was shared.
func (v *view) imageStores(space string) []*store {
	var stores []*store
	for _, s := range v.shared {
		if _, ok := s.store.relSpace(space); ok {
			stores = append(stores, s.store)
		}
	}
	return append(stores, v.own)
}

// image returns the store holding image name of space and its path there.
func (v *view) image(space, name string) (*store, string, bool) {
	for _, s := range v.imageStores(space) {
		if rel, ok := s.imageFile(space, name); ok {
			return s, rel, true
		}
	}
	return nil, "", false
}

// images lists the images of all stores, newest first. Of the same path
// in a shared space and the user's own store, only the shared one is
// reachable and listed.
func (v *view) images() ([]note.Image, error) {
	var list []note.Image
	seen := map[string]bool{}
	for _, s := range append(v.stores()[1:], v.own) {
		l, err := s.images()
		if err != nil {
			return nil, err
		}
		for _, img := range l {
			if p := strings.ToLower(img.Path()); !seen[p] {
				seen[p] = true
				list = append(list, img)
			}
		}
	}
	slices.SortStableFunc(list, func(a, b note.Image) int { return b.Uploaded.Compare(a.Uploaded) })
	return list, nil
}

// putImage stores an image next to note id, which may not move meanwhile,
// named after file, and returns its name and the users who see it.
func (v *view) putImage(id, file string, data []byte) (string, []string, error) {
	v.moves.Lock()
	defer v.moves.Unlock()
	s := v.find(id)
	if s == nil {
		return "", nil, fs.ErrNotExist
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, _ := s.noteDir(id)
	name, err := s.addImage(dir, s.notes[id].Space, file, data)
	return name, v.audience(s), err
}

// addImage stores an image in space, named after file.
func (v *view) addImage(space, file string, data []byte) (note.Image, []string, error) {
	v.moves.Lock()
	defer v.moves.Unlock()
	s, space := v.target(note.Note{Space: space}, nil)
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := s.dirOf(space)
	name, err := s.addImage(dir, space, file, data)
	if err != nil {
		return note.Image{}, nil, err
	}
	info, err := os.Stat(filepath.Join(s.dir, dir, name)) //nolint:gosec // a checked name under the -data flag
	if err != nil {
		return note.Image{}, nil, err
	}
	return newImage(space, name, info), v.audience(s), nil
}

// moveImage renames image from to label stem in space to, which may be in
// another store, and points the references of all notes to it. It returns
// the image and the users who see the change.
func (v *view) moveImage(from note.Image, to, stem string) (note.Image, []string, error) {
	if !note.ImageName.MatchString(stem + path.Ext(from.Name)) {
		return note.Image{}, nil, errBadLabel
	}
	v.moves.Lock()
	defer v.moves.Unlock()
	src, rel, ok := v.image(from.Space, from.Name)
	if !ok {
		return note.Image{}, nil, fs.ErrNotExist
	}
	dst, to := v.target(note.Note{Space: to}, nil)
	img := note.Image{Space: to, Name: stem + path.Ext(from.Name)}
	dst.mu.Lock()
	dir := dst.dirOf(to)
	out := filepath.Join(dir, img.Name)
	dst.mu.Unlock()
	if src == dst && out == rel {
		return v.imageInfo(dst, out, img)
	}
	names, err := dst.imageNames(dir)
	if err != nil {
		return note.Image{}, nil, err
	}
	except := "" // the image itself, when it stays in its directory
	if src == dst && filepath.Dir(rel) == dir {
		except = from.Name
	}
	if stemTaken(names, stem, except) {
		return note.Image{}, nil, errImageTaken
	}
	if err := os.MkdirAll(filepath.Join(dst.dir, dir), 0o700); err != nil {
		return note.Image{}, nil, err
	}
	if err := moveFile(filepath.Join(src.dir, rel), filepath.Join(dst.dir, out)); err != nil {
		return note.Image{}, nil, err
	}
	dst.mu.Lock()
	dst.markDir(dir, to)
	dst.mu.Unlock()
	src.removeDir(filepath.Dir(rel))
	users := union(v.audience(src), v.audience(dst))
	users = union(users, v.repoint(note.Image{Space: from.Space, Name: from.Name}, img))
	img, _, err = v.imageInfo(dst, out, img)
	return img, users, err
}

// imageInfo returns img, stored at rel in s, with its upload time and version.
func (v *view) imageInfo(s *store, rel string, img note.Image) (note.Image, []string, error) {
	info, err := os.Stat(filepath.Join(s.dir, rel))
	if err != nil {
		return note.Image{}, nil, err
	}
	return newImage(img.Space, img.Name, info), v.audience(s), nil
}

// moveFile moves file from to to, which may be on another file system.
func moveFile(from, to string) error {
	if err := os.Rename(from, to); err == nil {
		return nil
	}
	data, err := os.ReadFile(from) //nolint:gosec // under the -data flag
	if err != nil {
		return err
	}
	if err := atomicfile.WriteFile(to, data); err != nil {
		return err
	}
	return os.Remove(from)
}

// repoint points the references to image from in all notes to image to and
// returns the users who see the changed notes. Call with v.moves held.
func (v *view) repoint(from, to note.Image) []string {
	var users []string
	for _, s := range v.stores() {
		for _, n := range s.list() {
			if n.Kind == note.Drawing {
				continue
			}
			body, changed := note.RewriteImageRefs(n.Body, n.Space, from, to)
			if !changed {
				continue
			}
			n.Body = body
			if _, err := s.put(n); err != nil {
				log.Printf("point %s to %s: %v", n.ID, to.Path(), err)
				continue
			}
			users = union(users, v.audience(s))
		}
	}
	return users
}

// deleteImage deletes image name of space and returns the users who see it.
func (v *view) deleteImage(space, name string) ([]string, error) {
	v.moves.Lock()
	defer v.moves.Unlock()
	s, rel, ok := v.image(space, name)
	if !ok {
		return nil, nil
	}
	if err := os.Remove(filepath.Join(s.dir, rel)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	s.removeDir(filepath.Dir(rel))
	return v.audience(s), nil
}

// migrateAssets moves the images of the former Assets space, in .assets,
// into the space Assets, those of its subspaces into Assets/<subspace>,
// unless the user has the same image already, uploaded to a note.
func (s *store) migrateAssets() error {
	const assetDir = ".assets"
	from := filepath.Join(s.dir, assetDir)
	if _, err := os.Stat(from); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	have := map[[sha256.Size]byte]bool{}
	list, err := s.images()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, img := range list {
		data, err := os.ReadFile(filepath.Join(s.dir, s.dirOf(img.Space), img.Name)) //nolint:gosec // under the -data flag
		if err == nil {
			have[sha256.Sum256(data)] = true
		}
	}
	root, err := os.OpenRoot(from)
	if err != nil {
		return err
	}
	defer root.Close() //nolint:errcheck
	moved := 0
	err = fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !note.ImageName.MatchString(d.Name()) {
			return err
		}
		data, err := root.ReadFile(rel)
		if err != nil {
			return err
		}
		if sum := sha256.Sum256(data); !have[sum] {
			space := "Assets"
			if sub := path.Dir(rel); sub != "." {
				space += "/" + sub
			}
			if _, err := s.addImage(s.dirOf(space), space, d.Name(), data); err != nil {
				return err
			}
			have[sum] = true
			moved++
		}
		return root.Remove(rel)
	})
	if err != nil {
		return err
	}
	_ = os.RemoveAll(from) // left: what migrated before, or foreign files
	log.Printf("moved %d images of %s from Assets to the space Assets", moved, s.dir)
	return nil
}

// imageOf splits the {path...} of an image request into space and name.
func imageOf(r *http.Request) (space, name string) {
	space, name = path.Split(r.PathValue("path"))
	return note.SpacePath(space), name
}

// readImage reads an uploaded image, or answers why it cannot.
func readImage(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	// The server's read timeout is too short for 2 MiB over a slow phone link.
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(2 * time.Minute))
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxImage))
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		http.Error(w, "image larger than 2 MiB", http.StatusBadRequest)
		return nil, false
	} else if err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return nil, false
	}
	return data, true
}

// imageError answers a failed image request.
func imageError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errBadImage), errors.Is(err, errBadLabel), errors.Is(err, errImageTaken):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, fs.ErrNotExist):
		http.NotFound(w, r)
	default:
		serverError(w, err)
	}
}

// serveImage serves image file f, named name. A name can get other content:
// only a URL naming the version is cached. The ETag, not the modification
// time, revalidates, as renames keep it.
func serveImage(w http.ResponseWriter, r *http.Request, f *os.File, name string) {
	defer f.Close() //nolint:errcheck
	info, err := f.Stat()
	if err != nil {
		serverError(w, err)
		return
	}
	v := version(info)
	cache := "private, no-cache"
	if r.URL.Query().Get("v") == v {
		cache = "private, max-age=31536000, immutable"
	}
	for typ, ext := range imageTypes {
		if ext == path.Ext(name) {
			w.Header().Set("Content-Type", typ)
		}
	}
	w.Header().Set("ETag", `"`+v+`"`)
	w.Header().Set("Cache-Control", cache)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox; frame-ancestors 'none'")
	http.ServeContent(w, r, name, time.Time{}, f)
}

// decodeMove reads the body of PUT /api/images/{path...}: {"name": "<label>",
// "space": "<space>"}.
func decodeMove(w http.ResponseWriter, r *http.Request) (name, space string, ok bool) {
	var in struct{ Name, Space string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return "", "", false
	}
	return in.Name, note.SpacePath(in.Space), true
}
