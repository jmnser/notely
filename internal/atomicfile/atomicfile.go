// Package atomicfile replaces files so that a crash or power loss leaves
// either the old or the new content, never a truncated file.
package atomicfile

import (
	"crypto/rand"
	"os"
	"path"
	"path/filepath"
)

// WriteFile replaces file with data, mode 0600: it writes a temporary file in
// the same directory, flushes it to disk, renames it over file and flushes
// the directory, which holds the rename.
func WriteFile(file string, data []byte) error {
	dir := filepath.Dir(file)
	f, err := os.CreateTemp(dir, "."+filepath.Base(file)+".*.tmp") // mode 0600
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, file)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return syncDir(dir)
}

// WriteFileIn is WriteFile for name, slash-separated, in root, which no path
// can leave. The temporary file is a dot file with a random name, so
// concurrent writes of the same name do not mix.
func WriteFileIn(root *os.Root, name string, data []byte) error {
	tmp := path.Join(path.Dir(name), "."+path.Base(name)+"."+rand.Text()+".tmp")
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = root.Rename(tmp, name)
	}
	if err != nil {
		_ = root.Remove(tmp)
		return err
	}
	d, err := root.Open(path.Dir(name))
	if err != nil {
		return nil //nolint:nilerr // best effort, see syncDir
	}
	defer d.Close() //nolint:errcheck
	if err := d.Sync(); err != nil && !isUnsupported(err) {
		return err
	}
	return nil
}

// syncDir flushes a directory's entries. Windows cannot open directories for
// that and needs no flush, as it writes renames through.
func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // the caller's directory
	if err != nil {
		return nil //nolint:nilerr // best effort, see above
	}
	defer d.Close() //nolint:errcheck
	if err := d.Sync(); err != nil && !isUnsupported(err) {
		return err
	}
	return nil
}
