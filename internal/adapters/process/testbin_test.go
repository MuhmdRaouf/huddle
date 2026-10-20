package process

import "testing"

func TestIsTestBinary(t *testing.T) {
	rows := []struct {
		path string
		want bool
	}{
		{"/tmp/go-build1/b001/huddle.test", true},
		{"cli.test", true},
		{"/Users/x/.agents/huddle-bin/src/0123456789ab/huddle", false},
		{"/usr/local/bin/huddle", false},
		{"/tmp/tests/huddle", false},
		{"", false},
	}
	for _, row := range rows {
		if got := IsTestBinary(row.path); got != row.want {
			t.Errorf("IsTestBinary(%q) = %v, want %v", row.path, got, row.want)
		}
	}
}
