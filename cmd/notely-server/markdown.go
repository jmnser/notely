package main

import (
	"bytes"
	"slices"
	"strconv"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

// md renders GitHub Flavored Markdown. Raw HTML in notes is not rendered.
var md = goldmark.New(goldmark.WithExtensions(extension.GFM))

const (
	checkbox     = ` disabled="" type="checkbox"`
	imageSrc     = `<img src="/images/`
	imageHref    = `<a href="/images/`
	mermaidStart = `<pre><code class="language-mermaid">`
	mermaidEnd   = `</code></pre>`
)

// render converts markdown to HTML with a single root element. Task list
// checkboxes are enabled and numbered in data-task, in document order.
// Mermaid code blocks become <pre class="mermaid"> for Mermaid to draw.
// Uploaded images, /images/<name>, load from the API under prefix.
func render(src, prefix string) string {
	var buf bytes.Buffer
	if err := md.Convert([]byte(src), &buf); err != nil {
		return "<div></div>"
	}
	var out strings.Builder
	out.WriteString(`<div class="markdown">`)
	html := strings.NewReplacer(
		imageSrc, `<img loading="lazy" src="`+prefix+`/api/images/`,
		imageHref, `<a href="`+prefix+`/api/images/`,
	).Replace(buf.String())
	rest := mermaidBlocks(html)
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
// and plain items first, then checked ones, the item just checked on top.
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
// so numbering and spacing stay. Lists with an item whose text does not start
// on its marker's line stay put.
func group(src string, box *extast.TaskCheckBox) string {
	li := box.Parent().Parent()
	list, ok := li.Parent().(*ast.List)
	if _, isItem := li.(*ast.ListItem); !ok || !isItem {
		return src
	}
	var starts, marks []int
	col := 0
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
	i := 0
	for c := list.FirstChild(); c != nil; c, i = c.NextSibling(), i+1 {
		chunk := src[marks[i]:starts[i+1]]
		body := strings.TrimRightFunc(chunk, func(r rune) bool { return strings.ContainsRune(" \t>\n", r) })
		body = chunk[:len(body)+strings.IndexByte(chunk[len(body):], '\n')+1]
		items = append(items, item{marker: src[starts[i]:marks[i]], body: body, blank: chunk[len(body):]})
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

// markerEnd returns the length of the indent and list marker that line, up to
// an item's text, starts with, or -1 if it is not just that.
func markerEnd(line string, list *ast.List) int {
	rest := strings.TrimLeft(line, " \t>")
	if list.IsOrdered() {
		rest = strings.TrimLeft(rest, "0123456789")
	}
	if len(rest) < 2 || rest[0] != list.Marker || !strings.ContainsAny(rest[1:2], " \t") {
		return -1
	}
	return len(line) - len(rest) + 1
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

// mermaidBlocks marks fenced mermaid code blocks in html for Mermaid. Their
// content stays HTML-escaped; Mermaid reads it as text.
func mermaidBlocks(html string) string {
	var out strings.Builder
	for {
		before, after, ok := strings.Cut(html, mermaidStart)
		out.WriteString(before)
		if !ok {
			return out.String()
		}
		code, rest, _ := strings.Cut(after, mermaidEnd)
		out.WriteString(`<pre class="mermaid">` + code + `</pre>`)
		html = rest
	}
}
