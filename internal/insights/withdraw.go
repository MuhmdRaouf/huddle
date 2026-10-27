// Which tips give way to which alerts: a tip is the quiet early form of the alert that covers
// it, so both firing reads as the same thing said twice.
package insights

// coveredBy maps a tip's rule to the alert kind that covers it, using radar's own kind strings
// (internal/radar/shared). subagent_silent has no cover: its stuck alert fires at 10 minutes,
// before the tip's 20 minutes, so the tip is the only subagent-specific notice and never
// competes with an alert.
var coveredBy = map[string]string{
	RuleToolFailing:       "loop",
	RuleSameCommand:       "loop",
	RuleContextCheckpoint: "context",
}

// withdrawn answers the tips no active alert covers, in order; nil when none survive. A tip
// with no session is about the whole huddle and no alert answers for it, so it stays.
func withdrawn(tips []Tip, alerts []ActiveAlert) []Tip {
	var kept []Tip
	for _, tip := range tips {
		kind, covered := coveredBy[tip.Rule]
		if !covered || tip.Session == "" || !hasAlert(alerts, tip.Session, kind) {
			kept = append(kept, tip)
		}
	}
	return kept
}

// hasAlert is whether one active alert of the kind is live for the session.
func hasAlert(alerts []ActiveAlert, session, kind string) bool {
	for _, alert := range alerts {
		if alert.Session == session && alert.Kind == kind {
			return true
		}
	}
	return false
}
