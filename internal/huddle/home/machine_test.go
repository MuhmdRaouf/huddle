package home

import (
	"regexp"
	"testing"
)

// TestFitMachineNameAlwaysPassesTheDeviceNameRule pins the default machine name to the rule
// join-hub and pairing check (auth.DeviceNameRe): a long or odd hostname still joins without
// --name.
func TestFitMachineNameAlwaysPassesTheDeviceNameRule(t *testing.T) {
	rule := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _-]{0,31}$`)
	cases := map[string]string{
		"studio.local": "studio",
		"iad20-gt1025-8c1f7bb3-9e1a-4a21-a46d-d806aabd0e21-6ACBB61B3737": "iad20-gt1025-8c1f7bb3-9e1a-4a21-",
		"_build+box": "build-box",
		"":           "localhost",
		".":          "localhost",
		"--":         "localhost",
		"mac mini":   "mac mini",
	}
	for host, want := range cases {
		got := fitMachineName(host)
		if got != want {
			t.Errorf("fitMachineName(%q) = %q, want %q", host, got, want)
		}
		if !rule.MatchString(got) {
			t.Errorf("fitMachineName(%q) = %q fails the device-name rule", host, got)
		}
	}
}
