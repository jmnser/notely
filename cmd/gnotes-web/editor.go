package main

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
)

// listItem matches the marker of a Markdown list item: indent, bullet or
// number, spacing and an optional task box.
var listItem = regexp.MustCompile(`^([ \t]*)([-*+]|[0-9]{1,9}[.)])([ \t]+)(\[[ xX]\](?:[ \t]+|$))?`)

// continueList handles Enter at byte offset pos of s. In a list item it
// returns the range [from, to) of s to replace with insert: the next item's
// marker, or nothing to remove the marker of an empty item and end the list.
func continueList(s string, pos int) (from, to int, insert string, ok bool) {
	start := strings.LastIndexByte(s[:pos], '\n') + 1
	end := len(s)
	if i := strings.IndexByte(s[pos:], '\n'); i >= 0 {
		end = pos + i
	}
	line := s[start:end]
	m := listItem.FindStringSubmatchIndex(line)
	if m == nil || start+m[1] > pos {
		return 0, 0, "", false
	}
	if strings.TrimSpace(line[m[1]:]) == "" {
		return start, end, "", true
	}
	indent, marker, space := line[m[2]:m[3]], line[m[4]:m[5]], line[m[6]:m[7]]
	if n, err := strconv.Atoi(marker[:len(marker)-1]); err == nil {
		marker = strconv.Itoa(n+1) + marker[len(marker)-1:]
	}
	task := ""
	if m[8] >= 0 {
		task = "[ ] "
	}
	return pos, pos, "\n" + indent + marker + space + task, true
}

// byteOffset converts the UTF-16 offset i of s, as used by the DOM, to a byte offset.
func byteOffset(s string, i int) int {
	n := 0
	for b, r := range s {
		if n >= i {
			return b
		}
		n += utf16.RuneLen(r)
	}
	return len(s)
}

// utf16Offset converts byte offset b of s to a UTF-16 offset.
func utf16Offset(s string, b int) int {
	n := 0
	for _, r := range s[:b] {
		n += utf16.RuneLen(r)
	}
	return n
}
