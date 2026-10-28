// Package health holds the registry a process's parts report their last success and last error
// to, and the part list the /health answer carries.
package health

import (
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// Part is one component with its state, as /health answers it.
type Part struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	State     string `json:"state"`
	LastOKAt  int64  `json:"lastOkAt,omitempty"`
	LastErrAt int64  `json:"lastErrAt,omitempty"`
	LastError string `json:"lastError,omitempty"`
	Errors    int64  `json:"errors"`
}

// Registry holds what each component of one process last reported. Every method is safe on a nil
// *Registry, where it does nothing, so a caller never checks.
type Registry struct {
	mu    sync.Mutex
	now   func() time.Time
	parts map[string]*domain.HealthComponent
}

// New answers a registry that reads the moment from now.
func New(now func() time.Time) *Registry {
	return &Registry{now: now, parts: map[string]*domain.HealthComponent{}}
}

// Register names a component before its first report; registering twice keeps the first.
func (r *Registry) Register(name string, kind domain.HealthKind, every time.Duration) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.parts[name]; ok {
		return
	}
	r.parts[name] = &domain.HealthComponent{Name: name, Kind: kind, EveryMs: every.Milliseconds()}
}

// OK records a success now.
func (r *Registry) OK(name string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reported(name).LastOKAt = r.now().UnixMilli()
}

// Fail records an error now: the error count grows by one and the message is cut to
// HealthErrorMax bytes (cut on a UTF-8 boundary). A nil error records nothing.
func (r *Registry) Fail(name string, err error) {
	if r == nil || err == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	part := r.reported(name)
	part.LastErrAt = r.now().UnixMilli()
	part.LastError = clip(err.Error())
	part.Errors++
}

// Snapshot answers every component with its state at now, sorted by name.
func (r *Registry) Snapshot() []Part {
	if r == nil {
		return []Part{}
	}
	r.mu.Lock()
	now := r.now().UnixMilli()
	parts := make([]Part, 0, len(r.parts))
	for _, part := range r.parts {
		parts = append(parts, Part{
			Name:      part.Name,
			Kind:      string(part.Kind),
			State:     string(domain.HealthStateOf(*part, now)),
			LastOKAt:  part.LastOKAt,
			LastErrAt: part.LastErrAt,
			LastError: part.LastError,
			Errors:    part.Errors,
		})
	}
	r.mu.Unlock()
	slices.SortFunc(parts, func(a, b Part) int { return strings.Compare(a.Name, b.Name) })
	return parts
}

// Healthy says no component is failing or stale.
func (r *Registry) Healthy() bool {
	for _, part := range r.Snapshot() {
		if part.State == string(domain.HealthFailing) || part.State == string(domain.HealthStale) {
			return false
		}
	}
	return true
}

// reported answers the component's own record, registering it as a store with no period when
// nothing registered it first, so a forgotten Register still shows.
func (r *Registry) reported(name string) *domain.HealthComponent {
	part, ok := r.parts[name]
	if !ok {
		part = &domain.HealthComponent{Name: name, Kind: domain.HealthStore}
		r.parts[name] = part
	}
	return part
}

// clip cuts a message to HealthErrorMax bytes, backing off to a UTF-8 boundary.
func clip(msg string) string {
	if len(msg) <= domain.HealthErrorMax {
		return msg
	}
	cut := msg[:domain.HealthErrorMax]
	for len(cut) > 0 {
		r, size := utf8.DecodeLastRuneInString(cut)
		if r != utf8.RuneError || size != 1 {
			break
		}
		cut = cut[:len(cut)-1]
	}
	return cut
}
