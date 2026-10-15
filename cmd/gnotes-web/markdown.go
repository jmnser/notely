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
	mermaidStart = `<pre><code class="language-mermaid">`
	mermaidEnd   = `</code></pre>`
)

// render converts markdown to HTML with a single root element. Task list
// checkboxes are enabled and numbered in data-task, in document order.
// Mermaid code blocks become <pre class="mermaid"> for Mermaid to draw.
func render(src string) string {
	var buf bytes.Buffer
	if err := md.Convert([]byte(src), &buf); err != nil {
		return "<div></div>"
	}
	var out strings.Builder
	out.WriteString(`<div class="markdown">`)
	rest := mermaidBlocks(buf.String())
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

// tasks returns the offsets of the task list markers ("[ ]" or "[x]") in src,
// in document order.
func tasks(src string) []int {
	var offsets []int
	doc := md.Parser().Parse(text.NewReader([]byte(src)))
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if _, ok := n.(*extast.TaskCheckBox); ok && entering {
			if lines := n.Parent().Lines(); lines.Len() > 0 {
				offsets = append(offsets, lines.At(0).Start)
			}
		}
		return ast.WalkContinue, nil
	})
	return offsets
}

// toggle checks or unchecks task i of src.
func toggle(src string, i int) (string, bool) {
	offsets := tasks(src)
	if i < 0 || i >= len(offsets) {
		return src, false
	}
	o := offsets[i]
	if o+2 >= len(src) || src[o] != '[' || src[o+2] != ']' {
		return src, false
	}
	mark := "x"
	if src[o+1] != ' ' {
		mark = " "
	}
	return src[:o+1] + mark + src[o+2:], true
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
