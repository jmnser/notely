package note

import (
	"net/url"
	"regexp"
	"strings"
	"time"
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

// ParseImageRef resolves a link destination in a note of space to an image,
// as drawings resolve: diagram.png is the image in the same space,
// Work/diagram.png the one in Work and /diagram.png the one in the default
// space. ok is false for anything else, such as URLs and drawings.
func ParseImageRef(dest, space string) (string, string, bool) {
	if strings.Contains(dest, ":") || strings.HasPrefix(dest, "//") {
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
	switch space {
	case from:
	case "":
		ref = "/" + name
	default:
		ref = space + "/" + name
	}
	if strings.ContainsAny(ref, " \t()<>") {
		return "<" + ref + ">"
	}
	return ref
}

// linkDest finds the destinations of Markdown links and images, ](dest), in
// angle brackets or not.
var linkDest = regexp.MustCompile(`\]\([ \t]*(<[^<>\n]*>|[^\s()<>]+)`)

// ImageRefs returns the spaces and names of the images body, a note of
// space, references.
func ImageRefs(body, space string) []Image {
	var out []Image
	for _, m := range linkDest.FindAllStringSubmatch(body, -1) {
		if s, name, ok := ParseImageRef(strings.Trim(m[1], "<>"), space); ok {
			out = append(out, Image{Space: s, Name: name})
		}
	}
	return out
}

// RewriteImageRefs points body's references to image from, of a note of
// space, to image to, and reports whether it changed any.
func RewriteImageRefs(body, space string, from, to Image) (string, bool) {
	changed := false
	out := linkDest.ReplaceAllStringFunc(body, func(m string) string {
		sub := linkDest.FindStringSubmatch(m)
		s, name, ok := ParseImageRef(strings.Trim(sub[1], "<>"), space)
		if !ok || s != from.Space || name != from.Name {
			return m
		}
		changed = true
		return strings.TrimSuffix(m, sub[1]) + ImageRef(space, to.Space, to.Name)
	})
	return out, changed
}
