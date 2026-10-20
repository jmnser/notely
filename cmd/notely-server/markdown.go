package main

import (
	"bytes"
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

// toggle checks or unchecks task i of src. In a tight bullet list, the item then
// moves below the list's other unchecked items, above its checked ones.
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
	return sortItem(src[:o+1]+mark+src[o+2:], boxes[i].Parent().Parent()), true
}

// sortItem moves list item li of src after the last of its siblings that is
// not a checked task. Ordered and loose lists, and lists with items it cannot
// delimit, stay put.
func sortItem(src string, li ast.Node) string {
	list, ok := li.Parent().(*ast.List)
	if _, isItem := li.(*ast.ListItem); !ok || !isItem || list.IsOrdered() || !list.IsTight {
		return src
	}
	var starts []int
	from, after, col := 0, 0, 0
	for c := list.FirstChild(); c != nil; c = c.NextSibling() {
		first := c.FirstChild()
		if first == nil || first.Lines().Len() == 0 ||
			first.Kind() != ast.KindTextBlock && first.Kind() != ast.KindParagraph {
			return src
		}
		o := first.Lines().At(0).Start
		start := lineStart(src, o)
		marker := strings.TrimLeft(src[start:o], " \t>")
		if len(marker) < 2 || marker[0] != list.Marker || !strings.ContainsAny(marker[1:2], " \t") {
			return src // the content does not start on the marker's line
		}
		if c == li {
			from = len(starts)
		} else if box, ok := first.FirstChild().(*extast.TaskCheckBox); !ok || !box.IsChecked {
			after = len(starts) + 1
		}
		starts = append(starts, start)
		col = o - start
	}
	end := itemEnd(src, list.LastChild(), starts[len(starts)-1], col)
	added := end == len(src) && !strings.HasSuffix(src, "\n")
	if added {
		src += "\n"
		end++
	}
	starts = append(starts, end)
	var out string
	switch {
	case after < from:
		out = src[:starts[after]] + src[starts[from]:starts[from+1]] + src[starts[after]:starts[from]] + src[starts[from+1]:]
	case after > from+1:
		out = src[:starts[from]] + src[starts[from+1]:starts[after]] + src[starts[from]:starts[from+1]] + src[starts[after]:]
	default:
		out = src
	}
	if added {
		out = strings.TrimSuffix(out, "\n")
	}
	return out
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
