package misc

import (
	"fmt"
	"os"
)

// CreatePrivateFile creates or truncates path for writing with mode 0600. An existing file
// keeps its mode when opened, so it is also changed to 0600: credential files must never be
// readable by other users.
func CreatePrivateFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	if errChmod := f.Chmod(0o600); errChmod != nil {
		if errClose := f.Close(); errClose != nil {
			return nil, fmt.Errorf("chmod %s: %w (close: %v)", path, errChmod, errClose)
		}
		return nil, fmt.Errorf("chmod %s: %w", path, errChmod)
	}
	return f, nil
}
