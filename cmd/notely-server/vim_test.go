package main

import (
	"strings"
	"testing"
)

func TestVim(t *testing.T) {
	tests := []struct {
		in, keys, want string // | marks the cursor; keys are typed in Normal mode
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
	}
	for _, tt := range tests {
		pos := strings.Index(tt.in, "|")
		s := strings.Replace(tt.in, "|", "", 1)
		var v vim
		for _, k := range tt.keys {
			e := v.key(s, pos, string(k))
			s, pos = s[:e.from]+e.insert+s[e.to:], e.cursor
		}
		if got := s[:pos] + "|" + s[pos:]; got != tt.want || v.insert != tt.insert {
			t.Errorf("%q %s: got %q, insert %v, want %q, %v", tt.in, tt.keys, got, v.insert, tt.want, tt.insert)
		}
	}
}

func TestVimEscape(t *testing.T) {
	v := vim{insert: true, pending: "d"}
	if got := v.escape("ab", 2); got != 1 || v.insert || v.pending != "" {
		t.Errorf("escape = %d, %+v", got, v)
	}
	if got := v.escape("a\nb", 2); got != 2 {
		t.Errorf("escape at line start = %d, want 2", got)
	}
}

func TestVimUndo(t *testing.T) {
	var v vim
	for k, want := range map[string]string{"u": "undo", "<C-r>": "redo"} {
		if e := v.key("ab", 1, k); e.cmd != want || e.cursor != 1 {
			t.Errorf("%s: got %+v", k, e)
		}
	}
}
