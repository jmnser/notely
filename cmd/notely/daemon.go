//go:build !wasm

package main

import (
	"bytes"
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

// settle is how long file changes pause before the daemon syncs them.
const settle = 500 * time.Millisecond

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
	Sum     string    `json:"sum"` // SHA-256 of the content
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mtime"`
	Version string    `json:"version"` // the note's updated time or the image's version on the server
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
}

// daemon mirrors a user's notes and images to a directory: it pushes the
// changes made there and writes the changes made elsewhere.
type daemon struct {
	c     *client
	cfg   config
	st    *mirrorState
	root  *os.Root
	files map[string]*file // found by the last scan
	dirs  map[string]bool  // found by the last scan
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
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	unlock, err := lockPath(stateFile, "this server")
	if err != nil {
		return err
	}
	defer unlock()
	unlockDir, err := lockPath(filepath.Join(dir, ".notely"), "this directory")
	if err != nil {
		return err
	}
	defer unlockDir()
	st := &mirrorState{path: stateFile}
	if err := loadJSON(stateFile, st); err != nil {
		return err
	}
	if st.Dir != dir || st.User != cfg.Server.User {
		if len(st.Files) > 0 && st.User != cfg.Server.User {
			return fmt.Errorf("%s holds the notes of %s; sync another directory", st.Dir, st.User)
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
	d := &daemon{c: c, cfg: cfg, st: st, root: root, rejected: map[string]string{}}
	log.Printf("syncing %s with %s", dir, c.base)
	return d.run(ctx)
}

// run syncs on start, when files change, when the server reports a change
// and every syncEvery.
func (d *daemon) run(ctx context.Context) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close() //nolint:errcheck // nothing to do about it
	watched := map[string]bool{}
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
				d.st.Session = ""
				d.save()
				continue
			} else if err != nil && ctx.Err() == nil {
				log.Printf("sync: %v", err)
			}
		}
		d.watch(w, watched)
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

