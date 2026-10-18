package ticket

import "strings"

// Badge is one derived marker of a card; the UI colours them and the CLI list
// prints them. The json names are the wire the web pages read.
type Badge struct {
	Kind  string `json:"kind"` // expedite, priority, type, est, blocked, holder, waits, hand, incomplete
	Label string `json:"label"`
}

// Badges derives a ticket's badges in display order: expedite, priority, type,
// estimate, blocked, holder, one waits-on per open dependency, moved by hand,
// incomplete. depState answers a dependency's column, "missing" or "text".
func Badges(t Ticket, column string, depState func(id string) string, problems []Problem) []Badge {
	var badges []Badge
	if t.Expedite {
		badges = append(badges, Badge{Kind: "expedite", Label: "EXPEDITE"})
	}
	if t.Priority != "" {
		badges = append(badges, Badge{Kind: "priority", Label: t.Priority})
	}
	if t.Type != "" {
		badges = append(badges, Badge{Kind: "type", Label: t.Type})
	}
	if t.Est != "" {
		badges = append(badges, Badge{Kind: "est", Label: t.Est})
	}
	if column == "blocked" || strings.TrimSpace(t.Fields["blocked_reason"]) != "" {
		badges = append(badges, Badge{Kind: "blocked", Label: "BLOCKED"})
	}
	if holder := strings.TrimSpace(t.Fields["claimed_by"]); holder != "" {
		badges = append(badges, Badge{Kind: "holder", Label: "@" + holder})
	}
	for _, dep := range t.DependsOn {
		if !idRe.MatchString(dep) {
			continue
		}
		state := "missing"
		if depState != nil {
			state = depState(dep)
		}
		if state != "done" {
			badges = append(badges, Badge{Kind: "waits", Label: "waits on " + dep})
		}
	}
	if strings.TrimSpace(t.Fields["moved_by_hand"]) != "" {
		badges = append(badges, Badge{Kind: "hand", Label: "moved by hand"})
	}
	for _, p := range problems {
		if p.Severity == "failure" {
			badges = append(badges, Badge{Kind: "incomplete", Label: "incomplete"})
			break
		}
	}
	return badges
}
