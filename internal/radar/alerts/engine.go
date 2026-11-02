// Package alerts detects what deserves the user's attention right now, pure detection over
// what radar already holds (the engine behind GET /api/alerts), and turns the loudest kinds
// into desktop notifications.
//
// The detectors:
//
//   - stuck:       a live session mid-turn (a prompt with no Stop after it) with no request, tool
//     result or agent event for 10 min — 30 min while a tool is still running — and no
//     Notification since (Claude Code sends one when it waits on the user). Past 6 h of silence
//     a session counts as abandoned, not stuck.
//   - loop:        the same tool with the same input called 5 times in a row by one agent, the
//     last within 30 min.
//   - retry_storm: 5 or more 429/5xx answers within 2 minutes for one session (or one router),
//     the last within 10 min.
//   - context:     an agent's last request filled 85% of its context window (200k, or 1M when
//     the session shows it has one), no compaction since, within the last 6 h. A request
//     holding a run's total from a result event is not one call's context: those requests
//     never fire it.
//   - budget:      a budget at 80% or more of its limit this period (and again at 100%).
//   - upstream:     the Anthropic path is failing: the newest upstream_failing router health
//     event is newer than the newest upstream_recovered and less than 10 minutes old.
//   - quota_nearly_used: Claude's 5-hour plan window, off the newest statusline snapshot, at
//     80% or more. One alert per window: its id is the window's reset, so the dismissal and
//     the notification it gets hold until the next window.
//   - plugin_behind: the newest session start found Claude Code's plugin cache on another huddle
//     commit than the launcher's binary. Its id is the two commits; the next start that finds
//     them equal clears it. Report only: nothing writes into the plugin cache.
//
// Ids are stable for one occurrence, so a dismissal holds until the same thing happens again.
package alerts

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
	"github.com/MuhmdRaouf/huddle/internal/radar/budget"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

// Alert and AlertKind are re-exported from shared, the shapes the API serves.
type Alert = shared.Alert

// AlertKind is re-exported from shared.
type AlertKind = shared.AlertKind

// AlertKinds is every kind the engine detects.
var AlertKinds = []shared.AlertKind{
	shared.AlertStuck, shared.AlertLoop, shared.AlertRetryStorm, shared.AlertBudget, shared.AlertContext,
	shared.AlertUpstream, shared.AlertQuotaNearlyUsed, shared.AlertPluginBehind,
}

// Thresholds are the points each detector fires at.
type Thresholds struct {
	StuckMs         int64
	StuckToolMs     int64
	AbandonedMs     int64
	LoopRepeats     int
	LoopFreshMs     int64
	RetryCount      int
	RetryWindowMs   int64
	RetryFreshMs    int64
	ContextRatio    float64
	BudgetWarnPct   float64
	UpstreamFreshMs int64
}

const minuteMs = 60_000

// DefaultThresholds is where every detector fires by default.
var DefaultThresholds = Thresholds{
	StuckMs:         10 * minuteMs,
	StuckToolMs:     30 * minuteMs,
	AbandonedMs:     6 * 60 * minuteMs,
	LoopRepeats:     5,
	LoopFreshMs:     30 * minuteMs,
	RetryCount:      5,
	RetryWindowMs:   2 * minuteMs,
	RetryFreshMs:    10 * minuteMs,
	ContextRatio:    0.85,
	BudgetWarnPct:   80,
	UpstreamFreshMs: 10 * minuteMs,
}

// AlertInput is everything the detectors look at.
type AlertInput struct {
	Now      int64
	Sessions []store.SessionListItem
	// EachSession visits every session that holds a record with its requests, tool calls and
	// events; the records are read inside the visit and not kept. A nil EachSession holds no
	// records.
	EachSession  func(visit func(sessionID string, records store.SessionRecords))
	APIErrors    []shared.APIErrorRecord
	Budgets      []budget.BudgetSpend
	RouterEvents []shared.RouterEventRecord
	// ClaudeWindow is Claude's 5-hour plan window off the newest statusline snapshot; nil
	// when no session has posted one.
	ClaudeWindow *ClaudeWindow
	// PluginSkew is the plugin cache and binary commits the newest session start found apart;
	// nil when they matched or either was unknown.
	PluginSkew *PluginSkew
}

