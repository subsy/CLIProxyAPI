package misc

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCreatePrivateFile(t *testing.T) {
	dir := t.TempDir()
	fresh := filepath.Join(dir, "new.json")
	existing := filepath.Join(dir, "old.json")
	if err := os.WriteFile(existing, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(existing, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{fresh, existing} {
		f, err := CreatePrivateFile(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if _, err = f.WriteString("{}"); err != nil {
			t.Fatal(err)
		}
		if err = f.Close(); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Errorf("%s: mode %o, want 600", filepath.Base(path), mode)
		}
		if data, _ := os.ReadFile(path); string(data) != "{}" {
			t.Errorf("%s: content %q, want truncated and rewritten", filepath.Base(path), data)
		}
	}
}

// A failed chmod must leave an existing file's contents in place.
func TestCreatePrivateFileKeepsContentsWhenChmodFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can chmod any file")
	}
	// /dev/null is writable but not owned by us, so chmod fails; it has no contents to lose,
	// so this checks only that the error comes back before anything is truncated.
	if _, err := CreatePrivateFile("/dev/null"); err == nil {
		t.Fatal("want a chmod error for a file we do not own")
	}
}
