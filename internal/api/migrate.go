package api

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/jmnser/notely/internal/atomicfile"
)

// Former shared layout: notes and spaces directly in the data directory,
// shared by all users, images in .images, referenced as /images/<name>, and
// git backup state in .sync/<user>.
const (
	oldImageDir = ".images"
	oldSyncDir  = ".sync"
	// layoutFile records the migration: "migrating", then "users" once done.
	layoutFile = ".layout"
	// oldUsersSpace holds a former space named like usersDir while migrating.
	oldUsersSpace = ".users-space"
)

// migrate moves the former shared layout in dir to the per-user one: every
// user gets a copy of the notes, as all users saw them, and their git backup
// state. Images go next to the notes that reference them, which then
// reference them by name. Only directories and .md files count as notes, so
// a users file in dir stays. Every step can be repeated, so an interrupted
// migration continues on the next start.
func migrate(dir string, users map[string]argonHash) error {
	marker := filepath.Join(dir, layoutFile)
	state, err := os.ReadFile(marker) //nolint:gosec // under the -data flag
	switch {
	case err == nil && string(state) == usersDir:
		return nil
	case errors.Is(err, fs.ErrNotExist):
		if err := startMigration(dir, marker); err != nil {
			return err
		}
	case err != nil:
		return err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var notes []string // entries of the old layout
	for _, e := range entries {
		switch name := e.Name(); {
		case name == oldUsersSpace:
			notes = append(notes, name)
		case name == usersDir || strings.HasPrefix(name, "."):
		case e.IsDir() || e.Type().IsRegular() && filepath.Ext(name) == noteExt:
			notes = append(notes, name)
		}
	}
	images := filepath.Join(dir, oldImageDir)
	for _, user := range slices.Sorted(maps.Keys(users)) {
		home := filepath.Join(dir, usersDir, user)
		if err := os.MkdirAll(home, 0o700); err != nil {
			return err
		}
		for _, name := range notes {
			to := name
			if name == oldUsersSpace {
				to = usersDir
			}
			if err := copyTree(filepath.Join(dir, name), filepath.Join(home, to)); err != nil {
				return err
			}
		}
		if err := migrateImages(images, home); err != nil {
			return err
		}
		if err := migrateSync(dir, user); err != nil {
			return err
		}
		if len(notes) > 0 {
			log.Printf("copied the shared notes in %s to %s", dir, home)
		}
	}
	for _, name := range notes {
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(images); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dir, oldSyncDir)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("left %s: %v", oldSyncDir, err) // state of removed users
	}
	return atomicfile.WriteFile(marker, []byte(usersDir))
}

// startMigration marks the migration of dir as started, moving a former
// space named like usersDir aside.
func startMigration(dir, marker string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// Without the marker, a users directory is a space of the old layout,
	// which holds no directories.
	if hasDirs(filepath.Join(dir, usersDir)) {
		return fmt.Errorf("%s has user directories but no %s: restore it with the content %q",
			dir, layoutFile, usersDir)
	}
	if _, err := os.Stat(filepath.Join(dir, usersDir)); err == nil {
		if err := os.Rename(filepath.Join(dir, usersDir), filepath.Join(dir, oldUsersSpace)); err != nil {
			return err
		}
	}
	return atomicfile.WriteFile(marker, []byte("migrating"))
}

// hasDirs reports whether dir has subdirectories.
func hasDirs(dir string) bool {
	entries, _ := os.ReadDir(dir)
	return slices.ContainsFunc(entries, fs.DirEntry.IsDir)
}

// copyTree copies the directories, .md files and images of from to to,
// keeping files to has already. Dot entries and symbolic links are skipped.
func copyTree(from, to string) error {
	return filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, p)
		if err != nil {
			return err
		}
		out := filepath.Join(to, rel)
		switch {
		case rel != "." && strings.HasPrefix(d.Name(), "."):
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		case d.IsDir():
			return os.MkdirAll(out, 0o700)
		case !d.Type().IsRegular() || filepath.Ext(p) != noteExt && !imageName.MatchString(d.Name()):
			return nil
		}
		return copyMissing(p, out)
	})
}

// oldImageRef finds references to images of the old layout in Markdown.
var oldImageRef = regexp.MustCompile(`(?m)(^|[(<\s"'])/images/([0-9a-f]{32}\.(?:png|jpg|gif|webp))`)

// migrateImages copies the images in from next to the notes in home that
// reference them as /images/<name>, makes those references relative and
// copies the other images to home, where the weekly cleanup deletes them.
func migrateImages(from, home string) error {
	if _, err := os.Stat(from); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	err := filepath.WalkDir(home, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case p != home && strings.HasPrefix(d.Name(), "."):
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		case d.IsDir() || filepath.Ext(p) != noteExt:
			return nil
		}
		data, err := os.ReadFile(p) //nolint:gosec // under the -data flag
		if err != nil {
			return err
		}
		n, err := decode(data)
		if err != nil || !oldImageRef.MatchString(n.Body) {
			return nil //nolint:nilerr // not a note: left as it is
		}
		for _, m := range oldImageRef.FindAllStringSubmatch(n.Body, -1) {
			if err := copyMissing(filepath.Join(from, m[2]), filepath.Join(filepath.Dir(p), m[2])); err != nil {
				return err
			}
		}
		n.Body = oldImageRef.ReplaceAllString(n.Body, "$1$2")
		return atomicfile.WriteFile(p, encode(n))
	})
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(from)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Type().IsRegular() && imageName.MatchString(e.Name()) {
			if err := copyMissing(filepath.Join(from, e.Name()), filepath.Join(home, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// copyMissing copies file from to to unless to exists or from is missing.
func copyMissing(from, to string) error {
	if _, err := os.Lstat(to); err == nil {
		return nil
	}
	data, err := os.ReadFile(from) //nolint:gosec // under the -data flag
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return atomicfile.WriteFile(to, data)
}

// migrateSync moves .sync/<user> in dir to users/<user>/.sync.
func migrateSync(dir, user string) error {
	from, to := filepath.Join(dir, oldSyncDir, user), filepath.Join(dir, usersDir, user, syncDir)
	if _, err := os.Stat(from); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if _, err := os.Stat(to); err == nil {
		log.Printf("left %s: %s exists", from, to)
		return nil
	}
	if err := os.Rename(from, to); err != nil {
		return err
	}
	log.Printf("moved the git backup state in %s to %s", from, to)
	return nil
}
