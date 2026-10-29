// Package notefile is the file format of notes: Markdown with YAML front
// matter, drawings as Excalidraw files, named after their titles.
package notefile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jmnser/notely/internal/note"
)

// Extensions of note files.
const (
	MarkdownExt = ".md"
	DrawingExt  = ".excalidraw"
)

// validID matches note IDs, as the server accepts them.
var validID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Numbered reports whether path is base+ext or base-N+ext.
func Numbered(path, base, ext string) bool {
	rest, ok := strings.CutPrefix(path, base)
	if !ok {
		return false
	}
	if rest, ok = strings.CutSuffix(rest, ext); !ok {
		return false
	}
	if rest == "" {
		return true
	}
	num, ok := strings.CutPrefix(rest, "-")
	i, err := strconv.Atoi(num)
	return ok && err == nil && i > 1 && strconv.Itoa(i) == num
}

const maxName = 100 // bytes

// Name turns s into a readable file name that is safe on all platforms,
// or returns fallback if nothing is left.
func Name(s, fallback string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`/\:*?"<>|`, r) {
			return '-'
		}
		return r
	}, s)
	if len(s) > maxName {
		s = strings.ToValidUTF8(s[:maxName], "")
	}
	if s = strings.Trim(s, " ."); s == "" {
		return fallback
	}
	if stem, ext, ok := strings.Cut(s, "."); reserved[strings.ToUpper(strings.TrimRight(stem, " "))] {
		s = stem + "-"
		if ok {
			s += "." + ext
		}
	}
	return s
}

// reserved lists the device names Windows does not allow as file names, with
// or without an extension.
var reserved = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// Encode renders n as the content of its file.
func Encode(n note.Note) ([]byte, error) {
	if n.Kind == note.Drawing {
		return encodeDrawing(n)
	}
	return EncodeMarkdown(n), nil
}

// Decode parses a file written by Encode; ext is the file's extension.
func Decode(data []byte, ext string) (note.Note, error) {
	var n note.Note
	var err error
	if ext == DrawingExt {
		n, err = decodeDrawing(data)
	} else {
		n, err = decodeMarkdown(data)
	}
	if err == nil && !validID.MatchString(n.ID) {
		err = fmt.Errorf("invalid id %q", n.ID)
	}
	return n, err
}

// EncodeMarkdown renders n as Markdown with its fields in YAML front matter.
func EncodeMarkdown(n note.Note) []byte {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "id: %s\n", n.ID)
	fmt.Fprintf(&b, "title: %s\n", strconv.Quote(n.Title))
	if n.Space != "" {
		fmt.Fprintf(&b, "space: %s\n", strconv.Quote(n.Space))
	}
	if n.Pinned {
		b.WriteString("pinned: true\n")
	}
	fmt.Fprintf(&b, "updated: %s\n", n.Updated.Format(time.RFC3339Nano))
	b.WriteString("---\n")
	b.WriteString(n.Body)
	return []byte(b.String())
}

// decodeMarkdown parses a file written by EncodeMarkdown. Unknown keys are
// ignored.
func decodeMarkdown(data []byte) (note.Note, error) {
	var n note.Note
	first, rest, _ := strings.Cut(string(data), "\n")
	if strings.TrimSuffix(first, "\r") != "---" {
		return n, errors.New("no front matter")
	}
	for {
		line, after, found := strings.Cut(rest, "\n")
		if line = strings.TrimSuffix(line, "\r"); line == "---" {
			rest = after
			break
		}
		if !found {
			return n, errors.New("unterminated front matter")
		}
		rest = after
		key, value, _ := strings.Cut(line, ":")
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "id":
			n.ID = value
		case "title":
			n.Title = unquote(value)
		case "space":
			n.Space = unquote(value)
		case "pinned":
			n.Pinned = value == "true"
		case "updated":
			t, err := time.Parse(time.RFC3339Nano, value)
			if err != nil {
				return n, fmt.Errorf("updated: %w", err)
			}
			n.Updated = t
		}
	}
	n.Body = rest
	return n, nil
}

// unquote reads a double-quoted or plain YAML scalar.
func unquote(s string) string {
	if u, err := strconv.Unquote(s); err == nil {
		return u
	}
	return s
}

// Ext returns the file extension of notes of kind.
func Ext(kind string) string {
	if kind == note.Drawing {
		return DrawingExt
	}
	return MarkdownExt
}

// IsNote reports whether path names a note's file.
func IsNote(path string) bool {
	ext := filepath.Ext(path)
	return ext == MarkdownExt || ext == DrawingExt
}

// drawingMeta holds a drawing's fields in its file, under the key "notely".
// Excalidraw ignores the key, so the file still opens there.
type drawingMeta struct {
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Space   string    `json:"space,omitempty"`
	Pinned  bool      `json:"pinned,omitempty"`
	Updated time.Time `json:"updated"`
}

// encodeDrawing renders n as an Excalidraw file, indented for readable diffs.
// n.Body is empty or a JSON object.
func encodeDrawing(n note.Note) ([]byte, error) {
	scene := map[string]json.RawMessage{}
	if n.Body != "" {
		if err := json.Unmarshal([]byte(n.Body), &scene); err != nil {
			return nil, err
		}
	} else {
		scene["type"], scene["version"] = json.RawMessage(`"excalidraw"`), json.RawMessage(`2`)
		scene["elements"] = json.RawMessage(`[]`)
	}
	meta, err := json.Marshal(drawingMeta{ID: n.ID, Title: n.Title, Space: n.Space, Pinned: n.Pinned, Updated: n.Updated})
	if err != nil {
		return nil, err
	}
	scene["notely"] = meta
	return marshal(scene, "  ")
}

// decodeDrawing parses a file written by encodeDrawing.
func decodeDrawing(data []byte) (note.Note, error) {
	var scene map[string]json.RawMessage
	if err := json.Unmarshal(data, &scene); err != nil {
		return note.Note{}, err
	}
	var meta drawingMeta
	if raw, ok := scene["notely"]; !ok {
		return note.Note{}, errors.New("no notely fields")
	} else if err := json.Unmarshal(raw, &meta); err != nil {
		return note.Note{}, err
	}
	delete(scene, "notely")
	body, err := marshal(scene, "")
	if err != nil {
		return note.Note{}, err
	}
	return note.Note{
		ID: meta.ID, Kind: note.Drawing, Space: meta.Space, Title: meta.Title,
		Body: string(bytes.TrimSuffix(body, []byte("\n"))), Pinned: meta.Pinned, Updated: meta.Updated,
	}, nil
}

// marshal encodes v as JSON without escaping HTML characters.
func marshal(v any, indent string) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", indent)
	err := enc.Encode(v)
	return buf.Bytes(), err
}
