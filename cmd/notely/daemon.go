//go:build linux || darwin

package main

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/jmnser/notely/internal/atomicfile"
	"github.com/jmnser/notely/internal/note"
	"github.com/jmnser/notely/internal/notefile"
)

const (
	// settle is how long file changes pause before the daemon syncs them.
	settle = 500 * time.Millisecond
	// racy is how long after a file's modification time its size and time
	// may stay the same through another change, on coarse file systems.
	racy = 2 * time.Second
)

// mirrorState is what the daemon keeps per server: the session and each file
// as last synced, which tells local changes from remote ones.
type mirrorState struct {
	path string

	Session string           `json:"session,omitempty"`
	User    string           `json:"user,omitempty"`
	Dir     string           `json:"dir"`
	Files   map[string]entry `json:"files"` // slash-separated path in Dir → entry
}

// entry is a file as last synced.
type entry struct {
	ID      string    `json:"id,omitempty"` // a note's; empty for an image
	Space   string    `json:"space,omitempty"`
	Title   string    `json:"title,omitempty"` // a note's
	Pinned  bool      `json:"pinned,omitempty"`
	Sum     string    `json:"sum"` // SHA-256 of the content
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mtime"`
	Checked time.Time `json:"checked"`           // when the sum was taken
	Version string    `json:"version"`           // the note's updated time or the image's version on the server
	Updated string    `json:"updated,omitempty"` // the note's updated time in the file
}

// file is a note or image file found by a scan.
type file struct {
	sum   string
	size  int64
	mod   time.Time
	data  []byte     // read unless clean or too large
	clean bool       // unchanged since last synced
	large bool       // too large to sync
	note  *note.Note // a changed note file, decoded
	moved string     // the note's former path
	known bool       // a note to compare with the server's first, e.g. left by a crash
	base  string     // with known: the server's version last synced, if any
}

// daemon mirrors a user's notes and images to a directory: it pushes the
// changes made there and writes the changes made elsewhere.
type daemon struct {
	c         *client
	cfg       config
	st        *mirrorState
	root      *os.Root
	stateFile string
	files     map[string]*file  // found by the last scan
	dirs      map[string]bool   // found by the last scan
	dirSpace  map[string]string // directory → its space, of the synced files
	// missing holds the synced files the last scan did not find: a file is
	// only deleted when two scans miss it, as saves may move it away briefly.
	missing, prevMissing map[string]bool
	again                bool // the next sync is due once changes settle
	// rejected holds files the server or the daemon refused, by path, with
	// their sum: they are left alone until they change.
	rejected map[string]string
}

// runDaemon syncs dir with the server until ctx ends.
func runDaemon(ctx context.Context, c *client, cfg config, dir, stateFile string) error {
	if cfg.Server.User == "" || cfg.Server.PasswordCommand == "" {
		return errors.New("the daemon needs server.user and server.password_command in the config; see -init")
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	unlock, err := lockPath(stateFile, "this server")
	if err != nil {
		return err
	}
	defer unlock()
	st := &mirrorState{path: stateFile}
	if err := loadJSON(stateFile, st); err != nil {
		return err
	}
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) && st.Dir == dir && len(st.Files) > 0 {
		// Syncing it empty would delete every note.
		return fmt.Errorf("%s is missing; restore it, or remove %s to sync it anew", dir, stateFile)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := ownDir(dir); err != nil {
		return err
	}
	unlockDir, err := lockPath(filepath.Join(dir, ".notely"), "this directory")
	if err != nil {
		return err
	}
	defer unlockDir()
	if st.Dir != dir || st.User != cfg.Server.User {
		if len(st.Files) > 0 && st.User != cfg.Server.User {
			return fmt.Errorf("%s holds the notes of %s; sync another directory", st.Dir, st.User)
		}
		if st.User != cfg.Server.User {
			st.Session = ""
		}
		st.Dir, st.User, st.Files = dir, cfg.Server.User, nil
	}
	if st.Files == nil {
		st.Files = map[string]entry{}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close() //nolint:errcheck // read only
	c.id = randID()
	d := &daemon{c: c, cfg: cfg, st: st, root: root, stateFile: stateFile, rejected: map[string]string{}}
	log.Printf("syncing %s with %s", dir, c.base)
	return d.run(ctx)
}

// ownDir refuses a directory with files that the daemon never synced: it
// would add front matter to every Markdown file, upload it and rename images.
func ownDir(dir string) error {
	if _, err := os.Lstat(filepath.Join(dir, ".notely.lock")); !errors.Is(err, fs.ErrNotExist) {
		return nil //nolint:nilerr // synced before, or the lock reports the error
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(entries, func(e os.DirEntry) bool { return !strings.HasPrefix(e.Name(), ".") }) {
		return fmt.Errorf("%s has files but was never synced; use a new or empty directory", dir)
	}
	return nil
}

// run syncs on start, when files change, when the server reports a change
// and every syncEvery.
func (d *daemon) run(ctx context.Context) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close() //nolint:errcheck // nothing to do about it
	wake := make(chan struct{}, 1)
	notify := func() {
		select {
		case wake <- struct{}{}:
		default: // a sync is already due
		}
	}
	tick := time.NewTicker(syncEvery)
	defer tick.Stop()
	pause := time.NewTimer(0)
	pause.Stop()
	stopEvents, session := func() {}, ""
	defer func() { stopEvents() }()
	for {
		if err := d.signIn(ctx); errors.Is(err, errWrongPassword) {
			return err
		} else if err != nil {
			log.Printf("sign in: %v", err)
		}
		if d.st.Session != session {
			stopEvents()
			if session = d.st.Session; session != "" {
				stopEvents = d.listen(ctx, session, notify)
			}
		}
		if session != "" {
			if err := d.sync(ctx); errors.Is(err, errSignedOut) {
				d.st.Session = "" // signs in again on the next wake, not at once
				d.save()
			} else if err != nil && ctx.Err() == nil {
				log.Printf("sync: %v", err)
			}
			if d.again {
				pause.Reset(settle)
			}
		}
		d.watch(w)
	wait:
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-wake:
			case <-tick.C:
			case <-pause.C:
			case ev := <-w.Events:
				if !strings.HasPrefix(filepath.Base(ev.Name), ".") { // not the temporary files of writes
					pause.Reset(settle)
				}
				continue
			case err := <-w.Errors:
				log.Printf("watch: %v", err)
				continue
			}
			break wait
		}
	}
}

