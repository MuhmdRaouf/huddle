package channel

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// PruneEvents cuts every listed channel back to the events' retention and answers how many
// events went across them all. A channel that fails to open or to prune is skipped: its
// failure joins the returned error and the rest still prune.
func (h *Hub) PruneEvents(ctx context.Context, now time.Time) (int, error) {
	names, err := h.Names(ctx)
	if err != nil {
		return 0, err
	}
	total := 0
	var failures []error
	for _, name := range names {
		ch, err := h.GetExisting(ctx, name)
		if err == nil {
			var n int
			n, err = ch.Store.PruneEvents(ctx, now)
			total += n
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("prune the events of %s: %w", name, err))
		}
	}
	return total, errors.Join(failures...)
}
