package main

import (
	"bytes"
	"html"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"

	"github.com/jmnser/notely/internal/note"
)

// md renders GitHub Flavored Markdown. Raw HTML in notes is not rendered.
// Images of drawings become embeds, see drawings.
var md = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithParserOptions(parser.WithASTTransformers(util.Prioritized(drawings{}, 1000))),
	goldmark.WithRendererOptions(renderer.WithNodeRenderers(util.Prioritized(drawings{}, 500))),
)

const (
	checkbox     = ` disabled="" type="checkbox"`
	mermaidStart = `<pre><code class="language-mermaid">`
	mermaidEnd   = `</code></pre>`
)

// render converts markdown to HTML with a single root element. Task list
// checkboxes are enabled and numbered in data-task, in document order.
// Mermaid code blocks become <pre class="mermaid"> for Mermaid to draw.
// Images, referenced from a note of space, load from the API under prefix;
// versions maps their paths to their versions, so the browser caches them.
func render(src, prefix, space string, versions map[string]string) string {
	doc := md.Parser().Parse(text.NewReader([]byte(src)))
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch l := n.(type) {
		case *ast.Image:
			if img, ok := imageOf(string(l.Destination), space); ok {
				l.Destination = []byte(imageURL(prefix, img, versions[img.Path()]))
				l.SetAttributeString("loading", "lazy")
				if len(l.Title) == 0 {
					l.Title = []byte(img.Name)
				}
			}
		case *ast.Link:
			if img, ok := imageOf(string(l.Destination), space); ok {
				l.Destination = []byte(imageURL(prefix, img, versions[img.Path()]))
			}
		}
		return ast.WalkContinue, nil
	})
	var buf bytes.Buffer
	if err := md.Renderer().Render(&buf, []byte(src), doc); err != nil {
		return "<div></div>"
	}
	var out strings.Builder
	out.WriteString(`<div class="markdown">`)
	page := buf.String()
	rest := mermaidBlocks(page)
	for i := 0; ; i++ {
		before, after, ok := strings.Cut(rest, checkbox)
		out.WriteString(before)
		if !ok {
			break
		}
		out.WriteString(` type="checkbox" data-task="` + strconv.Itoa(i) + `"`)
		rest = after
	}
	out.WriteString("</div>")
	return out.String()
}

// tasks returns the task list checkboxes of doc in document order.
func tasks(doc ast.Node) []*extast.TaskCheckBox {
	var boxes []*extast.TaskCheckBox
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if box, ok := n.(*extast.TaskCheckBox); ok && entering && n.Parent().Lines().Len() > 0 {
			boxes = append(boxes, box)
		}
		return ast.WalkContinue, nil
	})
	return boxes
}

// toggle checks or unchecks task i of src and groups its list: unchecked
// and plain items first, then checked ones. A just checked item leads the
// checked ones, a just unchecked one ends the unchecked ones.
func toggle(src string, i int) (string, bool) {
	boxes := tasks(md.Parser().Parse(text.NewReader([]byte(src))))
	if i < 0 || i >= len(boxes) {
		return src, false
	}
	o := boxes[i].Parent().Lines().At(0).Start
	if o+2 >= len(src) || src[o] != '[' || src[o+2] != ']' {
		return src, false
	}
	mark := "x"
	if src[o+1] != ' ' {
		mark = " "
	}
	return group(src[:o+1]+mark+src[o+2:], boxes[i]), true
}

// item is a list item's source: its marker, the rest, and blank lines after.
type item struct {
	marker, body, blank string
}