// listen notifies on each change the server reports, reconnecting after
// syncEvery when the stream breaks, until stop is called or the session ends.
func (d *daemon) listen(ctx context.Context, session string, notify func()) (stop func()) {
	ctx, stop = context.WithCancel(ctx)
	go func() {
		for {
			err := d.c.events(ctx, session, notify)
			if ctx.Err() != nil {
				return
			}
			if errors.Is(err, errSignedOut) {
				notify() // the sync signs in again
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(syncEvery):
				notify() // for the changes missed meanwhile
			}
		}
	}()
	return stop
}

// watch adds the directories of the last scan to w; adding a watched one
// again does nothing, a recreated one gets a new watch.
func (d *daemon) watch(w *fsnotify.Watcher) {
	dirs := maps.Clone(d.dirs)
	if dirs == nil {
		dirs = map[string]bool{}
	}
	dirs["."] = true
	for dir := range dirs {
		if err := w.Add(filepath.Join(d.st.Dir, filepath.FromSlash(dir))); err != nil {
			log.Printf("watch %s: %v", dir, err)
		}
	}
}

// signIn signs on with the password command unless there is a session.
func (d *daemon) signIn(ctx context.Context) error {
	if d.st.Session != "" {
		return nil
	}
	var out bytes.Buffer
	cmd := passwordCommand(d.cfg.Server.PasswordCommand, &out)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("password_command: %w", err)
	}
	password, err := firstLine(out.String())
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	token, err := d.c.login(ctx, d.cfg.Server.User, password)
	if err != nil {
		return err
	}
	d.st.Session = token
	d.save()
	return nil
}

func (d *daemon) save() {
	if err := saveJSON(d.st.path, d.st); err != nil {
		log.Printf("save state: %v", err)
	}
}

// sync pushes the local changes, then writes the server's.
func (d *daemon) sync(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, syncTimeout)
	defer cancel()
	defer d.save()
	if err := d.scan(); err != nil {
		return err
	}
	before, _, err := d.c.exchange(ctx, d.st.Session, nil, nil)
	if err != nil {
		return err
	}
	images, err := d.c.images(ctx, d.st.Session)
	if err != nil {
		return err
	}
	if len(before) == 0 && len(images) == 0 && len(d.st.Files) > 0 {
		// E.g. a server restored without its data: the files may be the last copy.
		return fmt.Errorf("the server has no notes, so the files are kept; delete them, "+
			"or stop the daemon and remove %s to upload them again", d.stateFile)
	}
	// Notes first: the server carries the images of moved notes along, while
	// moving an image changes the notes using it.
	remote, err := d.pushNotes(ctx, before)
	if err != nil {
		return err
	}
	if err := d.pushImages(ctx); err != nil {
		return err
	}
	if images, err = d.c.images(ctx, d.st.Session); err != nil {
		return err
	}
	d.pullNotes(remote)
	return d.pullImages(ctx, images)
}

