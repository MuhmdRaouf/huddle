// A lane's model fallback on the request path: the decision is domain.Fallback's, held under
// the lane's own lock. A lane without a fallback list takes no lock and does no work.
package router

import (
	"sync"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// laneFallback is one lane's fallback state; nil for a lane without a list.
type laneFallback struct {
	mu       sync.Mutex
	decision *domain.Fallback
}

// newLaneFallback is the state over the lane's list; nil when the list is empty.
func newLaneFallback(chain []string) *laneFallback {
	if len(chain) == 0 {
		return nil
	}
	return &laneFallback{decision: domain.NewFallback(chain)}
}

// pick is the model a request for model goes to now.
func (f *laneFallback) pick(model string) string {
	if f == nil {
		return model
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.decision.Pick(model, time.Now().UnixMilli())
}

// answered counts one upstream answer for model and returns the switch it caused, if any.
func (f *laneFallback) answered(model string, status int) (domain.FallbackSwitch, bool) {
	if f == nil {
		return domain.FallbackSwitch{}, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.decision.Answered(model, status, time.Now().UnixMilli())
}

// dropped counts one failed upstream connection for model and returns the switch it caused, if
// any.
func (f *laneFallback) dropped(model, code string) (domain.FallbackSwitch, bool) {
	if f == nil {
		return domain.FallbackSwitch{}, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.decision.Dropped(model, code, time.Now().UnixMilli())
}

// fellBack logs a switch when there was one.
func (w *routerWorker) fellBack(change domain.FallbackSwitch, switched bool) {
	if switched {
		w.log(change.String())
	}
}
