package outside

// The capture switch: the settings file's "capture" member decides whether the router keeps
// request bodies. A missing file, a missing member or anything but JSON false reads on, a Set
// keeps the file's other members, and CaptureOn agrees with On.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCapture(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	capture := NewCapture(dir)

	t.Run("a home with no settings file reads on", func(t *testing.T) {
		if !capture.On() {
			t.Error("capture reads off in a fresh home")
		}
		if !CaptureOn(dir) {
			t.Error("CaptureOn disagrees with On in a fresh home")
		}
	})

	t.Run("only a JSON false reads off", func(t *testing.T) {
		for _, test := range []struct {
			file string
			want bool
		}{
			{`{"capture": false}`, false},
			{`{"zone": "Asia/Tokyo", "notify": true, "capture": false}`, false},
			{`{"capture": true}`, true},
			{`{"capture": "no"}`, true},
			{`{"notify": false}`, true},
		} {
			if err := os.WriteFile(settings, []byte(test.file), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := capture.On(); got != test.want {
				t.Errorf("On() over %s = %v, want %v", test.file, got, test.want)
			}
			if got := CaptureOn(dir); got != test.want {
				t.Errorf("CaptureOn over %s = %v, want %v", test.file, got, test.want)
			}
		}
	})

	t.Run("Set keeps the file's other members", func(t *testing.T) {
		before := "{\n  \"zone\": \"Asia/Tokyo\",\n  \"notify\": false\n}\n"
		if err := os.WriteFile(settings, []byte(before), 0o600); err != nil {
			t.Fatal(err)
		}
		if capture.Set(false) {
			t.Error("Set(false) answered true")
		}
		raw, err := os.ReadFile(settings)
		if err != nil {
			t.Fatal(err)
		}
		file := string(raw)
		for _, member := range []string{`"capture"`, `"notify"`, `"zone"`, "Asia/Tokyo"} {
			if !strings.Contains(file, member) {
				t.Errorf("after Set(false) the file lost %s:\n%s", member, file)
			}
		}
		if capture.On() || CaptureOn(dir) {
			t.Error("capture reads on after Set(false)")
		}
		if !capture.Set(true) {
			t.Error("Set(true) answered false")
		}
		if !capture.On() || !CaptureOn(dir) {
			t.Error("capture reads off after Set(true)")
		}
	})
}
