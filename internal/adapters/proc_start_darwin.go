package adapters

import (
	"syscall"
	"unsafe"
)

// ProcStartTime is when pid began, in unix milliseconds, from the kern.proc.pid sysctl's
// p_starttime: the raw __sysctl call answers 648 bytes of kinfo_proc whose first fields are the
// start time (seconds at 0, microseconds at 8, identical on both darwin architectures). A pid
// with no process answers an empty buffer and no error, so the length check is the liveness
// test. false when the sysctl cannot tell.
func ProcStartTime(pid int64) (int64, bool) {
	// The name's mib — CTL_KERN (1), KERN_PROC (14), KERN_PROC_PID (1) — and the syscall number
	// are the only pieces stdlib syscall lacks words for.
	mib := [4]int32{1, 14, 1, int32(pid)}
	var buf [648]byte
	n := uintptr(len(buf))
	_, _, errno := syscall.Syscall6(syscall.SYS___SYSCTL,
		uintptr(unsafe.Pointer(&mib[0])), uintptr(len(mib)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)), 0, 0)
	if errno != 0 || n != uintptr(len(buf)) {
		return 0, false
	}
	sec := *(*int64)(unsafe.Pointer(&buf[0]))
	usec := int64(*(*int32)(unsafe.Pointer(&buf[8])))
	return sec*1000 + usec/1000, true
}