// group reorders the list of the just toggled box in src: unchecked and plain
// items, then the toggled item, then the other checked items, each group in
// its order. The toggled item thus ends the unchecked or leads the checked
// ones. Each position keeps its marker and blank lines,
// so numbering and spacing stay. Lists stay put if an item's text does not
// start on its marker's line, or if items with several lines have markers of
// different widths.
func group(src string, box *extast.TaskCheckBox) string {
	li := box.Parent().Parent()
	list, ok := li.Parent().(*ast.List)
	if _, isItem := li.(*ast.ListItem); !ok || !isItem {
		return src
	}
	var starts, marks []int
	col, cols := 0, map[int]bool{}
	for c := list.FirstChild(); c != nil; c = c.NextSibling() {
		first := c.FirstChild()
		if first == nil || first.Lines().Len() == 0 ||
			first.Kind() != ast.KindTextBlock && first.Kind() != ast.KindParagraph {
			return src
		}
		o := first.Lines().At(0).Start
		start := lineStart(src, o)
		m := markerEnd(src[start:o], list)
		if m < 0 {
			return src // the text does not start on the marker's line
		}
		starts, marks = append(starts, start), append(marks, start+m)
		col = o - start
		cols[col] = true
	}
	end := itemEnd(src, list.LastChild(), starts[len(starts)-1], col)
	added := end == len(src) && !strings.HasSuffix(src, "\n")
	if added {
		src += "\n"
		end++
	}
	starts = append(starts, end)

	var items []item
	var unchecked, checked []string
	var toggled string
	quotes := strings.Count(src[starts[0]:marks[0]], ">")
	i := 0
	for c := list.FirstChild(); c != nil; c, i = c.NextSibling(), i+1 {
		lines := strings.SplitAfter(src[marks[i]:starts[i+1]], "\n")
		n := len(lines)
		for n > 1 && strings.Count(lines[n-1], ">") <= quotes && strings.Trim(lines[n-1], " \t>\r\n") == "" {
			n--
		}
		if n > 1 && len(cols) > 1 {
			return src // its nested lines would need another indent elsewhere
		}
		body := strings.Join(lines[:n], "")
		items = append(items, item{marker: src[starts[i]:marks[i]], body: body, blank: strings.Join(lines[n:], "")})
		b, isTask := c.FirstChild().FirstChild().(*extast.TaskCheckBox)
		switch {
		case b == box:
			toggled = body
		case isTask && b.IsChecked:
			checked = append(checked, body)
		default:
			unchecked = append(unchecked, body)
		}
	}
	bodies := slices.Concat(unchecked, []string{toggled}, checked)
	var out strings.Builder
	out.WriteString(src[:starts[0]])
	for i, it := range items {
		out.WriteString(it.marker + bodies[i] + it.blank)
	}
	out.WriteString(src[end:])
	if added {
		return strings.TrimSuffix(out.String(), "\n")
	}
	return out.String()
}

// markerEnd returns the length of line, the text before an item's content,
// up to and including the item's list marker, or -1 if it does not end in one.
func markerEnd(line string, list *ast.List) int {
	rest := strings.TrimRight(line, " \t")
	if rest == line || !strings.HasSuffix(rest, string(list.Marker)) {
		return -1
	}
	if list.IsOrdered() {
		num := strings.TrimRight(rest[:len(rest)-1], "0123456789")
		if len(num) == len(rest)-1 {
			return -1
		}
	}
	return len(rest)
}

// lineStart returns the offset of the line containing offset o of src.
func lineStart(src string, o int) int {
	return strings.LastIndexByte(src[:o], '\n') + 1
}

// itemEnd returns the offset after the last line of list item li, which
// starts at offset start of src with content at column col: its last source
// line and any following lines indented to col, such as a closing code fence.
func itemEnd(src string, li ast.Node, start, col int) int {
	end := start
	_ = ast.Walk(li, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering && n.Type() == ast.TypeBlock && n.Lines().Len() > 0 {
			end = max(end, n.Lines().At(n.Lines().Len()-1).Stop)
		}
		return ast.WalkContinue, nil
	})
	if src[end-1] != '\n' {
		i := strings.IndexByte(src[end:], '\n')
		if i < 0 {
			return len(src)
		}
		end += i + 1
	}
	for end < len(src) {
		line, _, _ := strings.Cut(src[end:], "\n")
		if strings.TrimSpace(line) == "" || indentOf(line) < col {
			return end
		}
		end += len(line) + 1
	}
	return len(src)
}

// indentOf returns the length of the leading spaces, tabs and blockquote
// markers of s.
func indentOf(s string) int {
	return len(s) - len(strings.TrimLeft(s, " \t>"))
}

