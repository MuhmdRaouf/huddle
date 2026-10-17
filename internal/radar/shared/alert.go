package shared

import (
	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// AlertKind is which detector fired.
type AlertKind string

const (
	AlertStuck      AlertKind = "stuck"
	AlertLoop       AlertKind = "loop"
	AlertRetryStorm AlertKind = "retry_storm"
	AlertBudget     AlertKind = "budget"
	AlertContext    AlertKind = "context"
)

// AlertSeverity is how loud the dashboard shows an alert.
type AlertSeverity string

const (
	SeverityErr  AlertSeverity = "err"
	SeverityWarn AlertSeverity = "warn"
)

// Alert is one thing worth the user's attention right now (GET /api/alerts). Its id is
// stable for one occurrence, so a dismissal holds until the same thing happens again.
type Alert struct {
	ID        string        `json:"id"`
	Kind      AlertKind     `json:"kind"`
	SessionID string        `json:"sessionId"`
	AgentID   *string       `json:"agentId"`
	Project   string        `json:"project"`
	Since     int64         `json:"since"`
	Detail    string        `json:"detail"`
	CostUsd   *USD          `json:"costUsd"`
	Severity  AlertSeverity `json:"severity"`
}

// MarshalJSON writes the alert in the wire's fixed key order, so the bytes never move: the
// session-scoped literals put kind first and costUsd before id, the budget literal puts id
// first and costUsd after detail.
func (a Alert) MarshalJSON() ([]byte, error) {
	if a.Kind == AlertBudget {
		return domain.MarshalCompactJSON(budgetAlertOrder(a))
	}
	return domain.MarshalCompactJSON(sessionAlertOrder{
		Kind:      a.Kind,
		SessionID: a.SessionID,
		AgentID:   a.AgentID,
		Project:   a.Project,
		CostUsd:   a.CostUsd,
		ID:        a.ID,
		Since:     a.Since,
		Detail:    a.Detail,
		Severity:  a.Severity,
	})
}

// sessionAlertOrder is the sessionAlert literal's key order: every session-scoped alert.
type sessionAlertOrder struct {
	Kind      AlertKind     `json:"kind"`
	SessionID string        `json:"sessionId"`
	AgentID   *string       `json:"agentId"`
	Project   string        `json:"project"`
	CostUsd   *USD          `json:"costUsd"`
	ID        string        `json:"id"`
	Since     int64         `json:"since"`
	Detail    string        `json:"detail"`
	Severity  AlertSeverity `json:"severity"`
}

// budgetAlertOrder is the budget literal's key order.
type budgetAlertOrder struct {
	ID        string        `json:"id"`
	Kind      AlertKind     `json:"kind"`
	SessionID string        `json:"sessionId"`
	AgentID   *string       `json:"agentId"`
	Project   string        `json:"project"`
	Since     int64         `json:"since"`
	Detail    string        `json:"detail"`
	CostUsd   *USD          `json:"costUsd"`
	Severity  AlertSeverity `json:"severity"`
}