// watch adds the directories of the last scan to w.
func (d *daemon) watch(w *fsnotify.Watcher, watched map[string]bool) {
	dirs := maps.Clone(d.dirs)
	if dirs == nil {
		dirs = map[string]bool{}
	}
	dirs["."] = true
	for dir := range dirs {
		if watched[dir] {
			continue
		}
		if err := w.Add(filepath.Join(d.st.Dir, filepath.FromSlash(dir))); err != nil {
			log.Printf("watch %s: %v", dir, err)
			continue
		}
		watched[dir] = true
	}
	for dir := range watched {
		if !dirs[dir] {
			delete(watched, dir) // removed: the watch is gone
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
	if err := d.pushImages(ctx); err != nil {
		return err
	}
	remote, err := d.pushNotes(ctx)
	if err != nil {
		return err
	}
	d.pullNotes(remote)
	images, err := d.c.images(ctx, d.st.Session)
	if err != nil {
		return err
	}
	return d.pullImages(ctx, images)
}

// scan finds the note and image files, reading those changed since the last
// sync. Dot files and symbolic links are left out.
func (d *daemon) scan() error {
	d.files, d.dirs = map[string]*file{}, map[string]bool{}
	return fs.WalkDir(d.root.FS(), ".", func(p string, de fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
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
			return err
		}
		f := &file{size: info.Size(), mod: info.ModTime()}
		e, known := d.st.Files[p]
		if known && e.Size == f.size && e.ModTime.Equal(f.mod) {
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
			return err
		}
		f.sum = sum(f.data)
		if f.clean = known && e.Sum == f.sum; f.clean {
			e.Size, e.ModTime = f.size, f.mod
			d.st.Files[p] = e
		}
		d.files[p] = f
		return nil
	})
}

// reject logs that file p with sum cannot be synced, once until it changes.
func (d *daemon) reject(p, sum string, err error) {
	if d.rejected[p] != sum {
		log.Printf("%s: %v", p, err)
		d.rejected[p] = sum
	}
}

// pushNotes sends the changed notes and deletions and returns the server's
// notes.
func (d *daemon) pushNotes(ctx context.Context) ([]note.Note, error) {
	present := d.localNotes()
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
		if e.ID != "" && !present[e.ID] {
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
		var n note.Note
		err := errors.New("too large")
		if !f.large {
			n, err = notefile.Decode(f.data, path.Ext(p))
		}
		switch {
		case err != nil && !f.large && untracked(f.data, path.Ext(p)):
			list = append(list, changed{p: p, f: f, n: d.newNote(p, f.data), stale: true})
		case err != nil:
			d.reject(p, f.sum, err)
			if e.ID != "" {
				present[e.ID] = true // not deleted
			}
		default:
			list = append(list, changed{p: p, f: f, n: n})
			if e.ID == n.ID {
				owner[n.ID] = p
			}
		}
	}
	for _, c := range list {
		if o, ok := owner[c.n.ID]; !c.stale && ok && o != c.p { // a copy
			c.n, c.stale = movedTo(c.n, o, c.p, d.spaceOf(path.Dir(c.p))), true
			c.n.ID = randID()
		} else if old, ok := synced[c.n.ID]; !c.stale && ok && old != c.p {
			if _, still := d.files[old]; !still {
				n := movedTo(c.n, old, c.p, d.spaceOf(path.Dir(c.p)))
				c.n, c.f.moved, c.stale = n, old, n != c.n // the front matter follows
			}
		}
		owner[c.n.ID], present[c.n.ID] = c.p, true
		if c.stale && !d.rewrite(c.p, c.f, c.n) {
			continue
		}
		c.f.note = &c.n
	}
	return present
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
			d.track(p, r, f.size, f.mod, f.sum)
			d.place(p, f, r)
		case f.clean && d.st.Files[p].Version == noteVersion(r):
			d.place(p, f, r)
		default:
			d.pull(p, f, r)
		}
	}
	for id, r := range byID {
		if _, ok := local[id]; !ok {
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
	if err == nil {
		err = d.write(to, data)
	}
	if err != nil {
		log.Printf("%s: %v", to, err)
		return
	}
	info, err := d.root.Stat(to)
	if err != nil {
		log.Printf("%s: %v", to, err)
		return
	}
	if p != "" && p != to {
		d.remove(p)
	}
	d.track(to, r, info.Size(), info.ModTime(), sum(data))
	log.Printf("pulled %s", to)
}

// track records the file at p as synced with note r.
func (d *daemon) track(p string, r note.Note, size int64, mod time.Time, sum string) {
	d.st.Files[p] = entry{ID: r.ID, Space: r.Space, Sum: sum, Size: size, ModTime: mod, Version: noteVersion(r)}
}

// pathFor returns where note n belongs: old if it fits n's space and title,
// else the first free <space>/<title>[-N].md (.excalidraw for drawings). As
// on the server, files the daemon does not own are never replaced.
func (d *daemon) pathFor(n note.Note, old string) string {
	base, ext := path.Join(spaceDir(n.Space), notefile.Name(n.Title, "untitled")), notefile.Ext(n.Kind)
	if old != "" && notefile.Numbered(old, base, ext) {
		return old
	}
	for i := 1; ; i++ {
		p := base + ext
		if i > 1 {
			p = fmt.Sprintf("%s-%d%s", base, i, ext)
		}
		if _, err := d.root.Lstat(p); err != nil || strings.EqualFold(p, old) {
			return p
		}
	}
}

// spaceDir is the directory of space: a directory per subspace.
func spaceDir(space string) string {
	if space == "" {
		return "."
	}
	parts := strings.Split(space, "/")
	for i, s := range parts {
		parts[i] = notefile.Name(s, "unnamed")
	}
	return path.Join(parts...)
}

// pushImages uploads new and changed image files and deletes or moves the
// images whose files are gone.
func (d *daemon) pushImages(ctx context.Context) error {
	gone := map[string]string{} // path → sum of images whose files are gone
	for p, e := range d.st.Files {
		if _, ok := d.files[p]; !ok && e.ID == "" {
			gone[p] = e.Sum
		}
	}
	for _, p := range sortedKeys(d.files) {
		f := d.files[p]
		if f.clean || !isImage(p) || d.rejected[p] == f.sum {
			continue
		}
		space, name := d.spaceOf(path.Dir(p)), path.Base(p)
		var img note.Image
		var err error
		if old, ok := movedFrom(gone, p, f.sum); ok {
			delete(gone, old)
			img, err = d.c.moveImage(ctx, d.st.Session, d.imageOf(old), space, strings.TrimSuffix(name, path.Ext(name)))
			if err == nil {
				delete(d.st.Files, old)
			}
		} else {
			if _, ok := d.st.Files[p]; ok { // changed: replace it
				if err := d.c.deleteImage(ctx, d.st.Session, d.imageOf(p)); err != nil && !errors.Is(err, errNotFound) {
					return err
				}
				delete(d.st.Files, p)
			}
			img, err = d.c.addImage(ctx, d.st.Session, space, name, f.data)
		}
		if _, bad := errors.AsType[badRequest](err); bad {
			d.reject(p, f.sum, err)
			continue
		} else if err != nil {
			return err
		}
		to := path.Join(spaceDir(img.Space), img.Name)
		if to != p {
			if _, err := d.root.Lstat(to); err == nil || d.root.Rename(p, to) != nil {
				d.reject(p, f.sum, fmt.Errorf("stored as %s", img.Path()))
				continue
			}
			delete(d.files, p)
			d.files[to] = f
		}
		d.st.Files[to] = entry{Space: img.Space, Sum: f.sum, Size: f.size, ModTime: f.mod, Version: img.Version}
		f.clean = true
		log.Printf("pushed %s", to)
	}
	for p := range gone {
		if err := d.c.deleteImage(ctx, d.st.Session, d.imageOf(p)); err != nil && !errors.Is(err, errNotFound) {
			return err
		}
		delete(d.st.Files, p)
		log.Printf("deleted %s", p)
	}
	return nil
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
		p := path.Join(spaceDir(img.Space), img.Name)
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
		d.st.Files[p] = entry{
			Space: img.Space, Sum: sum(data), Size: info.Size(), ModTime: info.ModTime(), Version: img.Version,
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

// write replaces the file at p atomically and durably.
func (d *daemon) write(p string, data []byte) error {
	if err := d.root.MkdirAll(path.Dir(p), 0o700); err != nil {
		return err
	}
	return atomicfile.WriteFileIn(d.root, p, data)
}

// remove deletes the file at p and its directories once empty.
func (d *daemon) remove(p string) {
	if err := d.root.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("%s: %v", p, err)
		return
	}
	delete(d.st.Files, p)
	for dir := path.Dir(p); dir != "."; dir = path.Dir(dir) {
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
