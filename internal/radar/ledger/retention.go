package ledger

import "context"

// RetentionSource answers how many days ledger rows live, read fresh on every prune; 0 or
// less keeps them forever. The caller wires the history retention setting in, so this package
// never reads the settings file.
type RetentionSource func(ctx context.Context) int
