// The pid record and the retire flow the starter runs through: cases, inputs and expected
// outputs.
package router

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

func TestPidFile(t *testing.T) {
	t.Parallel()
	t.Run("round-trips 0600 and refuses anything else", func(t *testing.T) {
		root := t.TempDir()
		if _, ok := ReadPidFile(context.Background(), root); ok {
			t.Fatal("readPidFile(empty root) found a record")
		}
		record := PidRecord{
			Pid:          1,
			Port:         2,
			StartedAt:    "t",
			Node:         "/node",
			Version:      "v",
			FrontVersion: 2,
			Mode:         PidModeRouter,
			Token:        "x",
		}
		if err := writeTestPidRecord(root, record); err != nil {
			t.Fatal(err)
		}
		read, ok := ReadPidFile(context.Background(), root)
		if !ok || read != record {
			t.Fatalf("readPidFile = %+v (found %v), want %+v", read, ok, record)
		}
		wantPrivateFile(t, domain.NewStateLayout(root).RouterPid)
		for _, text := range []string{`{"pid":"one"}`, "null"} {
			if err := os.WriteFile(domain.NewStateLayout(root).RouterPid, []byte(text), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, ok := ReadPidFile(context.Background(), root); ok {
				t.Fatalf("readPidFile accepted %q", text)
			}
		}
		RemovePidFile(root)
		if _, err := os.Stat(domain.NewStateLayout(root).RouterPid); !os.IsNotExist(err) {
			t.Fatalf("the pid file survived its removal: %v", err)
		}
	})

	t.Run("moves the running record aside as the retired one, and drops it again", func(t *testing.T) {
		root := t.TempDir()
		if _, ok := ReadRetiredFile(context.Background(), root); ok {
			t.Fatal("readRetiredFile(empty root) found a record")
		}
		record := PidRecord{
			Pid:          9,
			Port:         18787,
			StartedAt:    "t",
			Node:         "/node",
			Version:      "v",
			FrontVersion: 2,
			Mode:         PidModeEmergency,
			Token:        "tok",
		}
		if err := writeTestPidRecord(root, record); err != nil {
			t.Fatal(err)
		}
		if err := RetirePidFile(context.Background(), root, record); err != nil {
			t.Fatal(err)
		}
		retired, ok := ReadRetiredFile(context.Background(), root)
		if !ok || retired != record {
			t.Fatalf("readRetiredFile = %+v (found %v), want %+v", retired, ok, record)
		}
		if _, ok := ReadPidFile(context.Background(), root); ok {
			t.Fatal("the running record survived its retirement")
		}
		RemoveRetiredFile(root)
		if _, ok := ReadRetiredFile(context.Background(), root); ok {
			t.Fatal("the retired record survived its removal")
		}
		// A retired file that cannot be written (a directory sits there) is an error, not a silent
		// loss of the running record.
		if err := os.MkdirAll(domain.NewStateLayout(root).RouterRetired, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := RetirePidFile(context.Background(), root, record); err == nil {
			t.Fatal("retiring over a directory reported no error")
		}
	})
}

// writeTestPidRecord lays a pid record down the way the retired starter wrote it, for the tests
// of the reads and the retire that follow it.
func writeTestPidRecord(root string, record PidRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return os.WriteFile(domain.NewStateLayout(root).RouterPid, data, 0o600)
}
