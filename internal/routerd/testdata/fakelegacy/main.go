// fakelegacy stands in for a 0.0.1 per-provider router in the tests that stop one: its argv
// names it the way `plugin-core zai router run` does, so `<name> router` is in the command line
// a pid check reads. With a port it holds that port; without one it only waits.
package main

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"time"
)

func main() {
	// argv: <name> router <run|watch> [port] — the way a 0.0.1 `plugin-core zai router run`
	// names itself, the port last when it is there.
	port := 0
	for _, arg := range os.Args[1:] {
		number, err := strconv.Atoi(arg)
		if err == nil && number >= 1 && number <= 65535 {
			port = number
			break
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
