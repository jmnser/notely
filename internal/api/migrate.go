package api

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/jmnser/notely/internal/atomicfile"
)

// Former shared layout: notes and spaces directly in the data directory,
// images in .images, referenced as /images/<name>, and git backup state in
// .sync/<user>.
const (
	oldImageDir = ".images"
	oldSyncDir  = ".sync"
)

// migrate moves the notes of the former shared layout in dir to the only
// user, or else the default user, and each user's git backup state to their
// directory. Images move next to the notes that reference them, which then
// reference them by name. Every step can be repeated, so an interrupted
// migration continues on the next start.
func migrate(dir string, users map[string]argonHash) error {
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	var notes []string
	images, syncs := false, false
	for _, e := range entries {
		switch name := e.Name(); {
		case name == oldImageDir:
			images = true
		case name == oldSyncDir:
			syncs = true
		case name != usersDir && !strings.HasPrefix(name, "."):
			notes = append(notes, name)
		}
	}
	if len(notes) > 0 || images {
		owner := defaultUser
		for name := range users {
			if len(users) == 1 {
				owner = name
			}
		}
		if _, ok := users[owner]; !ok {
			return fmt.Errorf("notes in %s belong to no user: add user %s or move them to %s/<user>", dir, defaultUser, usersDir)
		}
		home := filepath.Join(dir, usersDir, owner)
		if err := os.MkdirAll(home, 0o700); err != nil {
			return err
		}
		for _, name := range notes {
			if err := os.Rename(filepath.Join(dir, name), filepath.Join(home, name)); err != nil {
				return err
			}
		}
		if len(notes) > 0 {
			log.Printf("moved the notes in %s to %s", dir, home)
		}
		if images {
			if err := migrateImages(filepath.Join(dir, oldImageDir), home); err != nil {
				return err
			}
		}
	}
	if syncs {
		return migrateSync(dir)
	}
	return nil
}

// migrateImages copies the images in from next to the notes in home that
// reference them as /images/<name>, makes those references relative, moves
// the remaining images to home, where the weekly cleanup deletes them, and
// removes from.
func migrateImages(from, home string) error {
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
		if err != nil || !strings.Contains(n.Body, "/images/") {
			return nil //nolint:nilerr // not a note: left as it is
		}
		for _, name := range imageRef.FindAllString(n.Body, -1) {
			ref := "/images/" + name
			if !strings.Contains(n.Body, ref) {
				continue
			}
			img, err := os.ReadFile(filepath.Join(from, name)) //nolint:gosec // a checked image name
			if errors.Is(err, fs.ErrNotExist) {
				continue
			} else if err != nil {
				return err
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(p), name)); errors.Is(err, fs.ErrNotExist) {
				if err := atomicfile.WriteFile(filepath.Join(filepath.Dir(p), name), img); err != nil {
					return err
				}
			}
			n.Body = strings.ReplaceAll(n.Body, ref, name)
		}
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
		if !e.Type().IsRegular() || !imageName.MatchString(e.Name()) {
			continue
		}
		to := filepath.Join(home, e.Name())
		if _, err := os.Stat(to); err == nil {
			continue
		}
		if err := os.Rename(filepath.Join(from, e.Name()), to); err != nil {
			return err
		}
	}
	log.Printf("moved the images in %s next to their notes in %s", from, home)
	return os.RemoveAll(from)
}

// migrateSync moves .sync/<user> to users/<user>/.sync and removes .sync once empty.
func migrateSync(dir string) error {
	from := filepath.Join(dir, oldSyncDir)
	entries, err := os.ReadDir(from)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() || !validID.MatchString(e.Name()) {
			continue
		}
		home := filepath.Join(dir, usersDir, e.Name())
		if err := os.MkdirAll(home, 0o700); err != nil {
			return err
		}
		if err := os.Rename(filepath.Join(from, e.Name()), filepath.Join(home, syncDir)); err != nil {
			return err
		}
	}
	log.Printf("moved the git backup state in %s to %s/<user>/%s", from, usersDir, syncDir)
	if err := os.Remove(from); err != nil {
		log.Printf("left %s: %v", from, err) // entries of no user
	}
	return nil
}
