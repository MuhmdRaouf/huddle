// fakelegacy stands in for a 0.0.1 per-provider router in the tests that stop one: its argv
// names it the way `plugin-core zai router run` does, so `<name> router` is in the command line
// a pid check reads. With a port it holds that port; without one it only waits. The `spawn`
// word makes it grow a worker child (`<self> <name> router worker`) first, the way a
// hand-restarted router grows one.
package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"time"
)

func main() {
	// argv: <name> router <run|watch> [port] ["spawn"] — the way a 0.0.1
	// `plugin-core zai router run` names itself, the port and the spawn word anywhere after.
	port := 0
	spawn := false
	for _, arg := range os.Args[1:] {
		if arg == "spawn" {
			spawn = true
			continue
		}
		number, err := strconv.Atoi(arg)
		if err == nil && number >= 1 && number <= 65535 {
			port = number
		}
	}
	if spawn && len(os.Args) > 1 {
		if self, err := os.Executable(); err == nil {
			child := exec.Command(self, os.Args[1], "router", "worker")
			if err := child.Start(); err == nil {
				fmt.Fprintf(os.Stderr, "worker %d\n", child.Process.Pid)
			}
		}
	}
	if port != 0 {
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			fmt.Fprintln(os.Stderr, "fakelegacy:", err)
			os.Exit(1)
		}
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}
	for {
		time.Sleep(time.Hour)
	}
}