// ClaudeWindow is Claude's 5-hour plan window as Claude Code reports it: the used percentage
// (0 to 100) and the moment it resets, unix milliseconds.
type ClaudeWindow struct {
	UsedPct  float64
	ResetsAt int64
}

// sessionData is one session's records, each list sorted oldest first.
type sessionData struct {
	requests []*shared.RequestRecord
	tools    []*shared.ToolCallRecord
	events   []*shared.EventRecord
}

// endReasons are the stop reasons that end a turn; anything else (or no reason yet) leaves it
// open. activityEvents are the hook lines that count as the session doing something.
var (
	endReasons = map[string]bool{
		"end_turn": true, "stop_sequence": true, "max_tokens": true, "refusal": true,
	}
	activityEventKinds = []string{
		"UserPromptSubmit", "SubagentStart", "SubagentStop", "PreCompact", "SessionStart",
	}
)

// sortedByTs is the records oldest first: the list itself when it already is, which the
// store's arrival order nearly always is, else a sorted copy. The store's list is never
// reordered in place.
func sortedByTs[T any](records []*T, ts func(*T) int64) []*T {
	byTs := func(a, b *T) int { return cmp.Compare(ts(a), ts(b)) }
	if slices.IsSortedFunc(records, byTs) {
		return records
	}
	copied := slices.Clone(records)
	slices.SortStableFunc(copied, byTs)
	return copied
}

// dataOf is one session's records, oldest first.
func dataOf(records store.SessionRecords) *sessionData {
	return &sessionData{
		requests: sortedByTs(records.Requests, func(r *shared.RequestRecord) int64 { return r.Ts }),
		tools:    sortedByTs(records.Tools, func(t *shared.ToolCallRecord) int64 { return t.StartedAt }),
		events:   sortedByTs(records.Events, func(e *shared.EventRecord) int64 { return e.Ts }),
	}
}

// lastTs is the newest event of one of these kinds.
func lastTs(events []*shared.EventRecord, kinds ...string) (int64, bool) {
	set := make(map[string]bool, len(kinds))
	for _, kind := range kinds {
		set[kind] = true
	}
	for i := len(events) - 1; i >= 0; i-- {
		if set[events[i].Kind] {
			return events[i].Ts, true
		}
	}
	return 0, false
}

// lastRequestOf is the session's newest request.
func lastRequestOf(data *sessionData) *shared.RequestRecord {
	if len(data.requests) == 0 {
		return nil
	}
	return data.requests[len(data.requests)-1]
}

// toolEnd is when a tool call finished.
func toolEnd(tool *shared.ToolCallRecord) int64 {
	if tool.DurationMs != nil {
		return tool.StartedAt + *tool.DurationMs
	}
	return tool.StartedAt
}

// lastActivityOf is the newest request, tool result or agent event the session shows.
func lastActivityOf(data *sessionData) (int64, bool) {
	var best int64
	found := false
	note := func(ts int64) {
		if !found || ts > best {
			best, found = ts, true
		}
	}
	if last := lastRequestOf(data); last != nil {
		note(last.Ts)
	}
	for _, tool := range data.tools {
		note(toolEnd(tool))
	}
	if ts, ok := lastTs(data.events, activityEventKinds...); ok {
		note(ts)
	}
	return best, found
}

// turnOpen says whether the session is in the middle of a turn (as opposed to waiting for the
// user to type).
func turnOpen(data *sessionData, lastActivity int64) bool {
	if last := lastRequestOf(data); last != nil && last.Ts >= lastActivity && last.AgentID == "main" {
		if last.StopReason != nil && endReasons[*last.StopReason] {
			return false
		}
	}
	prompt, hasPrompt := lastTs(data.events, "UserPromptSubmit")
	stop, hasStop := lastTs(data.events, "Stop", "SessionEnd", "Interrupted")
	if hasPrompt {
		return !hasStop || prompt > stop
	}
	// no hook lines for this session: only a request that asked for a tool says a turn is still going
	last := lastRequestOf(data)
	return last != nil && last.StopReason != nil && *last.StopReason == "tool_use"
}

