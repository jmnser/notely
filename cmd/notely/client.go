//go:build !wasm

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jmnser/notely/internal/note"
)

const (
	timeout    = 15 * time.Second // per backend request
	cookieName = "notely_session"
)

var (
	errSignedOut     = errors.New("signed out") // the backend needs a sign-on
	errWrongPassword = errors.New("wrong user name or password")
	errNotFound      = errors.New("not found")
)

// badRequest is the backend's explanation of a rejected request.
type badRequest string

func (e badRequest) Error() string { return string(e) }

// client calls the notely API of one server. It is safe for concurrent use.
type client struct {
	base string // server URL without a trailing slash, e.g. https://host/notely
	http *http.Client
	// stream has no timeout, for the event stream.
	stream *http.Client
	id     string // sent as X-Notely-Client, so the server does not report this client's own changes
}

// newClient checks server, the URL notely is served under. Plain HTTP is only
// accepted for loopback addresses: elsewhere the password and session would
// travel unencrypted.
func newClient(server string) (*client, error) {
	u, err := url.Parse(server)
	if err != nil || u.Host == "" || u.User != nil || u.Scheme != "https" && u.Scheme != "http" {
		return nil, fmt.Errorf("server %q: want an http(s) URL like https://host/notely", server)
	}
	if u.Scheme == "http" && !loopback(u.Hostname()) {
		return nil, fmt.Errorf("server %q: use https beyond localhost", server)
	}
	u.Path, u.RawPath, u.RawQuery, u.Fragment = strings.TrimSuffix(u.Path, "/"), "", "", ""
	// The API never redirects; following one could send the session elsewhere.
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client{
		base:   u.String(),
		http:   &http.Client{Timeout: timeout, CheckRedirect: noRedirect},
		stream: &http.Client{CheckRedirect: noRedirect},
	}, nil
}

func loopback(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || ip != nil && ip.IsLoopback()
}

// login signs on and returns the session token.
func (c *client) login(ctx context.Context, user, password string) (string, error) {
	res, err := c.send(ctx, http.MethodPost, "/api/login", "", map[string]string{"user": user, "password": password})
	if errors.Is(err, errSignedOut) {
		return "", errWrongPassword
	} else if err != nil {
		return "", err
	}
	defer res.Body.Close() //nolint:errcheck // nothing to do about it
	for _, ck := range res.Cookies() {
		if ck.Name == cookieName && ck.Value != "" {
			return ck.Value, nil
		}
	}
	return "", errors.New("sign-on: no session cookie in the response")
}

// logout ends the session on the server.
func (c *client) logout(ctx context.Context, token string) error {
	return c.call(ctx, http.MethodPost, "/api/logout", token, nil, nil)
}

// exchange pushes local changes to the backend and returns its notes and the
// changes it rejected, which do not stop the others.
func (c *client) exchange(ctx context.Context, token string, push []note.Note, deleted []string) (
	[]note.Note, map[string]error, error,
) {
	rejected := map[string]error{}
	for _, n := range push {
		var bad badRequest
		if err := c.call(ctx, http.MethodPut, "/api/notes/"+url.PathEscape(n.ID), token, n, nil); errors.As(err, &bad) {
			rejected[n.ID] = err
		} else if err != nil {
			return nil, nil, err
		}
	}
	for _, id := range deleted {
		var bad badRequest
		if err := c.call(ctx, http.MethodDelete, "/api/notes/"+url.PathEscape(id), token, nil, nil); errors.As(err, &bad) {
			rejected[id] = err // dropped, so it does not block every sync
		} else if err != nil {
			return nil, nil, err
		}
	}
	var notes []note.Note
	return notes, rejected, c.call(ctx, http.MethodGet, "/api/notes", token, nil, &notes)
}

// call sends in as JSON, if not nil, and decodes the JSON response into out.
func (c *client) call(ctx context.Context, method, path, token string, in, out any) error {
	res, err := c.send(ctx, method, path, token, in)
	if err != nil {
		return err
	}
	defer res.Body.Close() //nolint:errcheck // nothing to do about it
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	return nil
}

// send makes a request with in as JSON, if not nil, and returns the response
// if it succeeded; the caller closes its body.
func (c *client) send(ctx context.Context, method, path, token string, in any) (*http.Response, error) {
	if in == nil {
		return c.sendBody(ctx, method, path, token, nil, "")
	}
	b, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	return c.sendBody(ctx, method, path, token, bytes.NewReader(b), "application/json")
}

