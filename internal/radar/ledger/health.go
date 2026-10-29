package ledger

// The name the ledger package reports its loop under.
const PruneHealthName = "radar.ledger-prune"

// HealthReport takes a loop's result: OK after a good pass, Fail after a failed one.
type HealthReport interface {
	OK(name string)
	Fail(name string, err error)
}

// reportTo hands one pass's result to health; a nil health reports nothing, a nil err is a good pass.
func reportTo(health HealthReport, name string, err error) {
	if health == nil {
		return
	}
	if err != nil {
		health.Fail(name, err)
		return
	}
	health.OK(name)
}