// alertFields are the per-alert parts every detector fills in itself.
type alertFields struct {
	id       string
	since    int64
	detail   string
	severity shared.AlertSeverity
}

// sessionAlert is the fields every session-scoped alert shares: "main" reads as no agent, a
// missing project as none.
func sessionAlert(kind shared.AlertKind, session store.SessionListItem, agentID *string, fields alertFields) shared.Alert {
	alert := shared.Alert{
		ID:        fields.id,
		Kind:      kind,
		SessionID: session.ID,
		Since:     fields.since,
		Detail:    fields.detail,
		Severity:  fields.severity,
	}
	if agentID != nil && *agentID != "main" {
		id := *agentID
		alert.AgentID = &id
	}
	if session.Project != nil {
		alert.Project = *session.Project
	}
	if session.CostUsd != nil {
		cost := *session.CostUsd
		alert.CostUsd = &cost
	}
	return alert
}

// turnSilence is the silence in a turn that is still open: 0 when the session is idle, waiting,
// done or abandoned.
func turnSilence(session store.SessionListItem, data *sessionData) (int64, bool) {
	if !session.Live || session.External {
		return 0, false
	}
	lastActivity, ok := lastActivityOf(data)
	if !ok || !turnOpen(data, lastActivity) {
		return 0, false
	}
	// a Notification after the activity: Claude Code waits on the user, nothing is stuck
	if notified, has := lastTs(data.events, "Notification"); has && notified >= lastActivity {
		return 0, false
	}
	return lastActivity, true
}

func stuckAlert(session store.SessionListItem, data *sessionData, now int64, t Thresholds) *shared.Alert {
	lastActivity, ok := turnSilence(session, data)
	if !ok {
		return nil
	}
	last := lastRequestOf(data)
	toolRunning := last != nil && last.StopReason != nil && *last.StopReason == "tool_use" && last.Ts >= lastActivity
	idle := now - lastActivity
	quietFor := t.StuckMs
	if toolRunning {
		quietFor = t.StuckToolMs
	}
	if idle < quietFor || idle > t.AbandonedMs {
		return nil
	}
	minutes := idle / minuteMs
	detail := fmt.Sprintf("No request or tool activity for %d min in the middle of a turn", minutes)
	if toolRunning {
		detail = fmt.Sprintf("A tool has been running for %d min with nothing back", minutes)
	}
	var agentID *string
	if last != nil {
		id := last.AgentID
		agentID = &id
	}
	alert := sessionAlert(shared.AlertStuck, session, agentID, alertFields{
		id:       fmt.Sprintf("stuck:%s:%d", session.ID, lastActivity),
		since:    lastActivity,
		detail:   detail,
		severity: shared.SeverityErr,
	})
	return &alert
}

// toolAgent is the agent that made a tool call; a call with no agent is the main agent's.
func toolAgent(tool *shared.ToolCallRecord) string {
	if tool.AgentID != nil {
		return *tool.AgentID
	}
	return "main"
}

// toolAgents lists the agents that made the calls, in the order each first appears.
func toolAgents(tools []*shared.ToolCallRecord) []string {
	var agents []string
	seen := map[string]bool{}
	for _, tool := range tools {
		if agent := toolAgent(tool); !seen[agent] {
			seen[agent] = true
			agents = append(agents, agent)
		}
	}
	return agents
}

// trailingStreak is the current run of identical calls at the end of one agent's tool calls:
// its first call, its last call and its length; length 0 when the agent's last call has no
// input key.
func trailingStreak(tools []*shared.ToolCallRecord, agentID string) (first, last *shared.ToolCallRecord, length int) {
	for at := len(tools) - 1; at >= 0; at-- {
		call := tools[at]
		if toolAgent(call) != agentID {
			continue
		}
		if last == nil {
			if call.InputKey == nil {
				return nil, nil, 0
			}
			last = call
		} else if call.InputKey == nil || *call.InputKey != *last.InputKey || call.Name != last.Name {
			break
		}
		first, length = call, length+1
	}
	return first, last, length
}

