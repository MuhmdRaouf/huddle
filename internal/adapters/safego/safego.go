// Package safego starts goroutines that survive a panic: the hub is one process, and a panic in
// one background loop must not end it. A recovered panic is reported through the warn function
// and the goroutine ends; nothing restarts it.
package safego

import (
	"fmt"
	"os"
	"runtime/debug"
	"strings"
)

// warnStackLines is how many lines of the panicking goroutine's stack a warn line carries.
const warnStackLines = 12

// Go runs fn on a new goroutine. A panic inside fn is recovered and handed to warn as one line
// ("panic: <value>" and the first lines of the stack); a nil warn writes the line to stderr.
func Go(warn func(line string), fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				line := fmt.Sprintf("panic: %v\n%s", r, warnStack(debug.Stack()))
				if warn == nil {
					fmt.Fprintln(os.Stderr, line)
					return
				}
				warn(line)
			}
		}()
		fn()
	}()
}

// warnStack keeps a stack's first lines: the goroutine's state and the frames down to the
// panic, without the runtime's own.
func warnStack(stack []byte) string {
	lines := strings.Split(strings.TrimRight(string(stack), "\n"), "\n")
	if len(lines) > warnStackLines {
		lines = lines[:warnStackLines]
	}
	return strings.Join(lines, "\n")
}