// scan finds the note and image files, reading those changed since the last
// sync. Dot files and symbolic links are left out.
func (d *daemon) scan() error {
	// The lock file goes with the directory: syncing a new, empty one would
	// delete every note.
	if _, err := d.root.Lstat(".notely.lock"); err != nil {
		return fmt.Errorf("%s was removed; restart the daemon", d.st.Dir)
	}
	d.files, d.dirs, d.dirSpace = map[string]*file{}, map[string]bool{}, map[string]string{}
	for p, e := range d.st.Files {
		d.mapDir(path.Dir(p), e.Space)
	}
	if err := d.walk(); err != nil {
		return err
	}
	if len(d.files) == 0 && len(d.st.Files) > 0 {
		log.Printf("all files in %s are gone; downloading them again", d.st.Dir)
		d.st.Files = map[string]entry{}
	}
	missing := map[string]bool{}
	for p := range d.st.Files {
		if _, ok := d.files[p]; !ok {
			missing[p] = true
		}
	}
	d.prevMissing, d.missing, d.again = d.missing, missing, false
	return nil
}

// deletable reports whether the synced file at p is gone for good: missed by
// two scans. Else the next sync is due once changes settle.
func (d *daemon) deletable(p string) bool {
	if d.prevMissing[p] {
		return true
	}
	d.again = true
	return false
}

// walk adds the note and image files to d.files and the directories to d.dirs.
func (d *daemon) walk() error {
	return fs.WalkDir(d.root.FS(), ".", func(p string, de fs.DirEntry, err error) error {
		switch {
		case err != nil && p == ".":
			return err
		case err != nil:
			return d.unreadable(p, de, err)
		case p != "." && strings.HasPrefix(de.Name(), "."):
			if de.IsDir() {
				return fs.SkipDir
			}
			return nil
		case de.IsDir():
			d.dirs[p] = true
			return nil
		case !de.Type().IsRegular() || !notefile.IsNote(p) && !isImage(p):
			return nil
		}
		info, err := de.Info()
		if err != nil {
			return d.unreadable(p, de, err)
		}
		f := &file{size: info.Size(), mod: info.ModTime()}
		e, known := d.st.Files[p]
		if known && e.Size == f.size && e.ModTime.Equal(f.mod) && f.mod.Before(e.Checked.Add(-racy)) {
			f.sum, f.clean = e.Sum, true
			d.files[p] = f
			return nil
		}
		if f.size > maxImage { // kept, but not read
			f.sum, f.large = fmt.Sprintf("size %d, %s", f.size, f.mod), true
			d.files[p] = f
			d.reject(p, f.sum, errors.New("too large"))
			return nil
		}
		if f.data, err = d.root.ReadFile(p); err != nil {
			return d.unreadable(p, de, err)
		}
		f.sum = sum(f.data)
		if f.clean = known && e.Sum == f.sum; f.clean {
			e.Size, e.ModTime, e.Checked = f.size, f.mod, time.Now()
			d.st.Files[p] = e
		}
		d.files[p] = f
		return nil
	})
}

// unreadable logs that the scan cannot read p and keeps the synced files at
// or below it as they were, so they are not deleted. A file that vanished
// meanwhile is left out.
func (d *daemon) unreadable(p string, de fs.DirEntry, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	d.reject(p, "unreadable", err)
	for k, e := range d.st.Files {
		if k == p || strings.HasPrefix(k, p+"/") {
			d.files[k] = &file{sum: e.Sum, size: e.Size, mod: e.ModTime, clean: true}
		}
	}
	if de != nil && de.IsDir() {
		return fs.SkipDir
	}
	return nil
}

// reject logs that file p with sum cannot be synced, once until it changes.
func (d *daemon) reject(p, sum string, err error) {
	if d.rejected[p] != sum {
		log.Printf("%s: %v", p, err)
		d.rejected[p] = sum
	}
}

