package api

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jmnser/notely/internal/note"
)

func pngData(t *testing.T) []byte {
	t.Helper()
	return pngOf(t, 0)
}

// pngOf returns a PNG image that differs for each shade.
func pngOf(t *testing.T, shade uint8) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 2, 2))
	img.SetGray(0, 0, color.Gray{Y: shade})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// pngName is the name pngData is stored under when uploaded without a file name.
func pngName(*testing.T) string { return "image.png" }

// upload posts data to path and returns the stored name.
func upload(t *testing.T, h http.Handler, path string, data []byte) string {
	t.Helper()
	w := request(t, h, http.MethodPost, path, string(data))
	var out struct{ Name string }
	if err := json.Unmarshal(w.Body.Bytes(), &out); w.Code != http.StatusOK || err != nil {
		t.Fatalf("upload %s: %d %s", path, w.Code, w.Body)
	}
	return out.Name
}

func listImages(t *testing.T, h http.Handler) []note.Image {
	t.Helper()
	var list []note.Image
	w := request(t, h, http.MethodGet, "/api/images", "")
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	return list
}

func TestImages(t *testing.T) {
	dir := t.TempDir()
	s, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	h := newHandler(only(s), newEvents())
	request(t, h, http.MethodPut, "/api/notes/a", `{"title":"a","space":"work"}`)
	img := pngData(t)

	// Named after the file; the same image is stored once per space.
	for range 2 {
		if name := upload(t, h, "/api/notes/a/images?name=My%20Photo%20(1).PNG", img); name != "my-photo-1.png" {
			t.Fatalf("name = %q", name)
		}
	}
	if name := upload(t, h, "/api/notes/a/images?name=my-photo-1.png", pngOf(t, 1)); name != "my-photo-1-2.png" {
		t.Errorf("collision: %q", name)
	}
	if name := upload(t, h, "/api/notes/a/images?name=%C3%BC.png", pngOf(t, 2)); name != "image.png" {
		t.Errorf("nothing left of the name: %q", name)
	}
	if _, err := os.Stat(filepath.Join(dir, "work", "my-photo-1.png")); err != nil {
		t.Errorf("not next to the note: %v", err)
	}
	list := listImages(t, h)
	if len(list) != 3 || list[0].Space != "work" || list[0].Version == "" {
		t.Fatalf("list = %+v", list)
	}

	w := request(t, h, http.MethodGet, "/api/images/work/my-photo-1.png", "")
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), img) || w.Header().Get("Content-Type") != "image/png" ||
		!strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") ||
		w.Header().Get("Cache-Control") != "private, no-cache" || w.Header().Get("ETag") == "" {
		t.Errorf("get: %d %v", w.Code, w.Header())
	}
	var v string
	for _, i := range list {
		if i.Name == "my-photo-1.png" {
			v = i.Version
		}
	}
	if w := request(t, h, http.MethodGet, "/api/images/work/my-photo-1.png?v="+v, ""); !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("versioned get: %v", w.Header())
	}
	if w := request(t, h, http.MethodGet, "/api/notes/a/images/my-photo-1.png", ""); w.Code != http.StatusOK {
		t.Errorf("get by note: %d", w.Code)
	}
	if w := request(t, h, http.MethodGet, "/api/notes/a/images", ""); !strings.Contains(w.Body.String(), `"my-photo-1.png"`) {
		t.Errorf("list by note: %d %s", w.Code, w.Body)
	}

	for _, c := range []struct {
		name, method, path, body string
		want                     int
	}{
		{"svg", http.MethodPost, "/api/notes/a/images", `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`, http.StatusBadRequest},
		{"text", http.MethodPost, "/api/notes/a/images", "hello", http.StatusBadRequest},
		{"too large", http.MethodPost, "/api/notes/a/images", string(img) + strings.Repeat("x", maxImage), http.StatusBadRequest},
		{"unknown note", http.MethodPost, "/api/notes/b/images", string(img), http.StatusNotFound},
		{"unknown note list", http.MethodGet, "/api/notes/b/images", "", http.StatusNotFound},
		{"missing", http.MethodGet, "/api/images/work/missing.png", "", http.StatusNotFound},
		{"bad name", http.MethodGet, "/api/images/..%2f.users.json", "", http.StatusNotFound},
		{"bad name by note", http.MethodGet, "/api/notes/a/images/..%2f.users.json", "", http.StatusNotFound},
		{"svg name", http.MethodGet, "/api/images/work/a.svg", "", http.StatusNotFound},
	} {
		if w := request(t, h, c.method, c.path, c.body); w.Code != c.want {
			t.Errorf("%s: %d, want %d: %s", c.name, w.Code, c.want, w.Body)
		}
	}
}

