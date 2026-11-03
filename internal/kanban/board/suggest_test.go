package board

import "testing"

// The suggestion is the board a branch names: the whole branch, else its last path segment.
func TestSuggestBoard(t *testing.T) {
	boards := []string{"login", "billing", "feat"}
	tests := []struct {
		name   string
		branch string
		want   string
		ok     bool
	}{
		{"the last segment of a feature branch", "feat/login", "login", true},
		{"a branch that is a board", "billing", "billing", true},
		{"a deeper branch", "raouf/feat/billing", "billing", true},
		{"case and spacing fold", "Feat/Login", "login", true},
		{"no board matches", "fix/typo", "", false},
		{"no branch", "", "", false},
	}
	for _, tc := range tests {
		got, ok := SuggestBoard(tc.branch, boards)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: SuggestBoard(%q) = %q, %v, want %q, %v", tc.name, tc.branch, got, ok, tc.want, tc.ok)
		}
	}
}
