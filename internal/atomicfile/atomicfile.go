// Package atomicfile replaces files so that a crash or power loss leaves
// either the old or the new content, never a truncated file.
package atomicfile

import (
	"os"
	"path/filepath"
)

// WriteFile replaces path with data, mode 0600: it writes a temporary file in
// the same directory, flushes it to disk, renames it over path and flushes
// the directory, which holds the rename.
func WriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp") // mode 0600
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
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return syncDir(dir)
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