func TestMoveImages(t *testing.T) {
	dir := t.TempDir()
	s, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	h := newHandler(only(s), newEvents())
	img := pngData(t)
	request(t, h, http.MethodPut, "/api/notes/a", `{"title":"a","space":"work"}`)
	upload(t, h, "/api/notes/a/images?name=my-photo-1.png", img)
	upload(t, h, "/api/notes/a/images?name=image.png", pngOf(t, 1))

	// Uploads straight to a space, which then exists with images only.
	w := request(t, h, http.MethodPost, "/api/images?space=Art/&name=sky.webp", string(img))
	var up note.Image
	if err := json.Unmarshal(w.Body.Bytes(), &up); err != nil || up.Space != "Art" || up.Name != "sky.png" {
		t.Errorf("upload to a space: %d %s", w.Code, w.Body)
	}
	if w := request(t, h, http.MethodGet, "/api/images/Art/sky.png", ""); w.Code != http.StatusOK {
		t.Errorf("get from a space without notes: %d", w.Code)
	}
	// A subspace without notes keeps its path, which its directory name loses.
	request(t, h, http.MethodPost, "/api/images?space=Art/Old&name=old.png", string(pngOf(t, 3)))
	if !slices.ContainsFunc(listImages(t, h), func(i note.Image) bool { return i.Space == "Art/Old" && i.Name == "old.png" }) {
		t.Errorf("subspace lost: %+v", listImages(t, h))
	}
	request(t, h, http.MethodDelete, "/api/images/Art/Old/old.png", "")
	if _, err := os.Stat(filepath.Join(dir, "Art-Old")); err == nil {
		t.Error("emptied subspace directory left")
	}

	// Renaming and moving points the references of all notes to the image.
	request(t, h, http.MethodPut, "/api/notes/b", `{"title":"b","body":"![x](work/my-photo-1.png) ![y](Art/sky.png)"}`)
	request(t, h, http.MethodPut, "/api/notes/a", `{"title":"a","space":"work","body":"![x](my-photo-1.png)"}`)
	w = request(t, h, http.MethodPut, "/api/images/work/my-photo-1.png", `{"name":"beach","space":"Home Trips"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"name":"beach.png"`) {
		t.Fatalf("move: %d %s", w.Code, w.Body)
	}
	for _, c := range []struct {
		name, path, body string
		want             int
	}{
		{"bad label", "/api/images/work/beach.png", `{"name":"Bad Name","space":"work"}`, http.StatusBadRequest},
		{"taken", "/api/images/Home%20Trips/beach.png", `{"name":"image","space":"work"}`, http.StatusBadRequest},
		{"missing", "/api/images/work/missing.png", `{"name":"x","space":"work"}`, http.StatusNotFound},
	} {
		if w := request(t, h, http.MethodPut, c.path, c.body); w.Code != c.want {
			t.Errorf("%s: %d, want %d: %s", c.name, w.Code, c.want, w.Body)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "Home Trips", "beach.png")); err != nil {
		t.Errorf("not moved: %v", err)
	}
	for id, want := range map[string]string{"a": "![x](<Home Trips/beach.png>)", "b": "![x](<Home Trips/beach.png>) ![y](Art/sky.png)"} {
		if n, _ := s.get(id); n.Body != want {
			t.Errorf("%s: %q, want %q", id, n.Body, want)
		}
	}
	request(t, h, http.MethodPut, "/api/images/Art/sky.png", `{"name":"sky","space":""}`)
	if _, err := os.Stat(filepath.Join(dir, "Art")); err == nil {
		t.Error("emptied space directory left")
	}
	if n, _ := s.get("b"); !strings.HasSuffix(n.Body, "![y](sky.png)") {
		t.Errorf("b: %q", n.Body)
	}

	if w := request(t, h, http.MethodDelete, "/api/images/sky.png", ""); w.Code != http.StatusNoContent {
		t.Errorf("delete: %d", w.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, "sky.png")); err == nil {
		t.Error("not deleted")
	}
}

