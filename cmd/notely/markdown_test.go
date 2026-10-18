package main

import (
	"strings"
	"testing"
)

const list = "# Todo\n\n- [ ] one\n- [x] two\n\n```\n- [ ] in code\n```\n\n1. [X] three\n   - [ ] nested\n"

func TestRender(t *testing.T) {
	got := render(list, "")
	for _, want := range []string{
		`<div class="markdown">`,
		`<input type="checkbox" data-task="0">`,
		`<input checked="" type="checkbox" data-task="1">`,
		`<input checked="" type="checkbox" data-task="2">`,
		`<input type="checkbox" data-task="3">`,
		"- [ ] in code",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("render() lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "disabled") || strings.Contains(got, `data-task="4"`) {
		t.Errorf("render() = %s", got)
	}
	if got := render(`<script>x</script>`, ""); strings.Contains(got, "<script>") {
		t.Errorf("render() keeps raw HTML: %s", got)
	}
}

func TestToggle(t *testing.T) {
	tests := []struct {
		i    int
		want string
		ok   bool
	}{
		{0, strings.Replace(list, "[ ] one", "[x] one", 1), true},
		{1, strings.Replace(list, "[x] two", "[ ] two", 1), true},
		{2, strings.Replace(list, "[X] three", "[ ] three", 1), true},
		{3, strings.Replace(list, "[ ] nested", "[x] nested", 1), true},
		{4, list, false},
		{-1, list, false},
	}
	for _, tt := range tests {
		if got, ok := toggle(list, tt.i); got != tt.want || ok != tt.ok {
			t.Errorf("toggle(%d) = %q, %v, want %q, %v", tt.i, got, ok, tt.want, tt.ok)
		}
	}
}

func TestRenderMermaid(t *testing.T) {
	got := render("```mermaid\ngraph TD\n  A-->B[\"<b>x</b>\"]\n```\n\n```go\nx\n```\n", "")
	want := `<pre class="mermaid">graph TD
  A--&gt;B[&quot;&lt;b&gt;x&lt;/b&gt;&quot;]
</pre>`
	if !strings.Contains(got, want) {
		t.Errorf("render() = %s, want %s", got, want)
	}
	if !strings.Contains(got, `<pre><code class="language-go">`) {
		t.Errorf("render() changed other code blocks: %s", got)
	}
}

func TestRenderImages(t *testing.T) {
	got := render("![cat](/images/0123456789abcdef0123456789abcdef.png)\n\n![web](https://example.com/a.png)\n\n"+
		"[full](/images/0123456789abcdef0123456789abcdef.png)", "/notely")
	for _, want := range []string{
		`<a href="/notely/api/images/0123456789abcdef0123456789abcdef.png">full</a>`,
		`<img loading="lazy" src="/notely/api/images/0123456789abcdef0123456789abcdef.png" alt="cat">`,
		`<img src="https://example.com/a.png" alt="web">`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("render() lacks %q:\n%s", want, got)
		}
	}
}

func TestImageMarkdown(t *testing.T) {
	for file, want := range map[string]string{
		"cat.png":      "![cat](/images/x.png)\n",
		"a [b]\\c.jpg": "![a bc](/images/x.png)\n",
	} {
		if got := imageMarkdown(file, "/images/x.png"); got != want {
			t.Errorf("imageMarkdown(%q) = %q, want %q", file, got, want)
		}
	}
}
