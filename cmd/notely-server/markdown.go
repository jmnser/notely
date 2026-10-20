package main

import (
	"bytes"
	"regexp"
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

// imageLink finds links to a note's images: by name, relative to the note, or
// as /images/<name>, as notes referenced them before images moved next to them.
var imageLink = regexp.MustCompile(`<(img src|a href)="(/images/)?([0-9a-f]{32}\.(?:png|jpg|gif|webp))"`)

const (
	checkbox     = ` disabled="" type="checkbox"`
	mermaidStart = `<pre><code class="language-mermaid">`
	mermaidEnd   = `</code></pre>`
)

// render converts markdown to HTML with a single root element. Task list
// checkboxes are enabled and numbered in data-task, in document order.
// Mermaid code blocks become <pre class="mermaid"> for Mermaid to draw.
// Images of note id load from the API under prefix.
func render(src, prefix, id string) string {
	var buf bytes.Buffer
	if err := md.Convert([]byte(src), &buf); err != nil {
		return "<div></div>"
	}
	var out strings.Builder
	out.WriteString(`<div class="markdown">`)
	html := imageLink.ReplaceAllStringFunc(buf.String(), func(link string) string {
		m := imageLink.FindStringSubmatch(link)
		url := imageBase(prefix, id) + m[3]
		if m[2] != "" {
			url = prefix + "/api/images/" + m[3] // found in any of the user's directories
		}
		if m[1] == "img src" {
			return `<img loading="lazy" title="` + m[3] + `" src="` + url + `"`
		}
		return `<a href="` + url + `"`
	})
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

// imageBase is the URL path the images next to note id are served under,
// below prefix.
func imageBase(prefix, id string) string {
	return prefix + "/api/notes/" + id + "/images/"
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
