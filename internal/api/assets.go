package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"maps"
	"net/http"
	"os"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Assets are the images a user uploaded, in <dir>/.assets: <name>.<ext>, or
// <space>/<name>.<ext> in one of its subspaces. Names and subspaces are DNS
// labels, as Kubernetes names. The dot directory keeps them out of the notes,
// the image cleanup and the git backup.
const (
	assetDir      = ".assets"
	maxLabel      = 63
	fallbackLabel = "image" // when nothing of a file name is left
)

// dnsLabel matches an RFC 1123 label: up to 63 lower case letters, digits
// and dashes, starting and ending with a letter or digit.
var dnsLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

var errBadLabel = errors.New("use up to 63 lower case letters, digits and -, " +
	"starting and ending with a letter or digit")

// asset is an uploaded image listed in the Assets space.
type asset struct {
	Name     string    `json:"name"`            // DNS label and extension
	Space    string    `json:"space,omitempty"` // subspace, a DNS label
	Uploaded time.Time `json:"uploaded"`
}

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

// assetPath returns the path of space/name in .assets, or false if either is
// not valid.
func assetPath(space, name string) (string, bool) {
	stem, ext := strings.TrimSuffix(name, path.Ext(name)), path.Ext(name)
	if !dnsLabel.MatchString(stem) || !slices.Contains(slices.Collect(maps.Values(imageTypes)), ext) ||
		space != "" && !dnsLabel.MatchString(space) {
		return "", false
	}
	return path.Join(assetDir, space, name), true
}

// assets lists the user's assets, newest first. Call with s.mu held.
func (s *store) assets(root *os.Root) ([]asset, error) {
	list := []asset{}
	err := fs.WalkDir(root.FS(), assetDir, func(rel string, d fs.DirEntry, err error) error {
		switch {
		case errors.Is(err, fs.ErrNotExist) && rel == assetDir:
			return fs.SkipAll
		case err != nil:
			return err
		case d.IsDir():
			return nil
		}
		space, name := path.Split(strings.TrimPrefix(rel, assetDir+"/"))
		space = strings.TrimSuffix(space, "/")
		if p, ok := assetPath(space, name); !ok || p != rel || !d.Type().IsRegular() {
			return nil // temp files, foreign files
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		list = append(list, asset{Name: name, Space: space, Uploaded: info.ModTime().UTC()})
		return nil
	})
	slices.SortStableFunc(list, func(a, b asset) int { return b.Uploaded.Compare(a.Uploaded) })
	return list, err
}

// taken reports whether label stem is used in space by an asset other than except.
func taken(list []asset, space, stem, except string) bool {
	return slices.ContainsFunc(list, func(a asset) bool {
		return a.Space == space && strings.TrimSuffix(a.Name, path.Ext(a.Name)) == stem && a.Name != except
	})
}

// addAsset lists image data, of extension ext, in the Assets space, named
// after file, unless the user already has it. It returns the asset.
func (s *store) addAsset(data []byte, ext, file, space string) (asset, error) {
	if space != "" && !dnsLabel.MatchString(space) {
		return asset{}, errBadLabel
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return asset{}, err
	}
	defer root.Close() //nolint:errcheck
	list, err := s.assets(root)
	if err != nil {
		return asset{}, err
	}
	for _, a := range list {
		p, _ := assetPath(a.Space, a.Name)
		if info, err := root.Stat(p); err != nil || info.Size() != int64(len(data)) {
			continue
		}
		if old, err := root.ReadFile(p); err == nil && bytes.Equal(old, data) {
			return a, nil
		}
	}
	stem := label(file)
	for i := 2; taken(list, space, stem, ""); i++ {
		suffix := "-" + strconv.Itoa(i)
		stem = strings.TrimRight(label(file)[:min(len(label(file)), maxLabel-len(suffix))], "-") + suffix
	}
	if err := root.MkdirAll(path.Join(assetDir, space), 0o700); err != nil {
		return asset{}, err
	}
	a := asset{Name: stem + ext, Space: space}
	p, _ := assetPath(space, a.Name)
	if err := writeFile(root, p, data); err != nil {
		return asset{}, err
	}
	info, err := root.Stat(p)
	if err == nil {
		a.Uploaded = info.ModTime().UTC()
	}
	return a, err
}

// moveAsset renames asset space/name to label stem in subspace to, keeping
// its extension, and removes a subspace left empty.
func (s *store) moveAsset(space, name, to, stem string) (asset, error) {
	from, ok := assetPath(space, name)
	if !ok {
		return asset{}, fs.ErrNotExist
	}
	a := asset{Name: stem + path.Ext(name), Space: to}
	dst, ok := assetPath(to, a.Name)
	if !ok {
		return asset{}, errBadLabel
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return asset{}, err
	}
	defer root.Close() //nolint:errcheck
	list, err := s.assets(root)
	if err != nil {
		return asset{}, err
	}
	if !slices.ContainsFunc(list, func(a asset) bool { return a.Space == space && a.Name == name }) {
		return asset{}, fs.ErrNotExist
	}
	if dst == from {
		return a, nil
	}
	except := "" // the asset itself, when it stays in its space
	if space == to {
		except = name
	}
	if taken(list, to, stem, except) {
		return asset{}, errAssetTaken
	}
	if err := root.MkdirAll(path.Join(assetDir, to), 0o700); err != nil {
		return asset{}, err
	}
	if err := root.Rename(from, dst); err != nil {
		return asset{}, err
	}
	if space != "" {
		_ = root.Remove(path.Join(assetDir, space)) // fails while not empty
	}
	info, err := root.Stat(dst)
	if err == nil {
		a.Uploaded = info.ModTime().UTC()
	}
	return a, err
}

var errAssetTaken = errors.New("an asset of that name is in that space")

// deleteAsset deletes asset space/name and removes a subspace left empty.
func (s *store) deleteAsset(space, name string) error {
	p, ok := assetPath(space, name)
	if !ok {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return err
	}
	defer root.Close() //nolint:errcheck
	if err := root.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if space != "" {
		_ = root.Remove(path.Join(assetDir, space)) // fails while not empty
	}
	return nil
}

// assetOf splits the {path...} of an asset request into space and name.
func assetOf(r *http.Request) (space, name string) {
	space, name = path.Split(r.PathValue("path"))
	return strings.TrimSuffix(space, "/"), name
}

func (s *store) listAssets(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		serverError(w, err)
		return
	}
	defer root.Close() //nolint:errcheck
	list, err := s.assets(root)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, list)
}

