package main

import (
	"strings"
	"testing"
)

func TestVim(t *testing.T) {
	tests := []struct {
		in, keys, want string // | marks the cursor; keys start in Normal mode, \x1b is Escape
		insert         bool
	}{
		{"a|bc", "h", "|abc", false},
		{"|abc", "h", "|abc", false},
		{"a|bc", "l", "ab|c", false},
		{"ab|c", "l", "ab|c", false},
		{"é|😀x", "lh", "é|😀x", false},
		{"ab|cd\nx\nefgh", "jj", "abcd\nx\nef|gh", false},
		{"abcd\nx\nef|gh", "kk", "ab|cd\nx\nefgh", false},
		{"  a|b", "0", "|  ab", false},
		{"  a|b", "^", "  |ab", false},
		{"|ab\ncd", "$", "a|b\ncd", false},
		{"|ab\n\n  cd", "G", "ab\n\n  |cd", false},
		{"|foo.bar baz", "w", "foo|.bar baz", false},
		{"|foo bar\n  baz", "ww", "foo bar\n  |baz", false},
		{"|foo bar", "e", "fo|o bar", false},
		{"foo ba|r", "b", "foo |bar", false},
		{"foo |bar", "b", "|foo bar", false},
		{"a|bc", "x", "a|c", false},
		{"ab|c", "x", "a|b", false},
		{"|\nx", "x", "|\nx", false},
		{"ab|\nc", "x", "|a\nc", false},
		{"|foo bar", "dw", "|bar", false},
		{"foo |bar\nbaz", "dw", "foo| \nbaz", false},
		{"f|oo bar", "de", "f| bar", false},
		{"a\n|\n  b", "dw", "a\n|  b", false},
		{"a\n|\nb", "cw", "a\n|\nb", true},
		{"a\n|\n\nb", "dw", "a\n|\nb", false},
		{"|a\n\nb", "w", "a\n|\nb", false},
		{"|a\n\nb", "ww", "a\n\n|b", false},
		{"a\n\n|b", "b", "a\n|\nb", false},
		{"|abc\nde\nfghi", "$jj", "abc\nde\nfgh|i", false},
		{"|abc\nde\nfghi", "$jhj", "abc\nde\n|fghi", false},
		{"a|b\n  c", "\r", "ab\n  |c", false},
		{"ab\n|c", "\b", "a|b\nc", false},
		{"a\n|b\nc", "d+", "|a", false},
		{"fo|o bar", "de", "f|o", false},
		{"a|bc\nd\nef", "jjk", "abc\n|d\nef", false},
		{"a|bc\nd\nef", "jlj", "abc\nd\n|ef", false},
		{"foo |bar", "db", "|bar", false},
		{"a|bc", "D", "|a", false},
		{"a\n|b\nc", "dd", "a\n|c", false},
		{"a\n  b\n|c", "dd", "a\n  |b", false},
		{"|a", "dd", "|", false},
		{"a\n|b\nc", "dj", "|a", false},
		{"a\n|b\nc", "dk", "|c", false},
		{"a|b\nc", "dG", "|", false},
		{"|foo bar", "cw", "| bar", true},
		{"fo|o bar", "cw", "fo| bar", true},
		{"|a b", "cw", "| b", true},
		{"foo|.bar", "cw", "foo|bar", true},
		{"a\n|\nb", "yyp", "a\n\n|\nb", false},
		{"a\n|b", "dj", "a\n|b", false},
		{"|a\nb", "dk", "|a\nb", false},
		{"  ab\n|cd", "gg", "  |ab\ncd", false},
		{"a|bc", "C", "a|", true},
		{"  a|b\nc", "cc", "|\nc", true},
		{"a|b", "i", "a|b", true},
		{"a|b", "a", "ab|", true},
		{"  a|b", "I", "  |ab", true},
		{"a|b\nc", "A", "ab|\nc", true},
		{"a|b\nc", "o", "ab\n|\nc", true},
		{"a\nb|c", "O", "a\n|\nbc", true},
		{"|ab", "yyp", "ab\n|ab", false},
		{"a\n|b", "yykP", "|b\na\nb", false},
		{"|  a\nb", "ddp", "b\n  |a", false},
		{"|ab", "xp", "b|a", false},
		{"|ab", "ylP", "|aab", false},
		{"|ab", "ywP", "a|bab", false},
		{"|ab", "p", "|ab", false},
		{"|ab", "dx", "|ab", false},
		{"|ab", "gx", "|ab", false},
		{"|a b c d", "2w", "a b |c d", false},
		{"|abcd", "3x", "|d", false},
		{"|abcd", "10x", "|", false},
		{"|a\nb\nc\nd", "2dd", "|c\nd", false},
		{"|a\nb\nc\nd", "d2j", "|d", false},
		{"|a\nb\nc\nd", "3G", "a\nb\n|c\nd", false},
		{"a\nb\n|c", "1G", "|a\nb\nc", false},
		{"|a b c", "d2w", "|c", false},
		{"|a b c d", "2d2w", "|", false},
		{"|ab", "yl3p", "aaa|ab", false},
		{"|a\nb", "yy2p", "a\n|a\na\nb", false},
		{"|foo bar baz", "c2wx\x1b", "|x baz", false},
		{"|a b c", "dw.", "|c", false},
		{"|a b c d e", "2dw.", "|e", false},
		{"|foo foo", "cwbar\x1bw.", "bar ba|r", false},
		{"|a\nb", "Ax\x1bj.", "ax\nb|x", false},
		{"|ab", "ix\x1b.", "|xxab", false},
		{"|a\nb", "ox\x1b.", "a\nx\n|x\nb", false},
		{"|ab", "x.", "|", false},
		{"|a", ".", "|a", false},
		{"|abcd", "vld", "|cd", false},
		{"a|bcd", "vlly$p", "abcdbc|d", false},
		{"|abcd", "vllold", "a|d", false},
		{"a\n|b\nc", "Vjd", "|a", false},
		{"|a\nb\nc", "Vy2jp", "a\nb\nc\n|a", false},
		{"|abc", "vlc", "|c", true},
		{"|ab", "vVd", "|", false},
		{"|ab cd", "vd.", "|b cd", false},
		{"|ab", "vvx", "|b", false},
		{"|ab", "vl\x1bx", "|a", false},
	}
	for _, tt := range tests {
		pos := strings.Index(tt.in, "|")
		s := strings.Replace(tt.in, "|", "", 1)
		var v vim
		for _, k := range tt.keys {
			if v.insert { // type, as the browser does
				if k == '\x1b' {
					pos = v.escape(s, pos)
				} else {
					s, pos = s[:pos]+string(k)+s[pos:], pos+len(string(k))
				}
				continue
			}
			key := string(k)
			if k == '\x1b' {
				key = "<Esc>"
			}
			if a, ok := map[rune]string{'\r': "Enter", '\b': "Backspace"}[k]; ok {
				key = vimAliases[a]
			}
			e := v.key(s, pos, key)
			s, pos = s[:e.from]+e.insert+s[e.to:], e.cursor
		}
		if got := s[:pos] + "|" + s[pos:]; got != tt.want || v.insert != tt.insert {
			t.Errorf("%q %s: got %q, insert %v, want %q, %v", tt.in, tt.keys, got, v.insert, tt.want, tt.insert)
		}
	}
}

