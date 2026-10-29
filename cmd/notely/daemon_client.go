//go:build linux || darwin

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jmnser/notely/internal/note"
)

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
	stream := &http.Client{CheckRedirect: c.http.CheckRedirect} // no timeout: the stream stays open
	res, err := c.do(stream, req)
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
