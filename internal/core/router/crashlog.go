// The router's own records: the rotating `router.log` (a fresh file a day, the last few kept).
// Never a header or a body. Every write is best effort: a full disk, a missing directory or a
// read-only file costs the line, never the request.
package router

import (
	"errors"
	"os"
	"sync"
	"time"
)

// LogFs is the file operations the logs use; tests inject failing ones to prove nothing propagates.
// Each operation reports its own failure instead of throwing: the logs swallow them all the same.
type LogFs interface {
	Append(path string, text string) error
	Size(path string) (int64, error)
	Read(path string) ([]byte, error)
	Write(path string, data []byte) error
	Rename(from string, to string) error
}

// DiskFs is the file operations on the real disk. It appends only where the directory already
// exists: a router whose state was removed (an uninstall) must not bring the directory back by
// logging.
var DiskFs LogFs = diskFs{}

type diskFs struct{}

func (diskFs) Append(path, text string) error {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = file.WriteString(text)
	return errors.Join(err, file.Close())
}

func (diskFs) Size(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func (diskFs) Read(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (diskFs) Write(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	return errors.Join(err, file.Close())
}

func (diskFs) Rename(from, to string) error {
	return os.Rename(from, to)
}

// isoNow is new Date().toISOString(): UTC, millisecond precision.
func isoNow() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// RouterLogLimit is the rotating router log's size limit.
const RouterLogLimit int64 = 4 * 1024 * 1024

// RotatingLog is a line logger appending to path, rotated to `<path>.1` once it passes limit bytes.
// Never reports a failure: a full disk costs the line, nothing else. A nil fs is DiskFs and a zero
// limit is RouterLogLimit.
func RotatingLog(path string, fs LogFs, limit int64) func(line string) {
	if fs == nil {
		fs = DiskFs
	}
	if limit == 0 {
		limit = RouterLogLimit
	}
	written := int64(0)
	if size, err := fs.Size(path); err == nil {
		written = size
	}
	var mu sync.Mutex
	return func(line string) {
		mu.Lock()
		defer mu.Unlock()
		text := isoNow() + " " + line + "\n"
		// The trim counts the file's bytes against the text's UTF-16 length; ASCII logs never
		// differ.
		if written+utf16Len(text) > limit {
			_ = fs.Rename(path, path+".1")
			written = 0
		}
		_ = fs.Append(path, text)
		written += utf16Len(text)
	}
}