// keyedGroup is every item that answered one key.
type keyedGroup[T any] struct {
	key   string
	items []T
}

// groupByKey groups items by key with the keys in first-appearance order, the order a
// JavaScript Map iterates in.
func groupByKey[T any](items []T, key func(T) string) []keyedGroup[T] {
	var groups []keyedGroup[T]
	index := map[string]int{}
	for _, item := range items {
		k := key(item)
		if at, ok := index[k]; ok {
			groups[at].items = append(groups[at].items, item)
			continue
		}
		index[k] = len(groups)
		groups = append(groups, keyedGroup[T]{key: k, items: []T{item}})
	}
	return groups
}

func loopAlerts(session store.SessionListItem, data *sessionData, now int64, t Thresholds) []shared.Alert {
	var alerts []shared.Alert
	for _, agentID := range toolAgents(data.tools) {
		first, last, length := trailingStreak(data.tools, agentID)
		if length < t.LoopRepeats || now-last.StartedAt > t.LoopFreshMs {
			continue
		}
		alerts = append(alerts, sessionAlert(shared.AlertLoop, session, &agentID, alertFields{
			id:       fmt.Sprintf("loop:%s:%s:%s", session.ID, agentID, first.ID),
			since:    first.StartedAt,
			detail:   fmt.Sprintf("%s called %d times in a row with the same input", last.Name, length),
			severity: shared.SeverityWarn,
		}))
	}
	return alerts
}

func contextAlerts(session store.SessionListItem, data *sessionData, now int64, t Thresholds) []shared.Alert {
	if !session.Live {
		return nil
	}
	window := shared.ContextWindow(data.requests)
	// an agent's slot keeps its newest request and its first-seen position
	var order []string
	lastByAgent := map[string]*shared.RequestRecord{}
	for _, request := range data.requests {
		if _, ok := lastByAgent[request.AgentID]; !ok {
			order = append(order, request.AgentID)
		}
		lastByAgent[request.AgentID] = request
	}
	compacted := int64(-1)
	if ts, ok := lastTs(data.events, "PreCompact"); ok {
		compacted = ts
	}
	var alerts []shared.Alert
	for _, agentID := range order {
		request := lastByAgent[agentID]
		if shared.IsRunTotal(request) {
			continue // the job's total would read as many windows over
		}
		used := shared.ContextOf(request)
		ratio := float64(used) / float64(window)
		stale := now-request.Ts > t.AbandonedMs || compacted >= request.Ts
		if ratio < t.ContextRatio || stale {
			continue
		}
		pct := int64(math.Round(ratio * 100))
		alerts = append(alerts, sessionAlert(shared.AlertContext, session, &agentID, alertFields{
			id:    fmt.Sprintf("context:%s:%s:%s", session.ID, agentID, request.ID),
			since: request.Ts,
			detail: fmt.Sprintf("Context at %dk of %dk tokens (%d%%): compaction is near",
				int64(math.Round(float64(used)/1000)), window/1000, pct),
			severity: shared.SeverityWarn,
		}))
	}
	return alerts
}

// failuresBySubject buckets the retryable failures in the freshness window by session, or by
// router when no session is known, subjects in first-appearance order.
func failuresBySubject(in AlertInput, t Thresholds) []keyedGroup[shared.APIErrorRecord] {
	var fresh []shared.APIErrorRecord
	for _, failure := range in.APIErrors {
		if failure.Ts <= in.Now && in.Now-failure.Ts <= t.RetryFreshMs {
			fresh = append(fresh, failure)
		}
	}
	return groupByKey(fresh, func(failure shared.APIErrorRecord) string {
		if failure.SessionID != nil {
			return *failure.SessionID
		}
		plugin := "unknown"
		if failure.Plugin != nil {
			plugin = *failure.Plugin
		}
		return "router:" + plugin
	})
}