// pushNotes sends the changed notes and deletions and returns the server's
// notes; before is its list from the start of the sync.
func (d *daemon) pushNotes(ctx context.Context, before []note.Note) ([]note.Note, error) {
	present := d.localNotes()
	d.compare(before)
	var push []note.Note
	paths := map[string]string{} // ID → path of the pushed notes
	for p, f := range d.files {
		if f.note != nil && d.rejected[p] != f.sum {
			push = append(push, *f.note)
			paths[f.note.ID] = p
		}
	}
	var deleted []string
	gone := map[string]string{} // ID → path
	for p, e := range d.st.Files {
		if _, ok := d.files[p]; e.ID != "" && !present[e.ID] && (ok || d.deletable(p)) {
			deleted = append(deleted, e.ID)
			gone[e.ID] = p
		}
	}
	remote, rejected, err := d.c.exchange(ctx, d.st.Session, push, deleted)
	if err != nil {
		return nil, err
	}
	for id, p := range gone {
		delete(d.st.Files, p) // a rejected deletion is dropped, as in the terminal UI
		if rejected[id] == nil {
			log.Printf("deleted %s", p)
		}
	}
	for id, p := range paths {
		if err := rejected[id]; err != nil {
			d.reject(p, d.files[p].sum, err)
			delete(paths, id)
		} else {
			if old := d.files[p].moved; old != "" {
				delete(d.st.Files, old)
			}
			log.Printf("pushed %s", p)
		}
	}
	return remote, nil
}

// compare checks the known notes against the server's: a file that only
// differs in the updated time is taken as synced; an older one, or a move
// while the note changed there, is replaced by the server's version.
func (d *daemon) compare(remote []note.Note) {
	var known []string
	for _, p := range sortedKeys(d.files) {
		if f := d.files[p]; f.known && f.note != nil {
			known = append(known, p)
		}
	}
	byID := map[string]note.Note{}
	for _, r := range remote {
		byID[r.ID] = r
	}
	for _, p := range known {
		f := d.files[p]
		r, ok := byID[f.note.ID]
		switch {
		case !ok:
		case same(*f.note, r):
			d.track(p, r, f.note.Updated, f.size, f.mod, f.sum)
			f.note, f.clean = nil, true
		case f.base != "" && noteVersion(r) == f.base: // unchanged there: push the move
			continue
		case f.base != "" || r.Updated.After(f.note.Updated):
			d.st.Files[p] = entry{ID: r.ID, Space: r.Space} // pulled
			f.note, f.clean = nil, true
		default:
			continue
		}
		if f.moved != "" {
			delete(d.st.Files, f.moved)
		}
	}
}

// localNotes decodes the changed note files and returns the IDs found. A
// file without front matter becomes a new note, named after the file in the
// space of its directory, and gets front matter. A copied file gets a new
// ID, a moved one the space of its directory and its file name as title.
func (d *daemon) localNotes() map[string]bool {
	present := map[string]bool{}
	owner := map[string]string{}  // ID → path of the file keeping it
	synced := map[string]string{} // ID → path last synced
	for p, e := range d.st.Files {
		if e.ID != "" {
			synced[e.ID] = p
		}
	}
	type changed struct {
		p     string
		f     *file
		n     note.Note
		stale bool // the file needs n's fields
	}
	var list []changed
	for _, p := range sortedKeys(d.files) {
		f, e := d.files[p], d.st.Files[p]
		if f.clean && e.ID != "" {
			present[e.ID], owner[e.ID] = true, p
		}
		if f.clean || !notefile.IsNote(p) {
			continue
		}
		n, stale, ok := d.decodeNote(p, f, e)
		if !ok {
			if e.ID != "" {
				present[e.ID] = true // not deleted
			}
			continue
		}
		if e.ID == n.ID {
			owner[n.ID] = p
		}
		list = append(list, changed{p: p, f: f, n: n, stale: stale})
	}
	for _, c := range list {
		o, owned := owner[c.n.ID]
		old, wasSynced := synced[c.n.ID]
		switch {
		case c.stale:
		case owned && o != c.p: // a copy
			c.n, c.stale = movedTo(c.n, o, c.p, d.spaceOf(path.Dir(c.p))), true
			c.n.ID = randID()
		case wasSynced && old != c.p:
			if _, still := d.files[old]; !still {
				// Unchanged content may be a move the daemon pulled before a crash.
				if e := d.st.Files[old]; c.f.sum == e.Sum {
					c.f.known, c.f.base = true, e.Version
				}
				n := movedTo(c.n, old, c.p, d.spaceOf(path.Dir(c.p)))
				c.n, c.f.moved, c.stale = n, old, n != c.n // the front matter follows
			}
		case !wasSynced:
			c.f.known = true
		default:
			// Newer than synced: pulled before a crash, not edited.
			base, err := time.Parse(time.RFC3339Nano, d.st.Files[c.p].Version)
			c.f.known = err == nil && c.n.Updated.After(base)
		}
		owner[c.n.ID], present[c.n.ID] = c.p, true
		if c.stale && !d.rewrite(c.p, c.f, c.n) {
			continue
		}
		c.f.note = &c.n
	}
	return present
}

