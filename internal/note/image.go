package note

import (
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
)

// Image is an image file in a space, stored next to its notes. Its name is a
// DNS label, as Kubernetes names, and an extension.
type Image struct {
	Space    string    `json:"space,omitempty"`
	Name     string    `json:"name"`
	Uploaded time.Time `json:"uploaded"`
	Version  string    `json:"version"` // changes with the content
}

// Path is the image's space path and name, as a reference from the default
// space writes it.
func (i Image) Path() string {
	if i.Space == "" {
		return i.Name
	}
	return i.Space + "/" + i.Name
}

// Stem is the image's name without its extension.
func (i Image) Stem() string {
	stem, _, _ := strings.Cut(i.Name, ".") // labels hold no dots
	return stem
}

// ImageName matches an image's name: a DNS label of up to 63 lower case
// letters, digits and dashes, starting and ending with a letter or digit,
// and the extension of a PNG, JPEG, GIF or WebP image.
var ImageName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?\.(png|jpg|gif|webp)$`)

// legacyRef is a reference of the layout before images moved next to notes,
// /images/<hash>, which may point into any space.
var legacyRef = regexp.MustCompile(`^/images/([0-9a-f]{32}\.(?:png|jpg|gif|webp))$`)

// scheme starts a URL such as https://example.com/a.png.
var scheme = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)

// ParseImageRef resolves a link destination in a note of space to an image,
// as drawings resolve: diagram.png is the image in the same space,
// Work/diagram.png the one in Work and /diagram.png the one in the default
// space. ok is false for anything else, such as URLs and drawings.
func ParseImageRef(dest, space string) (string, string, bool) {
	if strings.HasPrefix(dest, "//") || !strings.HasPrefix(dest, "/") && scheme.MatchString(dest) {
		return "", "", false
	}
	if p, err := url.PathUnescape(dest); err == nil {
		dest = p
	}
	if m := legacyRef.FindStringSubmatch(dest); m != nil {
		return "", m[1], true
	}
	dest = strings.TrimPrefix(dest, "./")
	i := strings.LastIndexByte(dest, '/')
	name := dest[i+1:]
	if !ImageName.MatchString(name) {
		return "", "", false
	}
	if i >= 0 {
		space = SpacePath(dest[:i])
	}
	return space, name, true
}

// ImageRef is the link destination a note of space from uses for image name
// in space, the shortest that ParseImageRef resolves.
func ImageRef(from, space, name string) string {
	ref := name
	switch {
	case space == from:
	case space == "":
		ref = "/" + name
	case strings.Contains(space, ":"): // with a leading slash, not taken for a URL
		ref = "/" + space + "/" + name
	default:
		ref = space + "/" + name
	}
	if strings.ContainsAny(ref, " \t()<>") {
		return "<" + ref + ">"
	}
	return ref
}

var (
	// linkDest finds the destinations of inline Markdown links and images,
	// ](dest), in angle brackets or not.
	linkDest = regexp.MustCompile(`\]\([ \t]*(<[^<>\n]*>|[^\s()<>]+)`)
	// refDest finds the destinations of link reference definitions, [x]: dest.
	refDest = regexp.MustCompile(`(?m)^ {0,3}\[[^\]\n]+\]:[ \t]*(<[^<>\n]*>|\S+)`)
	parser  = goldmark.New(goldmark.WithExtensions(extension.GFM)).Parser()
)

// dests returns the offsets of the link destinations in body, outside code.
func dests(body string) [][2]int {
	var code [][2]int
	doc := parser.Parse(text.NewReader([]byte(body)))
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n.Kind() {
		case ast.KindFencedCodeBlock, ast.KindCodeBlock, ast.KindHTMLBlock:
			lines := n.Lines()
			for i := range lines.Len() {
				code = append(code, [2]int{lines.At(i).Start, lines.At(i).Stop})
			}
			if f, ok := n.(*ast.FencedCodeBlock); ok && f.Info != nil {
				code = append(code, [2]int{f.Info.Segment.Start, f.Info.Segment.Stop})
			}
		case ast.KindCodeSpan, ast.KindRawHTML:
			for c := n.FirstChild(); c != nil; c = c.NextSibling() {
				if t, ok := c.(*ast.Text); ok {
					code = append(code, [2]int{t.Segment.Start, t.Segment.Stop})
				}
			}
			if r, ok := n.(*ast.RawHTML); ok {
				for i := range r.Segments.Len() {
					code = append(code, [2]int{r.Segments.At(i).Start, r.Segments.At(i).Stop})
				}
			}
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	inCode := func(o int) bool {
		for _, c := range code {
			if o >= c[0] && o < c[1] {
				return true
			}
		}
		return false
	}
	var out [][2]int
	for _, re := range []*regexp.Regexp{linkDest, refDest} {
		for _, m := range re.FindAllStringSubmatchIndex(body, -1) {
			if !inCode(m[2]) {
				out = append(out, [2]int{m[2], m[3]})
			}
		}
	}
	slices.SortFunc(out, func(a, b [2]int) int { return a[0] - b[0] })
	return out
}

// ImageRefs returns the spaces and names of the images body, a note of
// space, references.
func ImageRefs(body, space string) []Image {
	var out []Image
	for _, d := range dests(body) {
		if s, name, ok := ParseImageRef(strings.Trim(body[d[0]:d[1]], "<>"), space); ok {
			out = append(out, Image{Space: s, Name: name})
		}
	}
	return out
}

// RewriteImageRefs points body's references to image from, of a note of
// space, to image to, and reports whether it changed any.
func RewriteImageRefs(body, space string, from, to Image) (string, bool) {
	var b strings.Builder
	last := 0
	for _, d := range dests(body) {
		s, name, ok := ParseImageRef(strings.Trim(body[d[0]:d[1]], "<>"), space)
		if !ok || s != from.Space || name != from.Name {
			continue
		}
		b.WriteString(body[last:d[0]])
		b.WriteString(ImageRef(space, to.Space, to.Name))
		last = d[1]
	}
	if last == 0 {
		return body, false
	}
	b.WriteString(body[last:])
	return b.String(), true
}