// sendBody makes a request with body of type contentType and returns the
// response if it succeeded; the caller closes its body.
func (c *client) sendBody(ctx context.Context, method, path, token string, body io.Reader, contentType string) (
	*http.Response, error,
) {
	req, err := c.request(ctx, method, path, token, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return c.do(c.http, req)
}

// request makes a request to the API, signed on with token if not empty.
func (c *client) request(ctx context.Context, method, path, token string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body) //nolint:gosec // the user's -server
	if err != nil {
		return nil, err
	}
	if c.id != "" {
		req.Header.Set("X-Notely-Client", c.id)
	}
	if token != "" {
		req.AddCookie(&http.Cookie{Name: cookieName, Value: token}) //nolint:gosec // a request cookie has no attributes
	}
	return req, nil
}

// do sends req with hc and returns the response if it succeeded; the caller
// closes its body.
func (c *client) do(hc *http.Client, req *http.Request) (*http.Response, error) {
	res, err := hc.Do(req) //nolint:gosec // the user's -server
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 300 {
		return res, nil
	}
	defer res.Body.Close() //nolint:errcheck // nothing to do about it
	text, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	switch res.StatusCode {
	case http.StatusUnauthorized:
		return nil, errSignedOut
	case http.StatusBadRequest:
		return nil, badRequest(strings.TrimSpace(string(text)))
	case http.StatusNotFound:
		return nil, fmt.Errorf("%s %s: %w", req.Method, req.URL.Path, errNotFound)
	case http.StatusTooManyRequests, http.StatusServiceUnavailable: // sign-in throttled
		return nil, errors.New(strings.TrimSpace(string(text)))
	}
	return nil, fmt.Errorf("%s %s: %s", req.Method, req.URL.Path, res.Status)
}

// images lists the images of all spaces.
func (c *client) images(ctx context.Context, token string) ([]note.Image, error) {
	var list []note.Image
	return list, c.call(ctx, http.MethodGet, "/api/images", token, nil, &list)
}

// maxImage is the server's limit, with room for a changed one.
const maxImage = 4 << 20

// image downloads img.
func (c *client) image(ctx context.Context, token string, img note.Image) ([]byte, error) {
	res, err := c.send(ctx, http.MethodGet, imagePath(img), token, nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close() //nolint:errcheck // nothing to do about it
	data, err := io.ReadAll(io.LimitReader(res.Body, maxImage+1))
	if err == nil && len(data) > maxImage {
		err = fmt.Errorf("image %s: too large", img.Path())
	}
	return data, err
}

// addImage uploads data as an image named after file to space and returns
// the image as stored, which may have another name.
func (c *client) addImage(ctx context.Context, token, space, file string, data []byte) (note.Image, error) {
	path := "/api/images?" + url.Values{"space": {space}, "name": {file}}.Encode()
	res, err := c.sendBody(ctx, http.MethodPost, path, token, bytes.NewReader(data), "application/octet-stream")
	if err != nil {
		return note.Image{}, err
	}
	defer res.Body.Close() //nolint:errcheck // nothing to do about it
	var img note.Image
	if err := json.NewDecoder(res.Body).Decode(&img); err != nil {
		return note.Image{}, fmt.Errorf("POST /api/images: %w", err)
	}
	return img, nil
}

// moveImage renames img to label stem in space; the server points the
// notes' references to it.
func (c *client) moveImage(ctx context.Context, token string, img note.Image, space, stem string) (note.Image, error) {
	var out note.Image
	return out, c.call(ctx, http.MethodPut, imagePath(img), token, map[string]string{"name": stem, "space": space}, &out)
}

// deleteImage deletes img.
func (c *client) deleteImage(ctx context.Context, token string, img note.Image) error {
	return c.call(ctx, http.MethodDelete, imagePath(img), token, nil, nil)
}

// imagePath is the API path of img.
func imagePath(img note.Image) string {
	var b strings.Builder
	b.WriteString("/api/images/")
	if img.Space != "" {
		for s := range strings.SplitSeq(img.Space, "/") {
			b.WriteString(url.PathEscape(s) + "/")
		}
	}
	b.WriteString(url.PathEscape(img.Name))
	return b.String()
}

// eventsIdle ends an event stream that stays silent; the server pings every 25 s.
const eventsIdle = time.Minute

// events calls changed whenever the server reports a change made elsewhere,
// until ctx ends or the stream breaks.
func (c *client) events(ctx context.Context, token string, changed func()) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	idle := time.AfterFunc(eventsIdle, cancel)
	defer idle.Stop()
	req, err := c.request(ctx, http.MethodGet, "/api/events?"+url.Values{"client": {c.id}}.Encode(), token, nil)
	if err != nil {
		return err
	}
	res, err := c.do(c.stream, req)
	if err != nil {
		return err
	}
	defer res.Body.Close() //nolint:errcheck // nothing to do about it
	lines := bufio.NewScanner(res.Body)
	for lines.Scan() {
		idle.Reset(eventsIdle)
		if strings.HasPrefix(lines.Text(), "data:") {
			changed()
		}
	}
	if err := lines.Err(); err != nil {
		return err
	}
	return io.ErrUnexpectedEOF
}
