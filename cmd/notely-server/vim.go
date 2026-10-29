package main

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// vim is the editor's Vim mode. In Normal mode keys move the cursor and edit
// the text; i, a, o and similar switch to Insert mode, Escape back.
type vim struct {
	insert   bool   // Insert mode
	pending  string // keys waiting for the rest of a command, such as d or g
	register string // last yanked or deleted text
	linewise bool   // register holds whole lines, without the last line break
	goal     int    // rune column j and k keep while the cursor stays at goalAt
	goalAt   int
}

// vimEdit is the effect of a Normal mode key: replace s[from:to] with insert,
// or run the browser command cmd, and put the cursor at byte offset cursor.
type vimEdit struct {
	from, to int
	insert   string
	cmd      string // "undo" or "redo"
	cursor   int
}

// vimAliases maps editing keys to their Normal mode commands.
var vimAliases = map[string]string{"Backspace": "h", "Enter": "j"}

// motions move from byte offset pos of s.
var motions = map[string]func(s string, pos int) int{
	"h": func(s string, pos int) int {
		if pos > lineStart(s, pos) {
			return prevRune(s, pos)
		}
		return pos
	},
	"l": func(s string, pos int) int { return min(nextRune(s, pos), lineEnd(s, pos)) },
	"j": func(s string, pos int) int { // to the next line's start, see vim.do
		if e := lineEnd(s, pos); e < len(s) {
			return e + 1
		}
		return pos
	},
	"k": func(s string, pos int) int {
		if st := lineStart(s, pos); st > 0 {
			return lineStart(s, st-1)
		}
		return pos
	},
	"0":  lineStart,
	"^":  firstNonBlank,
	"$":  lineEnd,
	"w":  wordForward,
	"b":  wordBack,
	"e":  wordEnd,
	"gg": func(string, int) int { return 0 },
	"G":  func(s string, _ int) int { return firstNonBlank(s, lineStart(s, len(s))) },
}

// key handles Normal mode key k, a character or "<C-r>", with the cursor at
// byte offset pos of s.
func (v *vim) key(s string, pos int, k string) vimEdit {
	e := v.do(s, clamp(s, pos), k)
	if !v.insert && e.cmd == "" {
		e.cursor = clamp(s[:e.from]+e.insert+s[e.to:], e.cursor)
	}
	if k != "j" && k != "k" {
		v.goalAt = -1
	}
	return e
}

func (v *vim) do(s string, pos int, k string) vimEdit {
	keys := v.pending + k
	v.pending = ""
	op, m := "", keys
	if strings.IndexByte("dcy", keys[0]) >= 0 {
		op, m = keys[:1], keys[1:]
	}
	if m == "" || m == "g" {
		v.pending = keys
		return vimEdit{cursor: pos}
	}
	if op != "" {
		return v.operate(s, pos, op, m)
	}
	f, ok := motions[m]
	if !ok {
		return v.command(s, pos, m)
	}
	t := f(s, pos)
	if (m == "j" || m == "k") && t != pos {
		if pos != v.goalAt {
			v.goal = utf8.RuneCountInString(s[lineStart(s, pos):pos])
		}
		t = column(s, t, v.goal)
		v.goalAt = clamp(s, t)
	}
	return vimEdit{cursor: t}
}

// escape switches to Normal mode with the cursor at pos, which steps back
// onto the last inserted character.
func (v *vim) escape(s string, pos int) int {
	v.insert, v.pending = false, ""
	if pos > lineStart(s, pos) {
		return prevRune(s, pos)
	}
	return pos
}

// command handles the Normal mode command m that is no motion.
func (v *vim) command(s string, pos int, m string) vimEdit {
	end := lineEnd(s, pos)
	switch m {
	case "i", "a", "I", "A":
		v.insert = true
		return vimEdit{cursor: map[string]int{
			"i": pos, "a": min(nextRune(s, pos), end), "I": firstNonBlank(s, pos), "A": end,
		}[m]}
	case "o":
		v.insert = true
		return vimEdit{from: end, to: end, insert: "\n", cursor: end + 1}
	case "O":
		v.insert = true
		st := lineStart(s, pos)
		return vimEdit{from: st, to: st, insert: "\n", cursor: st}
	case "x":
		return v.operate(s, pos, "d", "l")
	case "D":
		return v.operate(s, pos, "d", "$")
	case "C":
		return v.operate(s, pos, "c", "$")
	case "p", "P":
		return v.paste(s, pos, m == "p")
	case "u":
		return vimEdit{cmd: "undo", cursor: pos}
	case "<C-r>":
		return vimEdit{cmd: "redo", cursor: pos}
	}
	return vimEdit{cursor: pos}
}

