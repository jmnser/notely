package note

import (
	"strings"
	"testing"
)

func TestParseImageRef(t *testing.T) {
	for _, c := range []struct {
		dest, space, wantSpace, wantName string
		ok                               bool
	}{
		{"diagram.png", "Work", "Work", "diagram.png", true},
		{"./diagram.png", "Work", "Work", "diagram.png", true},
		{"Home/Trips/map-2.jpg", "Work", "Home/Trips", "map-2.jpg", true},
		{"My%20Space/a.gif", "", "My Space", "a.gif", true},
		{"/top.webp", "Work", "", "top.webp", true},
		{"/images/0123456789abcdef0123456789abcdef.png", "Work", "", "0123456789abcdef0123456789abcdef.png", true},
		{"Notes/a.png", "Work", "", "a.png", true},
		{"Diagram.png", "Work", "", "", false},
		{"-a.png", "Work", "", "", false},
		{"a.svg", "Work", "", "", false},
		{"Flow.excalidraw", "Work", "", "", false},
		{"https://example.com/a.png", "", "", "", false},
		{"//example.com/a.png", "", "", "", false},
		{"mailto:a.png", "", "", "", false},
		{"/Work:2024/a.png", "", "Work:2024", "a.png", true},
	} {
		space, name, ok := ParseImageRef(c.dest, c.space)
		if space != c.wantSpace || name != c.wantName || ok != c.ok {
			t.Errorf("ParseImageRef(%q, %q) = %q, %q, %v", c.dest, c.space, space, name, ok)
		}
	}
}

func TestImageRef(t *testing.T) {
	for _, c := range []struct{ from, space, want string }{
		{"Work", "Work", "a.png"},
		{"Work", "", "/a.png"},
		{"", "Work/Projects", "Work/Projects/a.png"},
		{"", "My Space", "<My Space/a.png>"},
		{"", "Work: 2024", "</Work: 2024/a.png>"},
		{"", "a:b", "/a:b/a.png"},
	} {
		got := ImageRef(c.from, c.space, "a.png")
		if got != c.want {
			t.Errorf("ImageRef(%q, %q) = %q, want %q", c.from, c.space, got, c.want)
		}
		if s, n, ok := ParseImageRef(strings.Trim(got, "<>"), c.from); !ok || s != c.space || n != "a.png" {
			t.Errorf("%q resolves to %q, %q", got, s, n)
		}
	}
}

func TestRewriteImageRefs(t *testing.T) {
	body := "![a](a.png) ![b](</Work/a.png> \"t\") [link](a.png) ![c](other.png) ![d](https://x/a.png)"
	got, changed := RewriteImageRefs(body, "Work", Image{Space: "Work", Name: "a.png"}, Image{Space: "My Space", Name: "b.png"})
	want := "![a](<My Space/b.png>) ![b](<My Space/b.png> \"t\") [link](<My Space/b.png>) ![c](other.png) ![d](https://x/a.png)"
	if !changed || got != want {
		t.Errorf("got %q, %v\nwant %q", got, changed, want)
	}
	if refs := ImageRefs(body, "Work"); len(refs) != 4 || refs[3] != (Image{Space: "Work", Name: "other.png"}) {
		t.Errorf("refs %v", refs)
	}
	if _, changed := RewriteImageRefs(body, "Home", Image{Space: "Home", Name: "x.png"}, Image{Name: "y.png"}); changed {
		t.Error("changed without references")
	}
}

func TestImageRefsSkipCode(t *testing.T) {
	body := "![x][1]\n\n[1]: a.png\n\n`![](a.png)`\n\n```\n![](a.png)\n```\n\n    ![](a.png)\n\n![](a.png)\n"
	if refs := ImageRefs(body, "W"); len(refs) != 2 {
		t.Errorf("refs = %v", refs)
	}
	got, _ := RewriteImageRefs(body, "W", Image{Space: "W", Name: "a.png"}, Image{Space: "W", Name: "b.png"})
	want := strings.Replace(body[:strings.LastIndex(body, "a.png")]+"b.png)\n", "[1]: a.png", "[1]: b.png", 1)
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}