func retryDetail(window []shared.APIErrorRecord, t Thresholds) string {
	first := window[0]
	var statuses []string
	seen := map[int64]bool{}
	for _, failure := range window {
		if !seen[failure.Status] {
			seen[failure.Status] = true
			statuses = append(statuses, strconv.FormatInt(failure.Status, 10))
		}
	}
	slices.Sort(statuses)
	where := ""
	if first.SessionID == nil && first.Plugin != nil {
		where = fmt.Sprintf(" at the %s router", shared.ScopeLabel("provider:"+*first.Plugin))
	}
	return fmt.Sprintf("%d rate limits or server errors (%s) within %d min%s",
		len(window), strings.Join(statuses, ", "),
		int64(math.Round(float64(t.RetryWindowMs)/minuteMs)), where)
}

func retryAlerts(in AlertInput, t Thresholds, sessions map[string]store.SessionListItem) []shared.Alert {
	var alerts []shared.Alert
	for _, group := range failuresBySubject(in, t) {
		slices.SortStableFunc(group.items, func(a, b shared.APIErrorRecord) int {
			return cmp.Compare(a.Ts, b.Ts)
		})
		window := densestWindow(group.items, t.RetryWindowMs)
		if len(window) < t.RetryCount {
			continue
		}
		first := window[0]
		session, ok := sessions[group.key]
		if !ok {
			id := ""
			if first.SessionID != nil {
				id = *first.SessionID
			}
			session = store.SessionListItem{ID: id}
		}
		alerts = append(alerts, sessionAlert(shared.AlertRetryStorm, session, nil, alertFields{
			id:       fmt.Sprintf("retry_storm:%s:%d", group.key, first.Ts),
			since:    first.Ts,
			detail:   retryDetail(window, t),
			severity: shared.SeverityErr,
		}))
	}
	return alerts
}

// densestWindow is the longest run of failures fitting in one window, the earliest such run on
// a tie.
func densestWindow(failures []shared.APIErrorRecord, windowMs int64) []shared.APIErrorRecord {
	var best []shared.APIErrorRecord
	start := 0
	for end := range failures {
		endTs := failures[end].Ts
		for failures[start].Ts < endTs-windowMs {
			start++
		}
		if end-start+1 > len(best) {
			best = failures[start : end+1]
		}
	}
	return best
}

// periodWord is a budget period as the detail sentence reads it.
var periodWord = map[budget.Period]string{
	budget.PeriodDay: "daily", budget.PeriodWeek: "weekly", budget.PeriodMonth: "monthly",
}

func budgetAlerts(in AlertInput, t Thresholds) []shared.Alert {
	var alerts []shared.Alert
	for _, spend := range in.Budgets {
		if float64(spend.Pct) < t.BudgetWarnPct {
			continue
		}
		over := float64(spend.Pct) >= 100
		action := "warn only"
		if spend.Action == budget.ActionStop {
			action = "stops at 100%"
			if over {
				action = "requests are stopped"
			}
		}
		threshold := jsscalar.JSNumber(t.BudgetWarnPct)
		if over {
			threshold = "100"
		}
		cost := spend.SpentUsd
		alert := shared.Alert{
			ID:        fmt.Sprintf("budget:%s:%d:%s", spend.ID, spend.PeriodStart, threshold),
			Kind:      shared.AlertBudget,
			SessionID: "",
			Project:   "",
			Since:     spend.PeriodStart,
			Detail: fmt.Sprintf("%s %s budget at %d%% ($%s of $%s), %s",
				shared.ScopeLabel(spend.Scope), periodWord[spend.Period],
				int64(math.Round(float64(spend.Pct))),
				jsscalar.ToFixed(float64(spend.SpentUsd), 2), jsscalar.ToFixed(float64(spend.LimitUsd), 2),
				action),
			CostUsd:  &cost,
			Severity: shared.SeverityWarn,
		}
		if over {
			alert.Severity = shared.SeverityErr
		}
		alerts = append(alerts, alert)
	}
	return alerts
}

