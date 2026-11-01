package server

import (
	"io"
	"sync"
)

// lockedWriter lets several goroutines share one writer: the server's own lines and its parts'
// logs (the Radar tab's watcher among them) write from goroutines of their own, and the writer
// a caller hands Serve need not be safe for that.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
