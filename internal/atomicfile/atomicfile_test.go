package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.md")
	for _, data := range []string{"one", "two"} {
		if err := WriteFile(path, []byte(data)); err != nil {
			t.Fatal(err)
		}
		if got, err := os.ReadFile(path); err != nil || string(got) != data {
			t.Fatalf("read %q, %v; want %q", got, err, data)
		}
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("temporary files left: %v", entries)
	}
	if err := WriteFile(filepath.Join(dir, "missing", "a.md"), nil); err == nil {
		t.Error("wrote into a missing directory")
	}
}