// upstreamAlerts is the Anthropic path's alert: Claude Code cannot reach Anthropic through
// huddle-router. One critical alert while the newest upstream_failing router health event is
// newer than the newest upstream_recovered and less than UpstreamFreshMs old; its detail names
// the failure code and its fix hint.
func upstreamAlerts(in AlertInput, t Thresholds) []shared.Alert {
	var failing, recovered *shared.RouterEventRecord
	for i := range in.RouterEvents {
		switch event := in.RouterEvents[i]; event.Event {
		case shared.EventUpstreamFailing:
			if failing == nil || event.Ts >= failing.Ts {
				failing = &in.RouterEvents[i]
			}
		case shared.EventUpstreamRecovered:
			if recovered == nil || event.Ts >= recovered.Ts {
				recovered = &in.RouterEvents[i]
			}
		}
	}
	if failing == nil || in.Now-failing.Ts >= t.UpstreamFreshMs {
		return nil
	}
	if recovered != nil && recovered.Ts >= failing.Ts {
		return nil
	}
	return []shared.Alert{{
		ID:       fmt.Sprintf("upstream:%s:%d", failing.Plugin, failing.Ts),
		Kind:     shared.AlertUpstream,
		Since:    failing.Ts,
		Detail:   failing.Reason + ": " + domain.FailHint(failing.Reason),
		Severity: shared.SeverityErr,
	}}
}

// quotaNearlyUsedRatio is the share of a quota that raises quota_nearly_used.
const quotaNearlyUsedRatio = 0.8

// claudeWindowMs is the span of Claude's plan window.
const claudeWindowMs = 5 * 60 * minuteMs

// quotaAlerts raises quota_nearly_used while Claude's 5-hour window is at 80% or more, the id
// keyed by the window's reset. Provider budgets have their own budget alert.
func quotaAlerts(in AlertInput) []shared.Alert {
	window := in.ClaudeWindow
	if window == nil || window.ResetsAt <= in.Now || window.UsedPct < quotaNearlyUsedRatio*100 {
		return nil
	}
	return []shared.Alert{{
		ID:    fmt.Sprintf("%s:claude-5h:%d", shared.AlertQuotaNearlyUsed, window.ResetsAt),
		Kind:  shared.AlertQuotaNearlyUsed,
		Since: window.ResetsAt - claudeWindowMs,
		Detail: fmt.Sprintf("Claude 5-hour window %d%% used, resets %s UTC",
			int64(math.Round(window.UsedPct)), time.UnixMilli(window.ResetsAt).UTC().Format("15:04")),
		Severity: shared.SeverityWarn,
	}}
}

// severityOrder puts err before warn.
var severityOrder = map[shared.AlertSeverity]int{shared.SeverityErr: 0, shared.SeverityWarn: 1}

// DetectAlerts returns every alert that holds right now, loudest and newest first.
func DetectAlerts(in AlertInput, t Thresholds) []shared.Alert {
	sessions := make(map[string]store.SessionListItem, len(in.Sessions))
	for _, session := range in.Sessions {
		sessions[session.ID] = session
	}
	var alerts []shared.Alert
	if in.EachSession != nil {
		in.EachSession(func(sessionID string, records store.SessionRecords) {
			session, ok := sessions[sessionID]
			if !ok {
				return
			}
			data := dataOf(records)
			if stuck := stuckAlert(session, data, in.Now, t); stuck != nil {
				alerts = append(alerts, *stuck)
			}
			alerts = append(alerts, loopAlerts(session, data, in.Now, t)...)
			alerts = append(alerts, contextAlerts(session, data, in.Now, t)...)
		})
	}
	alerts = append(alerts, retryAlerts(in, t, sessions)...)
	alerts = append(alerts, budgetAlerts(in, t)...)
	alerts = append(alerts, upstreamAlerts(in, t)...)
	alerts = append(alerts, quotaAlerts(in)...)
	alerts = append(alerts, pluginBehindAlerts(in)...)
	slices.SortStableFunc(alerts, func(a, b shared.Alert) int {
		if bySeverity := cmp.Compare(severityOrder[a.Severity], severityOrder[b.Severity]); bySeverity != 0 {
			return bySeverity
		}
		return cmp.Compare(b.Since, a.Since)
	})
	return alerts
}
