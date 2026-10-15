package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/maxence-charriere/go-app/v10/pkg/app"

	"github.com/jmnser/gnotes/internal/note"
)

var client = &http.Client{Timeout: 15 * time.Second}

// exchange pushes local changes to the backend and returns its notes.
func exchange(push []note.Note, deleted []string) ([]note.Note, error) {
	for _, n := range push {
		if err := call(http.MethodPut, "/api/notes/"+n.ID, n, nil); err != nil {
			return nil, err
		}
	}
	for _, id := range deleted {
		if err := call(http.MethodDelete, "/api/notes/"+id, nil, nil); err != nil {
			return nil, err
		}
	}
	var notes []note.Note
	return notes, call(http.MethodGet, "/api/notes", nil, &notes)
}

func call(method, path string, in, out any) error {
	var body bytes.Buffer
	if in != nil {
		if err := json.NewEncoder(&body).Encode(in); err != nil {
			return err
		}
	}
	u := *app.Window().URL()
	u.Path, u.RawQuery, u.Fragment = path, "", ""
	req, err := http.NewRequest(method, u.String(), &body)
	if err != nil {
		return err
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close() //nolint:errcheck
	if res.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("%s %s: %s", method, path, res.Status)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}
