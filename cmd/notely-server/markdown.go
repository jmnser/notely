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

// toggle checks or unchecks task i of src. In a bullet list, the item then
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
// not a checked task. Ordered lists and items it cannot delimit stay put.
func sortItem(src string, li ast.Node) string {
	list, ok := li.Parent().(*ast.List)
	if _, isItem := li.(*ast.ListItem); !ok || !isItem || list.IsOrdered() {
		return src
	}
	var starts []int
	from, after := 0, 0
	for c := list.FirstChild(); c != nil; c = c.NextSibling() {
		first := c.FirstChild()
		if first == nil || first.Lines().Len() == 0 ||
			first.Kind() != ast.KindTextBlock && first.Kind() != ast.KindParagraph {
			return src
		}
		start := lineStart(src, first.Lines().At(0).Start)
		if c == li {
			from = len(starts)
		} else if box, ok := first.FirstChild().(*extast.TaskCheckBox); !ok || !box.IsChecked {
			after = len(starts) + 1
		}
		starts = append(starts, start)
	}
	end := itemEnd(src, list.LastChild(), starts[len(starts)-1])
	added := end == len(src) && !strings.HasSuffix(src, "\n")
	if added {
		src += "\n"
		end++
	}
	starts = append(starts, end)
	var out string
	switch to := after; {
	case to < from:
		out = src[:starts[to]] + src[starts[from]:starts[from+1]] + src[starts[to]:starts[from]] + src[starts[from+1]:]
	case to > from+1:
		out = src[:starts[from]] + src[starts[from+1]:starts[to]] + src[starts[from]:starts[from+1]] + src[starts[to]:]
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
// starts at offset start of src: its last source line and any following lines
// indented past its marker, such as a closing code fence.
func itemEnd(src string, li ast.Node, start int) int {
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
	indent := indentOf(src[start:])
	for end < len(src) {
		line, _, _ := strings.Cut(src[end:], "\n")
		if strings.TrimSpace(line) == "" || indentOf(line) <= indent {
			return end
		}
		end += len(line) + 1
	}
	return len(src)
}

// indentOf returns the length of the leading spaces and tabs of s.
func indentOf(s string) int {
	return len(s) - len(strings.TrimLeft(s, " \t"))
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