func TestVimEscape(t *testing.T) {
	v := vim{insert: true, pending: []string{"d"}}
	if got := v.escape("ab", 2); got != 1 || v.insert || v.pending != nil {
		t.Errorf("escape = %d, %+v", got, v)
	}
	if got := v.escape("a\nb", 2); got != 2 {
		t.Errorf("escape at line start = %d, want 2", got)
	}
}

func TestVimSelection(t *testing.T) {
	tests := []struct {
		v        vim
		s        string
		c        int
		from, to int
	}{
		{vim{}, "ab", 1, 1, 2},
		{vim{}, "a\nb", 1, 1, 1},
		{vim{insert: true}, "ab", 1, 1, 1},
		{vim{visual: "v", anchor: 3}, "ab\ncd", 1, 1, 4},
		{vim{visual: "V", anchor: 0}, "ab\ncd\nef", 3, 0, 6},
		{vim{visual: "V", anchor: 9}, "ab", 0, 0, 2},
	}
	for _, tt := range tests {
		if from, to := tt.v.selection(tt.s, tt.c); from != tt.from || to != tt.to {
			t.Errorf("%+v %q %d: got %d-%d, want %d-%d", tt.v, tt.s, tt.c, from, to, tt.from, tt.to)
		}
	}
}

func TestVimUndo(t *testing.T) {
	var v vim
	for k, want := range map[string]string{"u": "undo", "<C-r>": "redo"} {
		if e := v.key("ab", 1, k); e.cmd != want || e.times != 1 || e.cursor != 1 {
			t.Errorf("%s: got %+v", k, e)
		}
	}
}
