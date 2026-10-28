package domain

// HealthKind says what a component is: a store the process opened, or a loop it runs.
type HealthKind string

const (
	HealthStore HealthKind = "store"
	HealthLoop  HealthKind = "loop"
)

// HealthState is a component's state at one moment.
type HealthState string

const (
	HealthOK      HealthState = "ok"
	HealthFailing HealthState = "failing"
	HealthStale   HealthState = "stale"
	HealthUnknown HealthState = "unknown"
)

// HealthComponent is what one component last reported. Times are Unix milliseconds; 0 means never.
type HealthComponent struct {
	Name      string
	Kind      HealthKind
	EveryMs   int64 // a loop's period; 0 for a store
	LastOKAt  int64
	LastErrAt int64
	LastError string
	Errors    int64
}

// HealthErrorMax is the most bytes of an error a component keeps.
const HealthErrorMax = 200

// HealthStateOf answers a component's state at now: failing while the last error is the latest
// news, stale once a loop has missed three periods, ok on any success, unknown before the first
// report.
func HealthStateOf(c HealthComponent, now int64) HealthState {
	if c.LastErrAt > c.LastOKAt {
		return HealthFailing
	}
	if c.Kind == HealthLoop && c.EveryMs > 0 && c.LastOKAt > 0 && now-c.LastOKAt > 3*c.EveryMs {
		return HealthStale
	}
	if c.LastOKAt > 0 {
		return HealthOK
	}
	return HealthUnknown
}
