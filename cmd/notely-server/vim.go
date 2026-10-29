package main

import (
	"math"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// vim is the editor's Vim mode. In Normal mode keys move the cursor and edit
// the text; i, a, o and similar switch to Insert mode, v and V to Visual mode,
// Escape back.
type vim struct {
	insert   bool     // Insert mode
	visual   string   // Visual mode: "v" for characters, "V" for lines, or ""
	anchor   int      // where the Visual selection started
	at       int      // Visual mode cursor; the editor selects the whole selection
	pending  []string // keys waiting for the rest of a command, such as 2d
	register string   // last yanked or deleted text
	linewise bool     // register holds whole lines, without the last line break
	goal     int      // rune column j and k keep, or -1; math.MaxInt after $
	goalAt   int      // cursor the goal is for, or -1
	last     dot      // last change, repeated by .
	change   []string // keys of the change whose Insert mode is running
	insAt    int      // where that Insert mode started
	insLen   int      // length of the text then
}

// dot is a change to repeat: its keys and the text typed in Insert mode.
type dot struct {
	keys []string
	text string
}

// vimEdit is the effect of a Normal mode key: replace s[from:to] with insert,
// or run the browser command cmd times times, and put the cursor at byte
// offset cursor.
type vimEdit struct {
	from, to int
	insert   string
	cmd      string // "undo" or "redo"
	times    int
	cursor   int
}

// maxCount limits counts, maxPaste the text a counted paste inserts.
const (
	maxCount = 9999
	maxPaste = 1 << 20
)

// vimAliases maps editing keys to their Normal mode commands.
var vimAliases = map[string]string{
	"Backspace": "<BS>", "Enter": "+", "ArrowLeft": "h", "ArrowDown": "j", "ArrowUp": "k", "ArrowRight": "l",
}

// motions move from byte offset pos of s.
var motions = map[string]func(s string, pos int) int{
	"h": func(s string, pos int) int {
		if pos > lineStart(s, pos) {
			return prevRune(s, pos)
		}
		return pos
	},
	"l": func(s string, pos int) int { return min(nextRune(s, pos), lineEnd(s, pos)) },
	"<BS>": func(s string, pos int) int {
		if pos > 0 {
			return prevRune(s, pos)
		}
		return pos
	},
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
	"+": func(s string, pos int) int {
		if e := lineEnd(s, pos); e < len(s) {
			return firstNonBlank(s, e+1)
		}
		return pos
	},
	"0":  lineStart,
	"^":  firstNonBlank,
	"$":  lineEnd,
	"w":  wordForward,
	"b":  wordBack,
	"e":  wordEnd,
	"gg": func(s string, _ int) int { return firstNonBlank(s, 0) },
	"G":  func(s string, _ int) int { return firstNonBlank(s, lineStart(s, len(s))) },
}

// key handles Normal or Visual mode key k, a character, "<C-r>", "<BS>" or
// "<Esc>", with the cursor at byte offset pos of s.
func (v *vim) key(s string, pos int, k string) vimEdit {
	visual := v.visual != ""
	if visual {
		pos = v.at
	}
	pos = clamp(s, min(pos, len(s)))
	if pos != v.goalAt {
		v.goal = -1
	}
	v.goalAt = -1 // set again by j, k and $
	keys := append(slices.Clip(v.pending), k)
	v.pending = nil
	e := v.do(s, pos, keys)
	n := s[:e.from] + e.insert + s[e.to:]
	changed := e.from != e.to || e.insert != "" || v.insert
	switch {
	case v.pending != nil || k == "." || !changed:
	case visual:
		v.last = dot{} // Visual changes don't repeat
	case v.insert:
		v.change, v.insAt, v.insLen = keys, e.cursor, len(n)
	default:
		v.last = dot{keys: keys}
	}
	if !v.insert && e.cmd == "" {
		e.cursor = clamp(n, e.cursor)
	}
	if v.visual != "" {
		v.at = e.cursor
	}
	return e
}

// do runs the command keys, or keeps them pending while incomplete.
func (v *vim) do(s string, pos int, keys []string) vimEdit {
	if keys[len(keys)-1] == "<Esc>" {
		v.visual = ""
		return vimEdit{cursor: pos}
	}
	n, rest := count(keys)
	op := ""
	if v.visual == "" && len(rest) > 0 && (rest[0] == "d" || rest[0] == "c" || rest[0] == "y") {
		op = rest[0]
		var n2 int
		if n2, rest = count(rest[1:]); n > 0 || n2 > 0 {
			n = min(max(n, 1)*max(n2, 1), maxCount)
		}
	}
	m := strings.Join(rest, "")
	if m == "" || m == "g" {
		v.pending = keys
		return vimEdit{cursor: pos}
	}
	if op != "" {
		return v.operate(s, pos, op, m, n)
	}
	if v.visual != "" {
		if e, ok := v.visualKey(s, pos, m); ok {
			return e
		}
	}
	if _, ok := motions[m]; !ok {
		if v.visual != "" {
			return vimEdit{cursor: pos}
		}
		return v.command(s, pos, m, n)
	}
	t := target(s, pos, m, n)
	switch {
	case m == "$":
		v.goal = math.MaxInt
	case m != "j" && m != "k":
		return vimEdit{cursor: t}
	case t == pos:
	default:
		if v.goal < 0 {
			v.goal = utf8.RuneCountInString(s[lineStart(s, pos):pos])
		}
		t = column(s, t, v.goal)
	}
	v.goalAt = clamp(s, t)
	return vimEdit{cursor: t}
}

// count splits a leading count off keys, 0 if there is none; a 0 without
// digits before it is a motion.
func count(keys []string) (int, []string) {
	n := 0
	for len(keys) > 0 && len(keys[0]) == 1 && keys[0][0] >= '0' && keys[0][0] <= '9' && (n > 0 || keys[0] != "0") {
		n = min(n*10+int(keys[0][0]-'0'), maxCount)
		keys = keys[1:]
	}
	return n, keys
}

// target is where motion m moves n times, at least once; with a count, G and
// gg go to line n.
func target(s string, pos int, m string, n int) int {
	if n > 0 && (m == "G" || m == "gg") {
		i := 0
		for ; n > 1; n-- {
			e := strings.IndexByte(s[i:], '\n')
			if e < 0 {
				break
			}
			i += e + 1
		}
		return firstNonBlank(s, i)
	}
	return repeat(motions[m], s, pos, max(n, 1))
}

// repeat applies motion f n times.
func repeat(f func(string, int) int, s string, pos, n int) int {
	for range n {
		pos = f(s, pos)
	}
	return pos
}

// reset switches to Normal mode and forgets pending keys and the goal column.
func (v *vim) reset() {
	v.insert, v.visual, v.pending, v.goalAt, v.change = false, "", nil, -1, nil
}

// escape switches to Normal mode with the cursor at pos, which steps back
// onto the last inserted character. A change typed in a row becomes the one
// to repeat.
func (v *vim) escape(s string, pos int) int {
	if v.change != nil {
		v.last = dot{}
		if pos >= v.insAt && len(s)-v.insLen == pos-v.insAt {
			v.last = dot{keys: v.change, text: s[v.insAt:pos]}
		}
	}
	v.reset()
	if pos > lineStart(s, pos) {
		return prevRune(s, pos)
	}
	return pos
}

// command handles the Normal mode command m that is no motion, n times
// where that makes sense.
func (v *vim) command(s string, pos int, m string, n int) vimEdit {
	end := lineEnd(s, pos)
	switch m {
	case "i":
		v.insert = true
		return vimEdit{cursor: pos}
	case "a":
		v.insert = true
		return vimEdit{cursor: min(nextRune(s, pos), end)}
	case "I":
		v.insert = true
		return vimEdit{cursor: firstNonBlank(s, pos)}
	case "A":
		v.insert = true
		return vimEdit{cursor: end}
	case "o":
		v.insert = true
		return vimEdit{from: end, to: end, insert: "\n", cursor: end + 1}
	case "O":
		v.insert = true
		st := lineStart(s, pos)
		return vimEdit{from: st, to: st, insert: "\n", cursor: st}
	case "v", "V":
		v.visual, v.anchor = m, pos
		return vimEdit{cursor: pos}
	case "x":
		return v.operate(s, pos, "d", "l", n)
	case "D":
		return v.operate(s, pos, "d", "$", 0)
	case "C":
		return v.operate(s, pos, "c", "$", 0)
	case "p", "P":
		return v.paste(s, pos, m == "p", max(n, 1))
	case "u":
		return vimEdit{cmd: "undo", times: max(n, 1), cursor: pos}
	case "<C-r>":
		return vimEdit{cmd: "redo", times: max(n, 1), cursor: pos}
	case ".":
		return v.replay(s, pos)
	}
	return vimEdit{cursor: pos}
}

// visualKey handles the Visual mode command m, if it is one.
func (v *vim) visualKey(s string, pos int, m string) (vimEdit, bool) {
	switch m {
	case "v", "V":
		if v.visual == m {
			m = ""
		}
		v.visual = m
	case "o":
		v.anchor, pos = pos, clamp(s, min(v.anchor, len(s)))
	case "d", "x", "c", "y":
		from, to := v.selection(s, pos)
		lines := v.visual == "V"
		v.visual = ""
		if m == "x" {
			m = "d"
		}
		if lines {
			return v.apply(s, m, from, lineEnd(s, max(from, to-1)), true, from), true
		}
		return v.apply(s, m, from, to, false, from), true
	default:
		return vimEdit{}, false
	}
	return vimEdit{cursor: pos}, true
}

// selection returns the range the editor selects with the cursor at c: the
// Visual selection, with the whole lines for V, a block cursor in Normal mode
// or nothing in Insert mode.
func (v *vim) selection(s string, c int) (from, to int) {
	c = min(c, len(s))
	switch {
	case v.insert:
		return c, c
	case v.visual == "":
		if c < lineEnd(s, c) {
			return c, nextRune(s, c)
		}
		return c, c
	}
	lo, hi := min(v.anchor, c, len(s)), min(max(v.anchor, c), len(s))
	if v.visual == "V" {
		return lineStart(s, lo), min(lineEnd(s, hi)+1, len(s))
	}
	return lo, nextRune(s, hi)
}

// operate applies operator op (d, c or y) to the text motion m moves over n
// times; a doubled operator or a line motion takes whole lines.
func (v *vim) operate(s string, pos int, op, m string, n int) vimEdit {
	switch m {
	case op, "j", "k", "+", "gg", "G":
		t := repeat(motions["j"], s, pos, max(n, 1)-1)
		if m != op {
			if t = target(s, pos, m, n); (m == "j" || m == "k" || m == "+") && t == pos {
				return vimEdit{cursor: pos} // no line to take along
			}
		}
		return v.apply(s, op, lineStart(s, min(pos, t)), lineEnd(s, max(pos, t)), true, pos)
	}
	if _, ok := motions[m]; !ok {
		return vimEdit{cursor: pos}
	}
	n = max(n, 1)
	var t int
	switch {
	case m == "w" && op == "c" && !unicode.IsSpace(runeAt(s, pos)):
		t, m = repeat(wordEnd, s, runEnd(s, pos), n-1), "e" // cw changes to the end of the word
	case m == "w": // the last word stays on its line; an empty one is a word
		p := repeat(wordForward, s, pos, n-1)
		t = wordForward(s, p)
		switch end := lineEnd(s, p); {
		case end > p:
			t = min(t, end)
		case op == "c":
			t = p
		default:
			t = min(t, p+1)
		}
	default:
		t = target(s, pos, m, n)
	}
	if m == "e" {
		t = nextRune(s, t)
	}
	return v.apply(s, op, min(pos, t), max(pos, t), false, min(pos, t))
}

// apply applies operator op to s[from:to], or to the lines in it for lines:
// d deletes, c changes in Insert mode and y yanks, moving the cursor to cursor.
func (v *vim) apply(s, op string, from, to int, lines bool, cursor int) vimEdit {
	if from < to || lines {
		v.register, v.linewise = s[from:to], lines
	}
	switch {
	case op == "y":
		return vimEdit{cursor: cursor}
	case op == "c":
		v.insert = true
		return vimEdit{from: from, to: to, cursor: from}
	case !lines:
		return vimEdit{from: from, to: to, cursor: from}
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

// paste puts the register n times after the cursor or its line, or before them.
func (v *vim) paste(s string, pos int, after bool, n int) vimEdit {
	r := v.register
	if r == "" && !v.linewise {
		return vimEdit{cursor: pos}
	}
	n = min(n, max(maxPaste/max(len(r), 1), 1))
	if v.linewise {
		r += strings.Repeat("\n"+r, n-1)
		at, ins, line := lineStart(s, pos), r+"\n", lineStart(s, pos)
		if after {
			at = lineEnd(s, pos)
			ins, line = "\n"+r, at+1
		}
		t := s[:at] + ins + s[at:]
		return vimEdit{from: at, to: at, insert: ins, cursor: firstNonBlank(t, line)}
	}
	r = strings.Repeat(r, n)
	at := pos
	if after {
		at = min(nextRune(s, pos), lineEnd(s, pos))
	}
	return vimEdit{from: at, to: at, insert: r, cursor: prevRune(s[:at]+r, at+len(r))}
}

// replay repeats the last change at pos as one edit.
func (v *vim) replay(s string, pos int) vimEdit {
	d := v.last
	if d.keys == nil {
		return vimEdit{cursor: pos}
	}
	e := v.do(s, pos, d.keys)
	n, c := s[:e.from]+e.insert+s[e.to:], e.cursor
	if v.insert {
		n = n[:c] + d.text + n[c:]
		c = v.escape(n, c+len(d.text))
	}
	v.pending = nil
	// The common prefix and suffix stay, cut at rune starts.
	p := 0
	for p < len(s) && p < len(n) && s[p] == n[p] {
		p++
	}
	for p > 0 && (p < len(s) && !utf8.RuneStart(s[p]) || p < len(n) && !utf8.RuneStart(n[p])) {
		p--
	}
	q := 0
	for q < len(s)-p && q < len(n)-p && s[len(s)-1-q] == n[len(n)-1-q] {
		q++
	}
	for q > 0 && !utf8.RuneStart(s[len(s)-q]) {
		q--
	}
	return vimEdit{from: p, to: len(s) - q, insert: n[p : len(n)-q], cursor: c}
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
	for i < len(s) && unicode.IsSpace(runeAt(s, i)) && (i == pos || !emptyLine(s, i)) {
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
	return runEnd(s, i)
}

// runEnd returns the last rune of the run of one word class at pos.
func runEnd(s string, pos int) int {
	c := wordClass(runeAt(s, pos))
	for n := nextRune(s, pos); n < len(s) && wordClass(runeAt(s, n)) == c; n = nextRune(s, n) {
		pos = n
	}
	return pos
}

// wordBack returns the start of the word before pos.
func wordBack(s string, pos int) int {
	i := pos
	for i > 0 && unicode.IsSpace(lastRune(s[:i])) {
		if i = prevRune(s, i); emptyLine(s, i) {
			return i
		}
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

// emptyLine reports whether the line at offset i of s is empty and i its start.
func emptyLine(s string, i int) bool {
	return s[i] == '\n' && (i == 0 || s[i-1] == '\n')
}

func lastRune(s string) rune {
	r, _ := utf8.DecodeLastRuneInString(s)
	return r
}