// decodeNote decodes the changed note file at p, last synced as e. ok is
// false if it cannot be synced, stale if the file needs n's fields.
func (d *daemon) decodeNote(p string, f *file, e entry) (n note.Note, stale, ok bool) {
	if f.large { // rejected by the scan
		return n, false, false
	}
	data := bytes.TrimPrefix(f.data, []byte("\xef\xbb\xbf")) // a byte order mark some editors add
	n, err := notefile.Decode(data, path.Ext(p))
	switch {
	case err == nil:
		return n, false, true
	case e.ID != "" && len(bytes.TrimSpace(f.data)) == 0: // maybe half written
		err = errors.New("empty; delete the file to delete the note")
	case untracked(data, path.Ext(p)):
		n = d.newNote(p, data)
		if e.ID != "" { // lost its fields, e.g. saved by Excalidraw: still the same note
			n.ID, n.Space, n.Title, n.Pinned = e.ID, e.Space, cmp.Or(e.Title, n.Title), e.Pinned
		}
		return n, true, true
	}
	d.reject(p, f.sum, err)
	return n, false, false
}

// untracked reports whether data, of a file with extension ext, has no notely
// fields: Markdown without front matter or an Excalidraw scene.
func untracked(data []byte, ext string) bool {
	if ext == notefile.DrawingExt {
		var scene map[string]json.RawMessage
		if json.Unmarshal(data, &scene) != nil || scene == nil {
			return false
		}
		_, tracked := scene["notely"]
		return !tracked
	}
	return !bytes.HasPrefix(data, []byte("---\n")) && !bytes.HasPrefix(data, []byte("---\r\n"))
}

// newNote is the note of an untracked file at p.
func (d *daemon) newNote(p string, data []byte) note.Note {
	ext := path.Ext(p)
	n := note.Note{
		ID: randID(), Space: d.spaceOf(path.Dir(p)), Title: strings.TrimSuffix(path.Base(p), ext),
		Body: string(data), Updated: time.Now().UTC(),
	}
	if ext == notefile.DrawingExt {
		n.Kind, n.Body = note.Drawing, string(bytes.TrimSpace(data))
	}
	return n
}

// movedTo updates n, whose file moved from old to p in space: a new
// directory is a new space, a new file name a new title.
func movedTo(n note.Note, old, p, space string) note.Note {
	if path.Dir(old) != path.Dir(p) {
		n.Space = space
	}
	ext := path.Ext(p)
	if !notefile.Numbered(p, path.Join(path.Dir(p), notefile.Name(n.Title, "untitled")), ext) {
		n.Title = strings.TrimSuffix(path.Base(p), ext)
	}
	return n
}

// rewrite gives the file at p the content of n, unless it changed since the
// scan, and reports whether it did.
func (d *daemon) rewrite(p string, f *file, n note.Note) bool {
	data, err := notefile.Encode(n)
	if err != nil {
		d.reject(p, f.sum, err)
		return false
	}
	if !d.unchanged(p, f) {
		return false
	}
	if err := d.write(p, data); err != nil {
		log.Printf("%s: %v", p, err)
		return false
	}
	info, err := d.root.Stat(p)
	if err != nil {
		log.Printf("%s: %v", p, err)
		return false
	}
	f.data, f.sum, f.size, f.mod = data, sum(data), info.Size(), info.ModTime()
	return true
}

// spaceOf returns the space of directory dir: that of the files synced in
// it, else its path.
func (d *daemon) spaceOf(dir string) string {
	for p, e := range d.st.Files {
		if path.Dir(p) == dir {
			return e.Space
		}
	}
	if dir == "." {
		return ""
	}
	return note.SpacePath(dir)
}

// pullNotes writes the server's notes: each one changed elsewhere or new,
// unless its file has changes the server did not take. Files of notes
// deleted elsewhere are removed unless changed.
func (d *daemon) pullNotes(remote []note.Note) {
	byID := map[string]note.Note{}
	for _, n := range remote {
		byID[n.ID] = n
	}
	local := map[string]string{} // ID → path
	for p, f := range d.files {
		id := d.st.Files[p].ID
		if f.note != nil {
			id = f.note.ID
		}
		if id != "" {
			local[id] = p
		}
	}
	for id, p := range local {
		f := d.files[p]
		r, ok := byID[id]
		pushed := f.note != nil && d.rejected[p] != f.sum
		switch {
		case !f.clean && !pushed: // rejected or unreadable
		case !ok && f.clean:
			if d.unchanged(p, f) {
				d.remove(p)
				log.Printf("removed %s, deleted elsewhere", p)
			}
		case !ok: // pushed, but deleted meanwhile; the next sync restores it
		case pushed && same(*f.note, r):
			d.track(p, r, f.note.Updated, f.size, f.mod, f.sum)
			d.place(p, f, r)
		case f.clean && d.st.Files[p].Version == noteVersion(r):
			d.place(p, f, r)
		default:
			d.pull(p, f, r)
		}
	}
	pending := map[string]bool{} // IDs of files missing, not yet deleted
	for p := range d.missing {
		pending[d.st.Files[p].ID] = true
	}
	for id, r := range byID {
		if _, ok := local[id]; !ok && !pending[id] {
			d.pull("", nil, r)
		}
	}
}

