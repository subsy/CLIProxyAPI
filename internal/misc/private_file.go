package misc

import (
	"fmt"
	"os"
)

// CreatePrivateFile opens path for writing with mode 0600, creating it or emptying it. An
// existing file keeps its mode when opened, so its mode is changed to 0600 first and only then
// is it truncated: the old contents are never readable by others, and a failed chmod leaves
// them intact.
func CreatePrivateFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if errPrivate := MakePrivateAndTruncate(f); errPrivate != nil {
		if errClose := f.Close(); errClose != nil {
			return nil, fmt.Errorf("%s: %w (close: %v)", path, errPrivate, errClose)
		}
		return nil, fmt.Errorf("%s: %w", path, errPrivate)
	}
	return f, nil
}

// MakePrivateAndTruncate sets an open file's mode to 0600, then empties it.
func MakePrivateAndTruncate(f *os.File) error {
	if err := f.Chmod(0o600); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}
	if err := f.Truncate(0); err != nil {
		return fmt.Errorf("truncate: %w", err)
	}
	return nil
}