func TestOldImageRefs(t *testing.T) {
	dir := t.TempDir()
	s, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	h := newHandler(only(s), newEvents())
	img := pngData(t)
	// The former layout's references, /images/<hash>, find the image anywhere.
	const hash = "0123456789abcdef0123456789abcdef.png"
	writeNote(t, dir, "work/"+hash, string(img))
	if w := request(t, h, http.MethodGet, "/api/images/"+hash, ""); w.Code != http.StatusOK {
		t.Errorf("old get: %d %v", w.Code, w.Header())
	}
	// Uploads of app versions from before images moved next to notes.
	w := request(t, h, http.MethodPost, "/api/images", string(pngOf(t, 9)))
	var out struct{ Path string }
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || !strings.HasPrefix(out.Path, "/images/") {
		t.Fatalf("old upload: %d %s", w.Code, w.Body)
	}
	if w := request(t, h, http.MethodGet, "/api"+out.Path, ""); w.Code != http.StatusOK {
		t.Errorf("old upload get: %d", w.Code)
	}
}

func TestMoveNoteCarriesImages(t *testing.T) {
	dir := t.TempDir()
	s, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	put := func(id, space, body string) {
		t.Helper()
		if _, err := s.put(note.Note{ID: id, Title: id, Space: space, Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	put("a", "work", "![](own.png) ![](shared.png) ![](/top.png)")
	put("b", "work", "![](shared.png)")
	for i, f := range []string{"work/own.png", "work/shared.png", "work/unused.png", "top.png", "home/shared.png"} {
		writeNote(t, dir, f, string(pngOf(t, uint8(i))))
	}
	put("a", "home", "![](own.png) ![](shared.png) ![](/top.png)")
	put("b", "work/sub", "![](/work/shared.png)") // its images left in work: the space keeps its path
	writeNote(t, dir, "work-sub/x.png", string(pngData(t)))
	put("b", "work/sub", "![](/work/shared.png)")
	if err := s.delete("b"); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.get("a"); n.Body != "![](own.png) ![](shared-2.png) ![](/top.png)" {
		t.Errorf("a: %q", n.Body)
	}
	if list, _ := s.images(); !slices.ContainsFunc(list, func(i note.Image) bool { return i.Space == "work/sub" }) {
		t.Errorf("images without notes: %+v", list)
	}
	for f, want := range map[string]bool{
		"home/own.png": true, "work/own.png": false, // used by a only: moved
		"home/shared.png": true, "work/shared.png": true, "home/shared-2.png": true, // also used by b: copied, under another name
		"home/top.png": false, "top.png": true, "work/unused.png": true,
	} {
		if _, err := os.Stat(filepath.Join(dir, f)); (err == nil) != want {
			t.Errorf("%s exists: %v, want %v", f, err == nil, want)
		}
	}
}

func TestMigrateAssets(t *testing.T) {
	dir := t.TempDir()
	writeNote(t, dir, "work/0123456789abcdef0123456789abcdef.png", string(pngOf(t, 1)))
	writeNote(t, dir, ".assets/from-note.png", string(pngOf(t, 1)))
	writeNote(t, dir, ".assets/logo.png", string(pngOf(t, 2)))
	writeNote(t, dir, ".assets/icons/logo.png", string(pngOf(t, 3)))
	s, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.migrateAssets(); err != nil {
		t.Fatal(err)
	}
	for f, want := range map[string]bool{
		"Assets/logo.png": true, "Assets-icons/logo.png": true, "Assets/from-note.png": false, ".assets": false,
	} {
		if _, err := os.Stat(filepath.Join(dir, f)); (err == nil) != want {
			t.Errorf("%s exists: %v, want %v", f, err == nil, want)
		}
	}
	list, err := s.images()
	if err != nil || len(list) != 3 || !slices.ContainsFunc(list, func(i note.Image) bool { return i.Space == "Assets/icons" }) {
		t.Errorf("images = %+v, %v", list, err)
	}
}

func TestRenameSpaceMovesSharedImages(t *testing.T) {
	dir := t.TempDir()
	s, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"n1", "n2"} {
		if _, err := s.put(note.Note{ID: id, Title: id, Space: "Old", Body: "![](a.png)"}); err != nil {
			t.Fatal(err)
		}
	}
	writeNote(t, dir, "Old/a.png", string(pngData(t)))
	for _, id := range []string{"n1", "n2"} { // one at a time, as the app pushes them
		if _, err := s.put(note.Note{ID: id, Title: id, Space: "New", Body: "![](a.png)"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "Old")); err == nil {
		t.Error("the old space is left")
	}
	if list, _ := s.images(); len(list) != 1 || list[0].Space != "New" {
		t.Errorf("images = %+v", list)
	}
}
