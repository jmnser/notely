package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/maxence-charriere/go-app/v10/pkg/app"

	"github.com/jmnser/gnotes/internal/note"
)

var client = &http.Client{Timeout: 15 * time.Second}

// errSignedOut is returned when the backend needs a sign-on.
var errSignedOut = errors.New("signed out")

// badRequest is the backend's explanation of a rejected request.
type badRequest string

func (e badRequest) Error() string { return string(e) }

// syncInfo is the signed-on user's git backup configuration and last result.
type syncInfo struct {
	Remote string `json:"remote"`
	Branch string `json:"branch"`
	Token  bool   `json:"token"` // a token is stored
	Last   *struct {
		Time   time.Time `json:"time"`
		Commit string    `json:"commit"`
		Error  string    `json:"error"`
	} `json:"last"`
}

// signIn starts a session; the backend sets its cookie.
func signIn(user, password string) error {
	return call(http.MethodPost, "/api/login", map[string]string{"user": user, "password": password}, nil)
}

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
	if res.StatusCode == http.StatusUnauthorized {
		return errSignedOut
	}
	if res.StatusCode == http.StatusBadRequest {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return badRequest(strings.TrimSpace(string(msg)))
	}
	if res.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("%s %s: %s", method, path, res.Status)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}
