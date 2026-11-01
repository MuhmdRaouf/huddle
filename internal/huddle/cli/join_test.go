package cli

import "testing"

// TestJoinShapeOfReadsTheCommandLine pins the three forms of the join word: an invite keeps its
// address and token, one channel name is a channel, and everything else is the join operation.
func TestJoinShapeOfReadsTheCommandLine(t *testing.T) {
	cases := []struct {
		name string
		line []string
		want joinShape
	}{
		{"a host and port with a token", []string{"127.0.0.1:4835", "--token", "x"}, joinInvite},
		{"a token alone", []string{"--token", "x"}, joinInvite},
		{"a host and port", []string{"hub.example:8443"}, joinInvite},
		{"a channel name", []string{"shop"}, joinChannel},
		{"a channel name with a dash and a digit", []string{"proj-2"}, joinChannel},
		{"no words", nil, joinOperation},
		{"flags only", []string{"--fresh"}, joinOperation},
		{"a task of several words", []string{"fixing", "the", "build"}, joinOperation},
		{"a word that is no channel name", []string{"Fixing_It"}, joinOperation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opt, pos := parseFlags(tc.line)
			c := &cli{raw: opt, pos: pos}
			if got := c.joinShapeOf(); got != tc.want {
				t.Errorf("joinShapeOf(%v) = %d, want %d", tc.line, got, tc.want)
			}
		})
	}
}