// imageOf resolves a link destination of a note in space to an image.
func imageOf(dest, space string) (note.Image, bool) {
	s, name, ok := note.ParseImageRef(dest, space)
	return note.Image{Space: s, Name: name}, ok
}

// imageURL is the URL of img below prefix, cached by the browser if version
// is not empty.
func imageURL(prefix string, img note.Image, version string) string {
	var p strings.Builder
	p.WriteString(prefix + "/api/images/")
	if img.Space != "" {
		for seg := range strings.SplitSeq(img.Space, "/") {
			p.WriteString(url.PathEscape(seg) + "/")
		}
	}
	p.WriteString(img.Name)
	if version != "" {
		p.WriteString("?v=" + url.QueryEscape(version))
	}
	return p.String()
}

// mermaidBlocks marks fenced mermaid code blocks in page for Mermaid. Their
// content stays HTML-escaped; Mermaid reads it as text.
func mermaidBlocks(page string) string {
	var out strings.Builder
	for {
		before, after, ok := strings.Cut(page, mermaidStart)
		out.WriteString(before)
		if !ok {
			return out.String()
		}
		code, rest, _ := strings.Cut(after, mermaidEnd)
		out.WriteString(`<pre class="mermaid">` + code + `</pre>`)
		page = rest
	}
}

// drawingExt ends references to drawings, as their files.
const drawingExt = ".excalidraw"

var kindDrawing = ast.NewNodeKind("Drawing")

// drawingNode embeds the drawing at ref, a link destination.
type drawingNode struct {
	ast.BaseInline
	ref string
}

func (n *drawingNode) Kind() ast.NodeKind { return kindDrawing }

func (n *drawingNode) Dump(src []byte, level int) {
	ast.DumpHelper(n, src, level, map[string]string{"Ref": n.ref}, nil)
}

// drawings turns images of drawings, ![](Flow.excalidraw), into buttons that
// the app fills with the drawing; data-drawing holds the reference.
type drawings struct{}

func (drawings) Transform(doc *ast.Document, _ text.Reader, _ parser.Context) {
	var images []*ast.Image
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if img, ok := n.(*ast.Image); ok && entering && isDrawingRef(string(img.Destination)) {
			images = append(images, img)
		}
		return ast.WalkContinue, nil
	})
	for _, img := range images {
		img.Parent().ReplaceChild(img.Parent(), img, &drawingNode{ref: string(img.Destination)})
	}
}

func (drawings) RegisterFuncs(r renderer.NodeRendererFuncRegisterer) {
	r.Register(kindDrawing, func(w util.BufWriter, _ []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			ref := n.(*drawingNode).ref
			_, title := drawingPath(ref, "")
			_, _ = w.WriteString(`<button type="button" class="drawing" data-drawing="` + html.EscapeString(ref) + `">` +
				html.EscapeString(title) + `</button>`)
		}
		return ast.WalkSkipChildren, nil
	})
}

// isDrawingRef reports whether a link destination names a drawing in the app,
// not on the web.
func isDrawingRef(dest string) bool {
	return strings.HasSuffix(strings.ToLower(dest), drawingExt) && !strings.Contains(dest, ":")
}

// drawingPath resolves a drawing reference made in a note in space: Flow is
// the drawing Flow in the same space, Work/Flow the one in Work and /Flow the
// one in the default space, each ending in .excalidraw.
func drawingPath(ref, space string) (string, string) {
	if p, err := url.PathUnescape(ref); err == nil {
		ref = p
	}
	ref = ref[:len(ref)-min(len(ref), len(drawingExt))]
	if strings.Contains(ref, "/") {
		space, title, _ := note.ParsePath(ref)
		return space, title
	}
	return space, strings.TrimSpace(ref)
}

// findDrawing returns the drawing titled title in space, compared
// case-insensitively, or nil.
func findDrawing(notes []note.Note, space, title string) *note.Note {
	for i, n := range notes {
		if n.Kind == note.Drawing && n.Space == space && strings.EqualFold(strings.TrimSpace(n.Title), title) {
			return &notes[i]
		}
	}
	return nil
}