// place moves the file at p of note r where r belongs, if it does not.
func (d *daemon) place(p string, f *file, r note.Note) {
	if d.pathFor(r, p) != p {
		d.pull(p, f, r)
	}
}

// pull writes note r to its file, moving it from p if not empty, unless the
// file at p changed since the scan.
func (d *daemon) pull(p string, f *file, r note.Note) {
	if p != "" && !d.unchanged(p, f) {
		return
	}
	to := d.pathFor(r, p)
	data, err := notefile.Encode(r)
	if err != nil {
		log.Printf("%s: %v", to, err)
		return
	}
	// A move renames first: a crash then leaves one file of the note, which
	// the next sync compares with the server.
	if p != "" && p != to {
		if err := d.rename(p, to); err != nil {
			log.Printf("%s: %v", to, err)
			return
		}
	}
	if err := d.write(to, data); err != nil {
		log.Printf("%s: %v", to, err)
		return
	}
	info, err := d.root.Stat(to)
	if err != nil {
		log.Printf("%s: %v", to, err)
		return
	}
	d.track(to, r, r.Updated, info.Size(), info.ModTime(), sum(data))
	log.Printf("pulled %s", to)
}

// track records the file at p, whose note has updated time local, as
// synced with note r.
func (d *daemon) track(p string, r note.Note, local time.Time, size int64, mod time.Time, sum string) {
	d.mapDir(path.Dir(p), r.Space)
	d.st.Files[p] = entry{
		ID: r.ID, Space: r.Space, Title: r.Title, Pinned: r.Pinned, Sum: sum, Size: size, ModTime: mod,
		Checked: time.Now(), Version: noteVersion(r), Updated: noteVersion(note.Note{Updated: local}),
	}
}

// pathFor returns where note n belongs: old if it fits n's space and title,
// else the first free <space>/<title>[-N].md (.excalidraw for drawings). As
// on the server, files the daemon does not own are never replaced.
func (d *daemon) pathFor(n note.Note, old string) string {
	base, ext := path.Join(d.spaceDir(n.Space), notefile.Name(n.Title, "untitled")), notefile.Ext(n.Kind)
	if old != "" && notefile.Numbered(old, base, ext) {
		return old
	}
	for i := 1; ; i++ {
		p := base + ext
		if i > 1 {
			p = fmt.Sprintf("%s-%d%s", base, i, ext)
		}
		if d.trackedElse(p, n.ID) {
			continue // another note's, maybe moved away briefly
		}
		if _, err := d.root.Lstat(p); err != nil || p == old || strings.EqualFold(p, old) && d.sameFile(p, old) {
			return p
		}
	}
}

// trackedElse reports whether p, also ignoring case, is synced as another
// note than id.
func (d *daemon) trackedElse(p, id string) bool {
	if e, ok := d.st.Files[p]; ok {
		return e.ID != id
	}
	for o, e := range d.st.Files {
		if e.ID != id && strings.EqualFold(o, p) {
			return true
		}
	}
	return false
}

// spaceDir is the directory of space: a directory per subspace, named
// after it. A name that another space's directory has already, also
// ignoring case as macOS does, gets -2, -3, ….
func (d *daemon) spaceDir(space string) string {
	dir := "."
	if space == "" {
		return dir
	}
	parts := strings.Split(space, "/")
	for i, part := range parts {
		sub, name := strings.Join(parts[:i+1], "/"), notefile.Name(part, "unnamed")
		for n := 1; ; n++ {
			c := path.Join(dir, name)
			if n > 1 {
				c = path.Join(dir, fmt.Sprintf("%s-%d", name, n))
			}
			if !d.dirTaken(c, sub) {
				dir = c
				break
			}
		}
	}
	return dir
}

// dirTaken reports whether directory dir, or one differing only in case,
// holds another space than space.
func (d *daemon) dirTaken(dir, space string) bool {
	for o, s := range d.dirSpace {
		if s != space && strings.EqualFold(o, dir) {
			return true
		}
	}
	return false
}

