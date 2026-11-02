package ledger

import "testing"

// BodyDays turns the dashboard's history retention into the days a stored body lives: the
// shorter of the setting and the 30-day body ceiling. A setting of 0 keeps the ledger rows
// forever and holds bodies 30 days all the same.
func TestBodyDays(t *testing.T) {
	t.Parallel()
	for retention, want := range map[int]int{
		7:  7,
		30: 30,
		90: 30,
		0:  30,
	} {
		if got := BodyDays(retention); got != want {
			t.Errorf("BodyDays(%d) = %d, want %d", retention, got, want)
		}
	}
}
