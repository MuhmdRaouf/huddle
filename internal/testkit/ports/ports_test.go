package ports

import (
	"fmt"
	"net"
	"testing"
)

// Free answers 200 calls 200 distinct ports in its range, each one a listener can take.
func TestFreeAnswersDistinctBindablePorts(t *testing.T) {
	const draws = 200
	seen := make(map[int]bool, draws)
	for i := 0; i < draws; i++ {
		port := Free(t)
		if port < portFirst || port > portLast {
			t.Fatalf("Free() = %d, want a port in %d-%d", port, portFirst, portLast)
		}
		if seen[port] {
			t.Fatalf("Free() answered %d twice in %d draws", port, i+1)
		}
		seen[port] = true
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			t.Fatalf("bind %d: %v", port, err)
		}
		_ = listener.Close()
	}
}