// mapDir records that directory dir holds space, and its parents the
// parent spaces.
func (d *daemon) mapDir(dir, space string) {
	if dir == "." || d.dirSpace == nil {
		return
	}
	dirs, spaces := strings.Split(dir, "/"), strings.Split(space, "/")
	if len(dirs) != len(spaces) {
		return
	}
	for i := range dirs {
		d.dirSpace[strings.Join(dirs[:i+1], "/")] = strings.Join(spaces[:i+1], "/")
	}
}

// pushImages uploads new and changed image files and deletes or moves the
// images whose files are gone. An image whose content is in a file not
// synced yet is not deleted, so a move the server refused does not lose it.
func (d *daemon) pushImages(ctx context.Context) error {
	unsynced := map[string]bool{} // sums of image files not synced yet
	for p, f := range d.files {
		if isImage(p) && !f.clean {
			unsynced[f.sum] = true
		}
	}
	gone := map[string]string{} // path → sum of images whose files are gone
	for p, e := range d.st.Files {
		if _, ok := d.files[p]; !ok && e.ID == "" {
			gone[p] = e.Sum
		}
	}
	for _, p := range sortedKeys(d.files) {
		f := d.files[p]
		if f.clean || f.large || !isImage(p) || d.rejected[p] == f.sum {
			continue
		}
		img, err := d.pushImage(ctx, p, f, gone)
		if _, bad := errors.AsType[badRequest](err); bad {
			d.reject(p, f.sum, err)
			continue
		} else if err != nil {
			return err
		}
		if !note.ImageName.MatchString(img.Name) {
			d.reject(p, f.sum, fmt.Errorf("invalid name from the server: %q", img.Name))
			continue
		}
		d.again = true // the server may have changed the notes using it
		to := path.Join(d.spaceDir(img.Space), img.Name)
		if e, ok := d.st.Files[to]; to != p && ok && e.ID == "" {
			d.reject(p, f.sum, fmt.Errorf("the same image as %s", to)) // stored once per space
			continue
		}
		if to != p {
			if _, err := d.root.Lstat(to); err == nil || d.root.Rename(p, to) != nil {
				d.reject(p, f.sum, fmt.Errorf("stored as %s", img.Path()))
				continue
			}
			delete(d.files, p)
			d.files[to] = f
		}
		d.mapDir(path.Dir(to), img.Space)
		d.st.Files[to] = entry{
			Space: img.Space, Sum: f.sum, Size: f.size, ModTime: f.mod, Checked: time.Now(), Version: img.Version,
		}
		f.clean = true
		log.Printf("pushed %s", to)
	}
	for _, p := range sortedKeys(gone) {
		if unsynced[gone[p]] || !d.deletable(p) {
			continue
		}
		if err := d.c.deleteImage(ctx, d.st.Session, d.imageOf(p)); err != nil && !errors.Is(err, errNotFound) {
			return err
		}
		delete(d.st.Files, p)
		log.Printf("deleted %s", p)
	}
	return nil
}

// pushImage sends the image file at p: as a move of a file gone with the
// same content, as a replacement of the image last synced at p, else as a
// new image. A replacement is uploaded before the former image is deleted.
func (d *daemon) pushImage(ctx context.Context, p string, f *file, gone map[string]string) (note.Image, error) {
	space, name := d.spaceOf(path.Dir(p)), path.Base(p)
	if old, ok := movedFrom(gone, p, f.sum); ok {
		img, err := d.c.moveImage(ctx, d.st.Session, d.imageOf(old), space, note.ImageLabel(name))
		if err == nil {
			delete(gone, old)
			delete(d.st.Files, old)
			return img, nil
		}
		if !errors.Is(err, errNotFound) { // else deleted elsewhere: upload it
			return img, err
		}
		delete(gone, old)
		delete(d.st.Files, old)
	}
	img, err := d.c.addImage(ctx, d.st.Session, space, name, f.data)
	e, replace := d.st.Files[p]
	if err != nil || !replace || e.ID != "" {
		return img, err
	}
	former := d.imageOf(p)
	if img.Space == former.Space && img.Name == former.Name { // the same content
		return img, nil
	}
	if e, ok := d.st.Files[path.Join(d.spaceDir(img.Space), img.Name)]; ok && e.ID == "" {
		return img, nil // the same as another image, which stays
	}
	// Until the move succeeds, p stays synced as the former image, so the
	// next sync retries: the upload then finds the same image again.
	if err := d.c.deleteImage(ctx, d.st.Session, former); err != nil && !errors.Is(err, errNotFound) {
		return img, err
	}
	return d.c.moveImage(ctx, d.st.Session, img, former.Space, former.Stem())
}

