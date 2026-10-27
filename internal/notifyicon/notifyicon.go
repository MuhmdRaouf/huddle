// The huddle icon the desktop notifiers show: the dashboard's icon PNG, written once under
// the user's ~/.agents/huddle, so a notify-send command can point at it with a file:// URI.
package notifyicon

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/MuhmdRaouf/huddle/internal/huddle/web"
)

// Dir is the folder under home the icon lives in.
func Dir(home string) string {
	return filepath.Join(home, ".agents", "huddle")
}

// PNGPath is the icon's file under home.
func PNGPath(home string) string {
	return filepath.Join(Dir(home), "huddle.png")
}

// EnsurePNG writes the dashboard's icon under home when the file is missing or holds other
// bytes, and answers its path. An empty home is an error; nothing is written then.
func EnsurePNG(home string) (string, error) {
	if home == "" {
		return "", errors.New("notifyicon: no home directory")
	}
	want, err := fs.ReadFile(web.FS(), "icons/icon-192.png")
	if err != nil {
		return "", fmt.Errorf("read the built dashboard's icon: %w", err)
	}
	path := PNGPath(home)
	if got, err := os.ReadFile(path); err == nil && bytes.Equal(got, want) {
		return path, nil
	}
	if err := os.MkdirAll(Dir(home), 0o700); err != nil {
		return "", err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, want, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return path, nil
}
