package api

import (
	"bytes"
	"encoding/json"
	"image"
	"image/gif"
	"net/http"
	"strings"
	"testing"
)

func TestLabel(t *testing.T) {
	for in, want := range map[string]string{
		"Cat Photo.PNG":                "cat-photo",
		"--my__dog--.gif":              "my-dog",
		"Ünïcode.jpg":                  "n-code",
		"...":                          "image",
		"":                             "image",
		strings.Repeat("a", 70):        strings.Repeat("a", 63),
		strings.Repeat("a", 62) + "-b": strings.Repeat("a", 62),
	} {
		if got := label(in); got != want {
			t.Errorf("label(%q) = %q, want %q", in, got, want)
		}
		if got := label(in); !dnsLabel.MatchString(got) {
			t.Errorf("label(%q) = %q is not a DNS label", in, got)
		}
	}
}

func listAssets(t *testing.T, h http.Handler) []asset {
	t.Helper()
	w := request(t, h, http.MethodGet, "/api/assets", "")
	var list []asset
	if err := json.Unmarshal(w.Body.Bytes(), &list); w.Code != http.StatusOK || err != nil {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	return list
}

func names(list []asset) string {
	s := make([]string, 0, len(list))
	for _, a := range list {
		s = append(s, strings.TrimPrefix(a.Space+"/"+a.Name, "/"))
	}
	return strings.Join(s, " ")
}

func TestAssets(t *testing.T) {
	s, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := newHandler(only(s), newEvents())
	if got := listAssets(t, h); len(got) != 0 {
		t.Fatalf("empty: %v", got)
	}
	img := pngData(t)
	var buf bytes.Buffer
	if err := gif.Encode(&buf, image.NewGray(image.Rect(0, 0, 3, 3)), nil); err != nil {
		t.Fatal(err)
	}

	// Images uploaded to notes are listed too; the same image once.
	request(t, h, http.MethodPut, "/api/notes/a", `{"title":"a"}`)
	if w := request(t, h, http.MethodPost, "/api/notes/a/images?name=My%20Cat.png", string(img)); w.Code != http.StatusOK {
		t.Fatalf("note image: %d %s", w.Code, w.Body)
	}
	if w := request(t, h, http.MethodPost, "/api/assets?name=other.png", string(img)); w.Code != http.StatusOK {
		t.Fatalf("same image: %d %s", w.Code, w.Body)
	}
	if w := request(t, h, http.MethodPost, "/api/assets?name=My_Cat.gif&space=pets", buf.String()); w.Code != http.StatusOK {
		t.Fatalf("gif: %d %s", w.Code, w.Body)
	}
	if got := names(listAssets(t, h)); !strings.Contains(got, "my-cat.png") || !strings.Contains(got, "pets/my-cat.gif") ||
		len(strings.Fields(got)) != 2 {
		t.Fatalf("assets = %s", got)
	}

	w := request(t, h, http.MethodGet, "/api/assets/pets/my-cat.gif", "")
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/gif" || !bytes.Equal(w.Body.Bytes(), buf.Bytes()) {
		t.Errorf("get: %d %v", w.Code, w.Header())
	}

	for _, c := range []struct {
		name, method, path, body string
		want                     int
	}{
		{"taken", http.MethodPut, "/api/assets/pets/my-cat.gif", `{"name":"my-cat"}`, http.StatusBadRequest},
		{"upper case", http.MethodPut, "/api/assets/my-cat.png", `{"name":"Cat"}`, http.StatusBadRequest},
		{"too long", http.MethodPut, "/api/assets/my-cat.png", `{"name":"` + strings.Repeat("a", 64) + `"}`, http.StatusBadRequest},
		{"nested", http.MethodPut, "/api/assets/my-cat.png", `{"name":"x","space":"a/b"}`, http.StatusBadRequest},
		{"missing", http.MethodPut, "/api/assets/nope.png", `{"name":"x"}`, http.StatusNotFound},
		{"bad space", http.MethodPost, "/api/assets?space=A", string(img), http.StatusBadRequest},
		{"svg", http.MethodPost, "/api/assets", `<svg xmlns="http://www.w3.org/2000/svg"></svg>`, http.StatusBadRequest},
		{"traversal", http.MethodGet, "/api/assets/..%2f..%2f.users.json", "", http.StatusNotFound},
		{"nested get", http.MethodGet, "/api/assets/a/b/c.png", "", http.StatusNotFound},
	} {
		if w := request(t, h, c.method, c.path, c.body); w.Code != c.want {
			t.Errorf("%s: %d %s, want %d", c.name, w.Code, w.Body, c.want)
		}
	}

	// Moving the last asset out of a subspace removes it.
	if w := request(t, h, http.MethodPut, "/api/assets/pets/my-cat.gif", `{"name":"kitty","space":""}`); w.Code != http.StatusOK {
		t.Fatalf("move: %d %s", w.Code, w.Body)
	}
	if w := request(t, h, http.MethodPut, "/api/assets/my-cat.png", `{"name":"cat","space":"pets"}`); w.Code != http.StatusOK {
		t.Fatalf("move: %d %s", w.Code, w.Body)
	}
	if got := names(listAssets(t, h)); !strings.Contains(got, "pets/cat.png") || !strings.Contains(got, "kitty.gif") {
		t.Fatalf("after move: %s", got)
	}
	for _, p := range []string{"/api/assets/pets/cat.png", "/api/assets/kitty.gif"} {
		if w := request(t, h, http.MethodDelete, p, ""); w.Code != http.StatusNoContent {
			t.Fatalf("delete %s: %d", p, w.Code)
		}
	}
	if got := listAssets(t, h); len(got) != 0 {
		t.Errorf("after delete: %v", got)
	}
}
