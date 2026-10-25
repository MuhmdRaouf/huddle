package domain

import (
	"fmt"
	"slices"
	"strconv"
)

// A lane's model fallback, as pure decisions over an epoch-ms `now`: three 5xx, 429 or dropped
// answers in a row for one model take it out for a minute, and its requests go to the next model
// of the lane's fallback list that is not out itself.

// FallbackFailures is how many failures in a row take a model out.
const FallbackFailures = 3

// FallbackWindowMs is how long a model stays out; the first request after it tries the model
// again, its count started over.
const FallbackWindowMs = 60_000

// Fallback is one lane's fallback list and each model's run of failures. The zero chain never
// switches. Not safe for concurrent use: the router holds it under its own lock.
type Fallback struct {
	chain  []string
	models map[string]modelRun
}

// modelRun is one model's failures in a row, the last failure's code, and when it is back.
type modelRun struct {
	fails     int
	code      string
	downUntil int64
}

// FallbackSwitch is one model taken out: which model, the one its requests go to now, and the
// failures that did it.
type FallbackSwitch struct {
	From  string
	To    string
	Count int
	Code  string
}

// String is the switch's log line: `fallback glm-5.3 -> glm-5.3-flash (3 x 503)`.
func (s FallbackSwitch) String() string {
	return fmt.Sprintf("fallback %s -> %s (%d x %s)", s.From, s.To, s.Count, s.Code)
}

// NewFallback is a lane's fallback over its list, in the order models fall back.
func NewFallback(chain []string) *Fallback {
	return &Fallback{chain: slices.Clone(chain), models: map[string]modelRun{}}
}

// FallbackFailure is whether an answer status counts as a failure: any 5xx, or a 429.
func FallbackFailure(status int) bool {
	return status == 429 || status >= 500
}

// Pick is the model a request for model goes to at now: model itself unless it is out, else the
// first model after it in the list (the whole list when it is not on it) that is not out; model
// when every one is.
func (f *Fallback) Pick(model string, now int64) string {
	if !f.out(model, now) {
		return model
	}
	start := slices.Index(f.chain, model) + 1
	for _, next := range f.chain[start:] {
		if next != model && !f.out(next, now) {
			return next
		}
	}
	return model
}

// Answered counts one answer for model: a 2xx starts its count over, a failure adds to it, any
// other status leaves it. It returns the switch when this answer took the model out and another
// model takes its requests.
func (f *Fallback) Answered(model string, status int, now int64) (FallbackSwitch, bool) {
	if status >= 200 && status < 300 {
		delete(f.models, model)
		return FallbackSwitch{}, false
	}
	if !FallbackFailure(status) {
		return FallbackSwitch{}, false
	}
	return f.fail(model, strconv.Itoa(status), now)
}

// Dropped counts one connection that failed before or while answering, by its errno code.
func (f *Fallback) Dropped(model, code string, now int64) (FallbackSwitch, bool) {
	return f.fail(model, code, now)
}

// fail adds one failure to model's run; the third takes it out and starts the run over.
func (f *Fallback) fail(model, code string, now int64) (FallbackSwitch, bool) {
	run := f.models[model]
	run.fails++
	run.code = code
	if run.fails < FallbackFailures {
		f.models[model] = run
		return FallbackSwitch{}, false
	}
	f.models[model] = modelRun{downUntil: now + FallbackWindowMs}
	next := f.Pick(model, now)
	if next == model {
		return FallbackSwitch{}, false
	}
	return FallbackSwitch{From: model, To: next, Count: run.fails, Code: code}, true
}

// out is whether model is taken out at now.
func (f *Fallback) out(model string, now int64) bool {
	return now < f.models[model].downUntil
}
