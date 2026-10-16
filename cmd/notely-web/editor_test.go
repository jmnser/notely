package main

import (
	"strings"
	"testing"
)

func TestContinueList(t *testing.T) {
	tests := []struct {
		name, in, want string // | marks the cursor
		ok             bool
	}{
		{"dash", "- one|", "- one\n- |", true},
		{"star", "* one|", "* one\n* |", true},
		{"plus", "+ one|", "+ one\n+ |", true},
		{"task", "- [x] done|", "- [x] done\n- [ ] |", true},
		{"open task", "- [ ] todo|", "- [ ] todo\n- [ ] |", true},
		{"indented", "- a\n  - b|\n- c", "- a\n  - b\n  - |\n- c", true},
		{"numbered", "9. nine|", "9. nine\n10. |", true},
		{"paren", "1) one|", "1) one\n2) |", true},
		{"split", "- ab|cd", "- ab\n- |cd", true},
		{"end list", "- one\n- |", "- one\n|", true},
		{"end task list", "- [ ] one\n- [ ] |", "- [ ] one\n|", true},
		{"end task without space", "- [ ] one\n- [ ]|", "- [ ] one\n|", true},
		{"end numbered", "1. one\n2. |\nnext", "1. one\n|\nnext", true},
		{"text", "plain|", "plain|", false},
		{"no space", "-one|", "-one|", false},
		{"cursor in marker", "-| one", "-| one", false},
		{"empty", "|", "|", false},
	}
	for _, tt := range tests {
		pos := strings.Index(tt.in, "|")
		s := strings.Replace(tt.in, "|", "", 1)
		from, to, insert, ok := continueList(s, pos)
		got := tt.in
		if ok {
			got = s[:from] + insert + "|" + s[to:]
		}
		if got != tt.want || ok != tt.ok {
			t.Errorf("%s: got %q, %v, want %q, %v", tt.name, got, ok, tt.want, tt.ok)
		}
	}
}

func TestOffsets(t *testing.T) {
	s := "aé😀b" // 1, 2 and 4 bytes; 1, 1 and 2 UTF-16 units
	for _, c := range []struct{ u16, b int }{{0, 0}, {1, 1}, {2, 3}, {4, 7}, {5, 8}} {
		if got := byteOffset(s, c.u16); got != c.b {
			t.Errorf("byteOffset(%d) = %d, want %d", c.u16, got, c.b)
		}
		if got := utf16Offset(s, c.b); got != c.u16 {
			t.Errorf("utf16Offset(%d) = %d, want %d", c.b, got, c.u16)
		}
	}
}
