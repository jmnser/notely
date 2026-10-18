package api

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func pngData(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestImages(t *testing.T) {
	dir := t.TempDir()
	h := (&images{dir: filepath.Join(dir, ".images")}).handler()
	img := pngData(t)

	paths := make([]string, 0, 2)
	for range 2 { // the same image is stored once, under the same name
		w := request(t, h, http.MethodPost, "/api/images", string(img))
		var out struct{ Path string }
		if err := json.Unmarshal(w.Body.Bytes(), &out); w.Code != http.StatusOK || err != nil {
			t.Fatalf("upload: %d %s", w.Code, w.Body)
		}
		paths = append(paths, out.Path)
	}
	if paths[0] != paths[1] || !strings.HasPrefix(paths[0], "/images/") || !strings.HasSuffix(paths[0], ".png") {
		t.Fatalf("paths = %v", paths)
	}
	if m, _ := filepath.Glob(filepath.Join(dir, ".images", "*")); len(m) != 1 {
		t.Errorf("stored files = %v", m)
	}

	w := request(t, h, http.MethodGet, "/api"+paths[0], "")
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), img) || w.Header().Get("Content-Type") != "image/png" ||
		!strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("get: %d %v", w.Code, w.Header())
	}
	for _, c := range []struct {
		name, method, path, body string
		want                     int
	}{
		{"svg", http.MethodPost, "/api/images", `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`, http.StatusBadRequest},
		{"text", http.MethodPost, "/api/images", "hello", http.StatusBadRequest},
		{"too large", http.MethodPost, "/api/images", string(img) + strings.Repeat("x", maxImage), http.StatusBadRequest},
		{"missing", http.MethodGet, "/api/images/00000000000000000000000000000000.png", "", http.StatusNotFound},
		{"bad name", http.MethodGet, "/api/images/..%2f.users.json", "", http.StatusNotFound},
		{"svg name", http.MethodGet, "/api/images/00000000000000000000000000000000.svg", "", http.StatusNotFound},
	} {
		if w := request(t, h, c.method, c.path, c.body); w.Code != c.want {
			t.Errorf("%s: %d, want %d", c.name, w.Code, c.want)
		}
	}
}