// uploadAsset serves POST /api/assets?name=<file name>&space=<subspace>.
func (s *store) uploadAsset(w http.ResponseWriter, r *http.Request) {
	data, ok := readImage(w, r)
	if !ok {
		return
	}
	ext, ok := imageTypes[http.DetectContentType(data)]
	if !ok {
		http.Error(w, errBadImage.Error(), http.StatusBadRequest)
		return
	}
	a, err := s.addAsset(data, ext, r.URL.Query().Get("name"), r.URL.Query().Get("space"))
	if errors.Is(err, errBadLabel) {
		http.Error(w, "subspace: "+err.Error(), http.StatusBadRequest)
		return
	} else if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, a)
}

func (s *store) serveAsset(w http.ResponseWriter, r *http.Request) {
	space, name := assetOf(r)
	p, ok := assetPath(space, name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		serverError(w, err)
		return
	}
	defer root.Close() //nolint:errcheck
	f, err := root.Open(p)
	if errors.Is(err, fs.ErrNotExist) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		serverError(w, err)
		return
	}
	defer f.Close() //nolint:errcheck
	info, err := f.Stat()
	if err != nil {
		serverError(w, err)
		return
	}
	for typ, ext := range imageTypes {
		if ext == path.Ext(name) {
			w.Header().Set("Content-Type", typ)
		}
	}
	w.Header().Set("Cache-Control", "private, no-cache") // a name can get other content
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox; frame-ancestors 'none'")
	http.ServeContent(w, r, name, info.ModTime(), f)
}

// putAsset renames or moves an asset: {"name": "<label>", "space": "<label>"}.
func (s *store) putAsset(w http.ResponseWriter, r *http.Request) {
	var in struct{ Name, Space string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if !dnsLabel.MatchString(in.Name) || in.Space != "" && !dnsLabel.MatchString(in.Space) {
		http.Error(w, "names and subspaces: "+errBadLabel.Error(), http.StatusBadRequest)
		return
	}
	space, name := assetOf(r)
	a, err := s.moveAsset(space, name, in.Space, in.Name)
	switch {
	case errors.Is(err, errAssetTaken):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, fs.ErrNotExist):
		http.NotFound(w, r)
	case err != nil:
		serverError(w, err)
	default:
		writeJSON(w, a)
	}
}

func (s *store) removeAsset(w http.ResponseWriter, r *http.Request) {
	if err := s.deleteAsset(assetOf(r)); err != nil {
		serverError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listAsset lists image name, uploaded with r, in the Assets space; it is
// best effort, as the image is stored.
func (s *store) listAsset(r *http.Request, data []byte, name string) {
	if _, err := s.addAsset(data, path.Ext(name), r.URL.Query().Get("name"), ""); err != nil {
		log.Printf("list image in assets: %v", err)
	}
}
