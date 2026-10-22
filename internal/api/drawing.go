package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"github.com/jmnser/notely/internal/note"
)

const drawingExt = ".excalidraw"

// extension returns the file extension of notes of kind.
func extension(kind string) string {
	if kind == note.Drawing {
		return drawingExt
	}
	return noteExt
}

// noteFile reports whether path names a note's file.
func noteFile(path string) bool {
	ext := filepath.Ext(path)
	return ext == noteExt || ext == drawingExt
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
