package adapters

import (
	"github.com/MuhmdRaouf/huddle/internal/core/adapters/fsutil"
)

// retryable reports whether retryBusy waits err out: EBUSY only. An access denied is a real
// permission error, so it never waits.
func retryable(err error) bool {
	return fsutil.ErrnoCode(err) == "EBUSY"
}
