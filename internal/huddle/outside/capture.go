package outside

// The capture switch: the settings file's "capture" member decides whether huddle-router keeps
// request and answer bodies. Capture stays on until the owner turns it off — a missing file, a
// missing member or anything but a JSON false reads on.

import (
	"path/filepath"
)

var (
	captureSetting = `"capture"`
)

// Capture is the hub's side of the capture switch over one data dir's settings file: the hub
// reads it to answer the dashboard and writes it when the owner flips the switch.
type Capture struct {
	dir string
}

// NewCapture is the capture switch over one data dir.
func NewCapture(dir string) *Capture {
	return &Capture{dir: dir}
}

// On reads the switch. Everything but a JSON false reads on.
func (c *Capture) On() bool {
	on := true
	for _, setting := range readSettings(c.file()) {
		if setting.key == captureSetting && setting.raw == "false" {
			on = false
		}
	}
	return on
}

// Set writes the switch, keeping the file's other members, and answers the new value.
func (c *Capture) Set(on bool) bool {
	value := "false"
	if on {
		value = "true"
	}
	writeSettings(c.dir, c.file(), putMember(readSettings(c.file()), captureSetting, value))
	return on
}

// CaptureOn reads the switch of one data dir, for a process holding no Capture — the router
// reads it at start and on every poll tick.
func CaptureOn(dir string) bool {
	return NewCapture(dir).On()
}

func (c *Capture) file() string { return filepath.Join(c.dir, "settings.json") }
