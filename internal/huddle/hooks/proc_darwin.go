// The session walk's kernel readers on macOS: one sysctl per answer, no process started.

package hooks

import (
	"encoding/binary"
	"strings"

	"golang.org/x/sys/unix"
)

// procCommandOf answers a pid's command line the way the walk reads it: the argc arguments
// kern.procargs2 keeps, joined with one space. The buffer carries a native-endian int32 argc,
// the executable path, the NUL padding behind it, then argc NUL-separated arguments; the
// environment follows, which the walk never reads. "" and false when the kernel does not
// answer (a pid gone, or one of another user).
func procCommandOf(pid int) (string, bool) {
	raw, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(raw) < 4 {
		return "", false
	}
	argc := int(int32(binary.NativeEndian.Uint32(raw[:4])))
	if argc <= 0 {
		return "", false
	}
	// the executable path ends at its first NUL; the padding behind it leads to argv[0]
	at := 4
	for at < len(raw) && raw[at] != 0 {
		at++
	}
	for at < len(raw) && raw[at] == 0 {
		at++
	}
	words := make([]string, 0, min(argc, len(raw)))
	for range argc {
		if at >= len(raw) {
			break
		}
		end := at
		for end < len(raw) && raw[end] != 0 {
			end++
		}
		words = append(words, string(raw[at:end]))
		at = end + 1
	}
	if len(words) == 0 {
		return "", false
	}
	return strings.Join(words, " "), true
}

// procParentOf answers a pid's parent from its kern.proc.pid entry. 0 and false when the kernel
// does not answer: a pid gone answers an empty entry, which reads as no parent.
func procParentOf(pid int) (int, bool) {
	if pid <= 0 {
		return 0, false
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || info.Proc.P_pid != int32(pid) {
		return 0, false
	}
	parent := int(info.Eproc.Ppid)
	if parent <= 0 {
		return 0, false
	}
	return parent, true
}