// operate applies operator op (d, c or y) to the text motion m moves over;
// a doubled operator or a line motion takes whole lines.
func (v *vim) operate(s string, pos int, op, m string) vimEdit {
	var from, to int
	switch m {
	case op, "j", "k", "gg", "G":
		t := pos
		if m != op {
			t = motions[m](s, pos)
		}
		from, to = lineStart(s, min(pos, t)), lineEnd(s, max(pos, t))
	default:
		f, ok := motions[m]
		if !ok {
			return vimEdit{cursor: pos}
		}
		if m == "w" && op == "c" && !unicode.IsSpace(runeAt(s, pos)) {
			f, m = wordEnd, "e" // cw changes to the end of the word
		}
		t := f(s, pos)
		switch m {
		case "w": // stay on the line
			if end := lineEnd(s, pos); end > pos {
				t = min(t, end)
			}
		case "e":
			t = nextRune(s, t)
		}
		from, to = min(pos, t), max(pos, t)
		if from < to {
			v.register, v.linewise = s[from:to], false
		}
		return v.change(op, from, to, from)
	}
	v.register, v.linewise = s[from:to], true
	if op != "d" {
		return v.change(op, from, to, pos)
	}
	// Delete the lines with a line break, the one before at the end of s.
	switch {
	case to < len(s):
		to++
	case from > 0:
		from--
	}
	n := s[:from] + s[to:]
	return vimEdit{from: from, to: to, cursor: firstNonBlank(n, lineStart(n, from))}
}

// change deletes [from, to) for op d and c, where c switches to Insert mode.
// y only moves the cursor.
func (v *vim) change(op string, from, to, cursor int) vimEdit {
	switch op {
	case "c":
		v.insert = true
	case "y":
		return vimEdit{cursor: cursor}
	}
	return vimEdit{from: from, to: to, cursor: from}
}

// paste puts the register after the cursor or its line, or before them.
func (v *vim) paste(s string, pos int, after bool) vimEdit {
	r := v.register
	switch {
	case r == "":
		return vimEdit{cursor: pos}
	case v.linewise && after:
		end := lineEnd(s, pos)
		n := s[:end] + "\n" + r + s[end:]
		return vimEdit{from: end, to: end, insert: "\n" + r, cursor: firstNonBlank(n, end+1)}
	case v.linewise:
		st := lineStart(s, pos)
		n := s[:st] + r + "\n" + s[st:]
		return vimEdit{from: st, to: st, insert: r + "\n", cursor: firstNonBlank(n, st)}
	}
	at := pos
	if after {
		at = min(nextRune(s, pos), lineEnd(s, pos))
	}
	return vimEdit{from: at, to: at, insert: r, cursor: prevRune(s[:at]+r, at+len(r))}
}

// lineEnd returns the offset of the line break ending the line at pos, or len(s).
func lineEnd(s string, pos int) int {
	if i := strings.IndexByte(s[pos:], '\n'); i >= 0 {
		return pos + i
	}
	return len(s)
}

// firstNonBlank returns the offset of the first non-blank of the line at pos.
func firstNonBlank(s string, pos int) int {
	end := lineEnd(s, pos)
	return end - len(strings.TrimLeft(s[lineStart(s, pos):end], " \t"))
}

// column returns the offset of rune col of the line starting at st, or its end.
func column(s string, st, col int) int {
	end := lineEnd(s, st)
	for i := range s[st:end] {
		if col == 0 {
			return st + i
		}
		col--
	}
	return end
}

// clamp keeps a Normal mode cursor off the line break of a non-empty line.
func clamp(s string, pos int) int {
	if pos >= lineEnd(s, pos) && pos > lineStart(s, pos) {
		return prevRune(s, pos)
	}
	return pos
}

func nextRune(s string, pos int) int {
	_, n := utf8.DecodeRuneInString(s[pos:])
	return pos + n
}

func prevRune(s string, pos int) int {
	_, n := utf8.DecodeLastRuneInString(s[:pos])
	return pos - n
}

func runeAt(s string, pos int) rune {
	r, _ := utf8.DecodeRuneInString(s[pos:])
	return r
}

// wordClass groups runes into blanks, punctuation and word characters.
func wordClass(r rune) int {
	switch {
	case unicode.IsSpace(r):
		return 0
	case r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
		return 2
	}
	return 1
}

// wordForward returns the start of the next word after pos.
func wordForward(s string, pos int) int {
	i := pos
	if c := wordClass(runeAt(s, i)); c != 0 {
		for i < len(s) && wordClass(runeAt(s, i)) == c {
			i = nextRune(s, i)
		}
	}
	for i < len(s) && unicode.IsSpace(runeAt(s, i)) {
		i = nextRune(s, i)
	}
	return i
}

// wordEnd returns the end of the word after pos, or pos at the end of s.
func wordEnd(s string, pos int) int {
	i := nextRune(s, pos)
	for i < len(s) && unicode.IsSpace(runeAt(s, i)) {
		i = nextRune(s, i)
	}
	if i >= len(s) {
		return pos
	}
	c := wordClass(runeAt(s, i))
	for n := nextRune(s, i); n < len(s) && wordClass(runeAt(s, n)) == c; n = nextRune(s, n) {
		i = n
	}
	return i
}

// wordBack returns the start of the word before pos.
func wordBack(s string, pos int) int {
	i := pos
	for i > 0 && unicode.IsSpace(lastRune(s[:i])) {
		i = prevRune(s, i)
	}
	if i == 0 {
		return 0
	}
	c := wordClass(lastRune(s[:i]))
	for i > 0 && wordClass(lastRune(s[:i])) == c {
		i = prevRune(s, i)
	}
	return i
}

func lastRune(s string) rune {
	r, _ := utf8.DecodeLastRuneInString(s)
	return r
}