// movedFrom returns the path in gone that the image at p with sum moved from.
func movedFrom(gone map[string]string, p, sum string) (string, bool) {
	for _, old := range sortedKeys(gone) {
		if gone[old] == sum && path.Ext(old) == path.Ext(p) {
			return old, true
		}
	}
	return "", false
}

// imageOf is the image last synced at p.
func (d *daemon) imageOf(p string) note.Image {
	return note.Image{Space: d.st.Files[p].Space, Name: path.Base(p)}
}

// pullImages downloads the images that are new or changed elsewhere, unless
// their files changed, and removes the files of images deleted elsewhere.
func (d *daemon) pullImages(ctx context.Context, images []note.Image) error {
	remote := map[string]bool{}
	for _, img := range images {
		if !note.ImageName.MatchString(img.Name) {
			continue
		}
		p := path.Join(d.spaceDir(img.Space), img.Name)
		remote[p] = true
		f, ok := d.files[p]
		e, known := d.st.Files[p]
		switch {
		case ok && (!f.clean || e.Version == img.Version):
			continue
		case !ok && known: // deleted here, but not yet on the server
			continue
		}
		data, err := d.c.image(ctx, d.st.Session, img)
		if err != nil {
			return err
		}
		if ok && !d.unchanged(p, f) {
			continue
		}
		if err := d.write(p, data); err != nil {
			log.Printf("%s: %v", p, err)
			continue
		}
		info, err := d.root.Stat(p)
		if err != nil {
			return err
		}
		d.mapDir(path.Dir(p), img.Space)
		d.st.Files[p] = entry{
			Space: img.Space, Sum: sum(data), Size: info.Size(), ModTime: info.ModTime(), Checked: time.Now(),
			Version: img.Version,
		}
		log.Printf("pulled %s", p)
	}
	for p, e := range d.st.Files {
		if e.ID != "" || remote[p] {
			continue
		}
		if f, ok := d.files[p]; ok && f.clean && d.unchanged(p, f) {
			d.remove(p)
			log.Printf("removed %s, deleted elsewhere", p)
		}
		if f, ok := d.files[p]; !ok || f.clean {
			delete(d.st.Files, p)
		}
	}
	return nil
}

// unchanged reports whether the file at p is as the scan found it.
func (d *daemon) unchanged(p string, f *file) bool {
	info, err := d.root.Lstat(p)
	return err == nil && info.Size() == f.size && info.ModTime().Equal(f.mod)
}

// sameFile reports whether a and b name the same file, e.g. differing only
// in case on a case-insensitive file system.
func (d *daemon) sameFile(a, b string) bool {
	fa, errA := d.root.Stat(a)
	fb, errB := d.root.Stat(b)
	return errA == nil && errB == nil && os.SameFile(fa, fb)
}

// write replaces the file at p atomically and durably.
func (d *daemon) write(p string, data []byte) error {
	if err := d.root.MkdirAll(path.Dir(p), 0o700); err != nil {
		return err
	}
	return atomicfile.WriteFileIn(d.root, p, data)
}

// rename moves the file at p to the free path to, and forgets p.
func (d *daemon) rename(p, to string) error {
	if err := d.root.MkdirAll(path.Dir(to), 0o700); err != nil {
		return err
	}
	if err := d.root.Rename(p, to); err != nil {
		return err
	}
	delete(d.st.Files, p)
	d.removeDirs(path.Dir(p))
	return nil
}

// remove deletes the file at p and its directories once empty.
func (d *daemon) remove(p string) {
	if err := d.root.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("%s: %v", p, err)
		return
	}
	delete(d.st.Files, p)
	d.removeDirs(path.Dir(p))
}

// removeDirs removes dir and its parents while empty.
func (d *daemon) removeDirs(dir string) {
	for ; dir != "."; dir = path.Dir(dir) {
		if d.root.Remove(dir) != nil { // not empty
			return
		}
	}
}

// same reports whether a and b differ at most in their updated time.
func same(a, b note.Note) bool {
	a.Updated = b.Updated
	return a == b
}

// noteVersion is the server's version of n: it stamps the updated time on each
// change.
func noteVersion(n note.Note) string { return n.Updated.UTC().Format(time.RFC3339Nano) }

// isImage reports whether p names an image file by its extension.
func isImage(p string) bool {
	switch strings.ToLower(path.Ext(p)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	}
	return false
}

func sum(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
