package ledger

import "context"

// RetentionSource answers how many days ledger rows live, read fresh on every prune; 0 or
// less keeps them forever. The caller wires the history retention setting in, so this package
// never reads the settings file.
type RetentionSource func(ctx context.Context) int

// bodyCeilingDays is the longest a stored body lives, whatever the retention says.
const bodyCeilingDays = 30

// BodyDays answers the days a stored body lives for a dashboard retention setting: the
// shorter of the setting and the 30-day body ceiling. A setting of 0 keeps the ledger rows
// forever and holds bodies 30 days all the same.
func BodyDays(retention int) int {
	if retention <= 0 || retention > bodyCeilingDays {
		return bodyCeilingDays
	}
	return retention
}
