package fsutil

import (
	"errors"
	"syscall"
)

// ErrnoCode returns a filesystem error's errno name in Node's spelling ("ENOENT"), for the
// errnos the core's messages print; empty when err is nil or carries none of them.
func ErrnoCode(err error) string {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return ""
	}
	switch errno {
	case syscall.ENOENT:
		return "ENOENT"
	case syscall.EEXIST:
		return "EEXIST"
	case syscall.EACCES:
		return "EACCES"
	case syscall.EPERM:
		return "EPERM"
	case syscall.EISDIR:
		return "EISDIR"
	case syscall.ENOTDIR:
		return "ENOTDIR"
	case syscall.ENOTEMPTY:
		return "ENOTEMPTY"
	case syscall.EMFILE:
		return "EMFILE"
	case syscall.ENFILE:
		return "ENFILE"
	case syscall.ENOSPC:
		return "ENOSPC"
	case syscall.EROFS:
		return "EROFS"
	case syscall.EBUSY:
		return "EBUSY"
	case syscall.ETXTBSY:
		return "ETXTBSY"
	case syscall.EXDEV:
		return "EXDEV"
	case syscall.ELOOP:
		return "ELOOP"
	case syscall.ENAMETOOLONG:
		return "ENAMETOOLONG"
	default:
		return ""
	}
}

// ErrorMessage returns the error's message, or empty for nil.
func ErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
