package routerd

import (
	"os"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/core/router"
)

// TestTheRecordLandsBeforeTheFirstHealthAnswer pins the start order a `huddle router start`
// and `huddle status` rely on: a daemon that answers its health path has published its pid, so
// a caller that saw it healthy never reads an empty run dir.
func TestTheRecordLandsBeforeTheFirstHealthAnswer(t *testing.T) {
	t.Parallel()
	rig := newDaemonRig(t)
	if err := router.SetEnabled(rig.env, "zai", true); err != nil {
		t.Fatal(err)
	}
	rig.start()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, ok := rig.health(t); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the daemon never answered its health path; its log:\n%s", rig.logs.joined())
		}
		time.Sleep(5 * time.Millisecond)
	}
	published, ok := readRecord(rig.env)
	if !ok {
		t.Fatal("the daemon answered its health path before publishing its record")
	}
	if published.PID != os.Getpid() || published.Port != rig.port {
		t.Fatalf("the record = %+v, want pid %d on port %d", published, os.Getpid(), rig.port)
	}
}
