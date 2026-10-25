// API routing as a pure function: parse a pathname + query, answer a status and a JSON
// body. No sockets here, so tests cover every route without an HTTP server. Query values
// are strings (or repeated); limits are clamped. The /api/history routes read the same
// shapes out of the SQLite history; they 503 when it failed to open.
package server

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/radar/budget"
	"github.com/MuhmdRaouf/huddle/internal/radar/cost"
	"github.com/MuhmdRaouf/huddle/internal/radar/history"
	"github.com/MuhmdRaouf/huddle/internal/radar/routerhealth"
	"github.com/MuhmdRaouf/huddle/internal/radar/settings"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

// ApiRequest is one routed request: the path as the URL spelled it (still percent-encoded)
// and its parsed query.
type ApiRequest struct {
	Path  string
	Query url.Values
}

// ApiResponse is one routed answer: a status and the JSON body sent with it.
type ApiResponse struct {
	Status int
	Body   any
}

// AppInfo is the build facts the health route serves.
type AppInfo struct {
	Version   string
	StartedAt int64
	Port      int
}

// HistoryLiveMs: a tree any node of which moved within this window is live; older ones read
// as history.
const HistoryLiveMs = 15 * 60_000

// ContextNote is what the rebuilt conversation cannot have: the transcript never carried
// them.
const ContextNote = "System prompt and tool definitions are not in the transcript."

// maxLimit and defaultLimit clamp every paged read.
const (
	maxLimit     = 1000
	defaultLimit = 200
)

// Routes lists every route the 404 answer names, the original eight first.
var Routes = []string{
	"/api/summary",
	"/api/sessions",
	"/api/sessions/:id",
	"/api/requests",
	"/api/events",
	"/api/tools",
	"/api/models",
	"/api/health",
	"/api/alerts",
	"/api/attribution",
	"/api/budgets",
	"/api/budget-status",
	"/api/spend",
	"/api/settings",
	"/api/router",
	"/api/advisor",
	"/api/usage",
	"/api/usage/today",
	DayPath,
	"/api/blocks",
	"/api/history/roots",
	"/api/history/tree/:rootId",
	"/api/history/requests",
	"/api/history/content/:requestId",
	"/api/history/context/:requestId",
	"/api/history/request/:requestId/capture",
	"/api/history/agent/:nodeId/transcript",
	"/api/history/repos",
	"/api/history/flow",
	"/api/history/stats",
	"/api/history/clear",
	"/api/quota",
	SelfCheckPath,
	OtelPath,
}

// InsightsView is the derived views the API routes read: costs, budgets, alerts, settings,
// router health and the model advisor.
type InsightsView interface {
	Alerts() []shared.Alert
	Attribution(cost.AttributionBy, cost.AttributionRange) []cost.AttributionRow
	Tree(cost.AttributionRange) []cost.AttributionNode
	Budgets() []budget.Budget
	BudgetStatus() budget.BudgetStatus
	SpendToday() cost.Spend
	Providers() []string
	Settings() settings.Settings
	RouterHealth() routerhealth.RouterHealth
	Advisor() cost.AdvisorReport
}

// WriteInsights is the writes the dashboard's own forms send.
type WriteInsights interface {
	SetBudgets(ctx context.Context, input *jsfmt.Value) ([]budget.Budget, string, error)
	SetSettings(ctx context.Context, input *jsfmt.Value) (settings.Settings, string, error)
	Dismiss(id string) bool
}

// The answer bodies, their keys spelled in the order the dashboard reads them.

type errorBody struct {
	Error string `json:"error"`
}

type requestsAnswer struct {
	Requests any `json:"requests"`
}

type eventsAnswer struct {
	Events any `json:"events"`
}

type toolsAnswer struct {
	Tools any `json:"tools"`
}

type alertsAnswer struct {
	Alerts []shared.Alert `json:"alerts"`
}

type attributionRows struct {
	By    string                `json:"by"`
	Range string                `json:"range"`
	Rows  []cost.AttributionRow `json:"rows"`
}

type attributionTree struct {
	By    string                 `json:"by"`
	Range string                 `json:"range"`
	Tree  []cost.AttributionNode `json:"tree"`
}

type budgetsAnswer struct {
	Budgets   []budget.Budget `json:"budgets"`
	Providers []string        `json:"providers"`
}

type spendAnswer struct {
	TodayUsd *shared.USD `json:"todayUsd"`
	// Unpriced is today's calls left out of TodayUsd.
	Unpriced        int64  `json:"unpriced"`
	PricesRetrieved string `json:"pricesRetrieved"`
}

type settingsAnswer struct {
	Settings settings.Settings `json:"settings"`
}

type summaryAnswer struct {
	Summary shared.Summary `json:"summary"`
}

type sessionsAnswer struct {
	Sessions []store.SessionTreeItem `json:"sessions"`
}

type sessionAnswer struct {
	Session *shared.SessionView `json:"session"`
}

type healthAnswer struct {
	OK        bool   `json:"ok"`
	Version   string `json:"version"`
	StartedAt int64  `json:"startedAt"`
	Port      int    `json:"port"`
	Sessions  int64  `json:"sessions"`
	UptimeMs  int64  `json:"uptimeMs"`
}

type notFoundAnswer struct {
	Error  string   `json:"error"`
	Routes []string `json:"routes"`
}

type savedBudgets struct {
	Budgets []budget.Budget `json:"budgets"`
}

type savedSettings struct {
	Settings settings.Settings `json:"settings"`
}

type dismissedAnswer struct {
	Dismissed string `json:"dismissed"`
}

type clearedAnswer struct {
	Cleared bool `json:"cleared"`
}

// costedRoot is one roots page row: a root with its tree's estimated cost appended.
type costedRoot struct {
	shared.RootSummary
	CostUsd *shared.USD `json:"costUsd"`
}

// costedNode is one tree row with the node's own estimated cost appended.
type costedNode struct {
	shared.TreeNodeRow
	CostUsd *shared.USD `json:"costUsd"`
}

// costedRequest is one request row with its estimated cost appended.
type costedRequest struct {
	shared.RequestRecord
	Cost *cost.PriceResult `json:"cost"`
}

// MarshalJSON prints the record's own keys in their order and the cost last; the embedded
// record's own marshaller would otherwise answer for the row alone and drop the cost.
func (c costedRequest) MarshalJSON() ([]byte, error) {
	base, err := c.RequestRecord.MarshalJSON()
	if err != nil {
		return nil, err
	}
	priced, err := domain.MarshalCompactJSON(c.Cost)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(base)+len(priced)+10)
	out = append(out, base[:len(base)-1]...)
	out = append(out, `,"cost":`...)
	out = append(out, priced...)
	return append(out, '}'), nil
}

type rootsAnswer struct {
	Roots []costedRoot `json:"roots"`
	Next  *int64       `json:"next"`
}

type treeAnswer struct {
	Nodes []costedNode `json:"nodes"`
}

type requestsPageAnswer struct {
	Requests []costedRequest `json:"requests"`
	Next     *string         `json:"next"`
}

type contentAnswer struct {
	Input  any   `json:"input"`
	Output any   `json:"output"`
	Bytes  int64 `json:"bytes"`
}

type repoRow struct {
	Repo  string `json:"repo"`
	Roots int64  `json:"roots"`
}

type reposAnswer struct {
	Repos []repoRow `json:"repos"`
}

type statsAnswer struct {
	Bytes         int64 `json:"bytes"`
	Nodes         int64 `json:"nodes"`
	Requests      int64 `json:"requests"`
	Roots         int64 `json:"roots"`
	RetentionDays int   `json:"retentionDays"`
}

// notedContext is one rebuilt conversation with the transcript's own limits noted.
type notedContext struct {
	shared.ContextAnswer
	Note string `json:"note"`
}

// jsonOf answers one routed answer.
func jsonOf(status int, body any) ApiResponse { return ApiResponse{Status: status, Body: body} }

// errorOf answers one routed refusal.
func errorOf(status int, message string) ApiResponse {
	return jsonOf(status, errorBody{Error: message})
}

// parseInt10 reads a query number the way JavaScript's parseInt does: leading Unicode
// whitespace, an optional sign, then digits, stopping at the first other character. A digit
// run too long for an int64 saturates, the way the double it parses to stays finite.
func parseInt10(raw string) (int64, bool) {
	text := jsfmt.Trim(raw)
	negative := false
	switch {
	case strings.HasPrefix(text, "-"):
		negative = true
		text = text[1:]
	case strings.HasPrefix(text, "+"):
		text = text[1:]
	}
	end := 0
	for end < len(text) && text[end] >= '0' && text[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	value, err := strconv.ParseInt(text[:end], 10, 64)
	if err != nil {
		if negative {
			return math.MinInt64, true
		}
		return math.MaxInt64, true
	}
	if negative {
		value = -value
	}
	return value, true
}

// clampLimit reads one page size: nothing or a non-number is the default, a positive ask
// caps at a thousand.
func clampLimit(raw string) int64 {
	parsed, ok := parseInt10(raw)
	if !ok || parsed <= 0 {
		return defaultLimit
	}
	return min(parsed, maxLimit)
}

// numParam reads one integer query parameter; nil when absent or not a number.
func numParam(query url.Values, key string) *int64 {
	if !query.Has(key) {
		return nil
	}
	if parsed, ok := parseInt10(query.Get(key)); ok {
		return &parsed
	}
	return nil
}

// strParam reads one query parameter that means something only when present and non-empty.
func strParam(query url.Values, key string) *string {
	if !query.Has(key) {
		return nil
	}
	raw := query.Get(key)
	if raw == "" {
		return nil
	}
	return &raw
}

// queryText reads one query parameter a filter reads verbatim, empty included: a key the
// query carried, even empty, narrows to the empty text.
func queryText(query url.Values, key string) *string {
	if !query.Has(key) {
		return nil
	}
	raw := query.Get(key)
	return &raw
}

// decodeOf decodes one path id; a malformed escape is the caller's typo, not a crash.
func decodeOf(raw string) *string {
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return nil
	}
	return &decoded
}

// oneOf reads one enum query parameter: absent or empty is the fallback, anything else must
// be named or the read refuses.
func oneOf[T ~string](allowed []T, raw string, fallback T) (T, bool) {
	if raw == "" {
		return fallback, true
	}
	for _, candidate := range allowed {
		if string(candidate) == raw {
			return candidate, true
		}
	}
	return fallback, false
}

// listRoute answers the paged record lists: requests, events and tool calls, newest first.
func listRoute(st *store.Store, path string, query url.Values) (ApiResponse, bool) {
	switch path {
	case "/api/requests":
		filter := store.RequestFilter{Limit: ptrOf(clampLimit(query.Get("limit")))}
		filter.Session = queryText(query, "session")
		filter.Agent = queryText(query, "agent")
		filter.Model = queryText(query, "model")
		filter.Since = numParam(query, "since")
		return jsonOf(200, requestsAnswer{Requests: st.Requests(filter)}), true
	case "/api/events":
		filter := store.EventsFilter{Limit: ptrOf(clampLimit(query.Get("limit")))}
		filter.Session = queryText(query, "session")
		filter.Since = numParam(query, "since")
		return jsonOf(200, eventsAnswer{Events: st.Events(filter)}), true
	case "/api/tools":
		filter := store.ToolsFilter{
			Failed: query.Get("failed") == "1",
			Limit:  ptrOf(clampLimit(query.Get("limit"))),
		}
		filter.Session = queryText(query, "session")
		return jsonOf(200, toolsAnswer{Tools: st.Tools(filter)}), true
	}
	return ApiResponse{}, false
}

// modelsRoute answers the models tab's tables: without a scope the whole store's cached
// answer, the one the snapshot pushes; a window and/or the picked sessions (comma-joined,
// as the hash carries them) aggregate those requests instead.
func modelsRoute(st *store.Store, query url.Values) ApiResponse {
	var scope *store.ModelsScope
	from := numParam(query, "from")
	to := numParam(query, "to")
	sessions := []string{}
	for _, raw := range query["session"] {
		for _, id := range strings.Split(raw, ",") {
			if id != "" {
				sessions = append(sessions, id)
			}
		}
	}
	if from != nil || to != nil || len(sessions) > 0 {
		scope = &store.ModelsScope{From: from, To: to}
		if len(sessions) > 0 {
			scope.Sessions = sessions
		}
	}
	return jsonOf(200, st.Models(scope))
}

// insightRoute answers the derived views: costs, budgets, alerts, settings, router health
// and the model advisor.
func insightRoute(insights InsightsView, path string, query url.Values) (ApiResponse, bool) {
	switch path {
	case "/api/alerts":
		return jsonOf(200, alertsAnswer{Alerts: insights.Alerts()}), true
	case "/api/attribution":
		return attributionRoute(insights, query)
	case "/api/budgets":
		saved := insights.Budgets()
		if saved == nil {
			// the dashboard reads null as "not loaded yet"; an empty store is a list, not nothing
			saved = []budget.Budget{}
		}
		return jsonOf(200, budgetsAnswer{Budgets: saved, Providers: insights.Providers()}), true
	case "/api/budget-status":
		return jsonOf(200, insights.BudgetStatus()), true
	case "/api/spend":
		spend := insights.SpendToday()
		return jsonOf(200, spendAnswer{TodayUsd: spend.USD, Unpriced: spend.Unpriced, PricesRetrieved: cost.PricesRetrieved}), true
	case "/api/settings":
		return jsonOf(200, settingsAnswer{Settings: insights.Settings()}), true
	case "/api/router":
		return jsonOf(200, insights.RouterHealth()), true
	case "/api/advisor":
		return jsonOf(200, insights.Advisor()), true
	}
	return ApiResponse{}, false
}

// attributionRoute answers the flat groupings and the drill-down tree.
func attributionRoute(insights InsightsView, query url.Values) (ApiResponse, bool) {
	rangeOf, ok := oneOf(cost.AttributionRanges, query.Get("range"), cost.RangeDay)
	if !ok {
		return errorOf(400, "range: one of "+joinRanges()), true
	}
	// by=tree asks for the drill-down; anything else names one flat grouping, repo by default
	if query.Get("by") == "tree" {
		return jsonOf(200, attributionTree{
			By:    "tree",
			Range: string(rangeOf),
			Tree:  insights.Tree(rangeOf),
		}), true
	}
	by, ok := oneOf(cost.AttributionByKinds, query.Get("by"), cost.ByRepo)
	if !ok {
		return errorOf(400, "by: one of "+joinByKinds()+", tree"), true
	}
	return jsonOf(200, attributionRows{
		By:    string(by),
		Range: string(rangeOf),
		Rows:  insights.Attribution(by, rangeOf),
	}), true
}

// joinRanges and joinByKinds spell the attribution parameters' allowed values.
func joinRanges() string {
	parts := make([]string, len(cost.AttributionRanges))
	for i, entry := range cost.AttributionRanges {
		parts[i] = string(entry)
	}
	return strings.Join(parts, ", ")
}

func joinByKinds() string {
	parts := make([]string, len(cost.AttributionByKinds))
	for i, entry := range cost.AttributionByKinds {
		parts[i] = string(entry)
	}
	return strings.Join(parts, ", ")
}

// dismissAlert silences one alert by the id its body names.
func dismissAlert(insights WriteInsights, body *jsfmt.Value) ApiResponse {
	var id string
	if body != nil && body.Kind() == jsfmt.KindObject {
		if field := fieldOf(body, "id"); field != nil && field.Kind() == jsfmt.KindString {
			id = field.Text()
		}
	}
	if id == "" || !insights.Dismiss(id) {
		return errorOf(400, "id: the alert's id")
	}
	return jsonOf(200, dismissedAnswer{Dismissed: id})
}

// HandleWrite routes the dashboard's own writes: PUT /api/budgets, PUT /api/settings,
// POST /api/alerts/dismiss and POST /api/history/clear — validated JSON in, the saved value
// out. A failed save reaches the caller as an error; the HTTP layer answers it 500. The
// statusline's quota POST rides here too: it writes the quota store, not insights.
func HandleWrite(ctx context.Context, insights WriteInsights, quotas *Quotas, method, path string, body *jsfmt.Value, h *history.History) (ApiResponse, error) {
	if method == "POST" && path == "/api/quota" {
		return HandleQuotaWrite(ctx, quotas, body), nil
	}
	if insights == nil {
		return errorOf(503, "not available"), nil
	}
	switch method + " " + path {
	case "PUT /api/budgets":
		saved, refusal, err := insights.SetBudgets(ctx, body)
		if err != nil {
			return ApiResponse{}, err
		}
		if refusal != "" {
			return errorOf(400, refusal), nil
		}
		if saved == nil {
			saved = []budget.Budget{}
		}
		return jsonOf(200, savedBudgets{Budgets: saved}), nil
	case "PUT /api/settings":
		saved, refusal, err := insights.SetSettings(ctx, body)
		if err != nil {
			return ApiResponse{}, err
		}
		if refusal != "" {
			return errorOf(400, refusal), nil
		}
		return jsonOf(200, savedSettings{Settings: saved}), nil
	case "POST /api/alerts/dismiss":
		return dismissAlert(insights, body), nil
	case "POST /api/history/clear":
		if h == nil {
			return errorOf(503, "history unavailable"), nil
		}
		if err := h.Clear(); err != nil {
			return ApiResponse{}, err
		}
		return jsonOf(200, clearedAnswer{Cleared: true}), nil
	}
	return errorOf(405, "method not allowed"), nil
}

// pricedOfRow shapes one cost row the way the price reader reads it.
func pricedOfRow(row history.CostRow) cost.PricedRequest {
	return cost.PricedRequest{
		Model:    derefText(row.Model),
		Upstream: derefText(row.Upstream),
		Ts:       row.Ts,
		Tokens: shared.Tokens{
			Input:      derefCount(row.Input),
			Output:     derefCount(row.Output),
			CacheRead:  derepOf(row.CacheRead, 0),
			CacheWrite: derepOf(row.CacheWrite, 0),
		},
		CacheWrite1h: row.CacheWrite1h,
		Speed:        row.Speed,
		Geo:          row.Geo,
		ServiceTier:  row.ServiceTier,
	}
}

// pricedOfRecord shapes one stored request the way the price reader reads it: a null stop
// reason with no output marks a request still streaming, whose numbers never price.
func pricedOfRecord(record shared.RequestRecord) cost.PricedRequest {
	return cost.PricedRequest{
		Model:          record.Model,
		Upstream:       record.Upstream,
		Ts:             record.Ts,
		Tokens:         record.Tokens,
		StopReason:     record.StopReason,
		StopReasonNull: record.StopReason == nil,
		CacheWrite1h:   record.CacheWrite1h,
		Speed:          record.Speed,
		Geo:            record.Geo,
		ServiceTier:    record.ServiceTier,
	}
}

// addInto sums one request's price into its group; an unpriced request is skipped, a mixed
// group sums the priced part.
func addInto(totals map[string]*shared.USD, key string, row history.CostRow) {
	price := cost.RequestCost(pricedOfRow(row))
	if price == nil {
		return
	}
	totals[key] = cost.AddCost(totals[key], &price.USD)
}

// costsOfRoots answers the estimated USD per tree, read in batched queries: nil when
// nothing in the tree had a price.
func costsOfRoots(h *history.History, rootIDs []string) (map[string]*shared.USD, error) {
	totals := make(map[string]*shared.USD, len(rootIDs))
	for _, id := range rootIDs {
		totals[id] = nil
	}
	rows, err := h.CostRows(rootIDs)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		addInto(totals, row.RootID, row)
	}
	return totals, nil
}

// costsOfNodes answers the estimated USD per node of one tree, from the same single batched
// read.
func costsOfNodes(h *history.History, rootID string) (map[string]*shared.USD, error) {
	totals := map[string]*shared.USD{}
	rows, err := h.CostRows([]string{rootID})
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		addInto(totals, row.NodeID, row)
	}
	return totals, nil
}

// liveSessionIds answers the sessions the Live tab shows, by id: the registry's open mains,
// never jobs. History keeps these out, so a session that is open but has been quiet for a
// while never reads as ended.
func liveSessionIds(st *store.Store) []string {
	ids := []string{}
	for _, item := range st.SessionList() {
		if item.Live && !item.External && !strings.Contains(item.ID, ":") {
			ids = append(ids, item.ID)
		}
	}
	return ids
}

// unattachedGroup answers the one Unattached group the unfiltered first history page ends
// with: every job no session claims, rolled up together — never a top-level root per job,
// and never more than once across the pages.
func unattachedGroup(h *history.History, query url.Values, now int64) (*shared.RootSummary, error) {
	// any filter or page cursor hides it: it is the end of the unfiltered first page alone
	if numParam(query, "before") != nil || strParam(query, "q") != nil || strParam(query, "repo") != nil {
		return nil, nil
	}
	return h.Unattached(history.UnattachedQuery{Now: now, LiveMs: HistoryLiveMs})
}

// historyRoots answers one page of top-level trees with their costs and the next cursor.
func historyRoots(h *history.History, query url.Values, live []string, now int64) (ApiResponse, error) {
	scope, ok := oneOf([]history.Scope{history.ScopeLive, history.ScopeHistory}, query.Get("scope"), history.ScopeLive)
	if !ok {
		return errorOf(400, "scope: one of live, history"), nil
	}
	limit := clampLimit(query.Get("limit"))
	q := history.RootsQuery{Scope: scope, Now: now, LiveMs: HistoryLiveMs, Limit: limit + 1}
	q.Before = numParam(query, "before")
	q.Search = strParam(query, "q")
	q.Repo = strParam(query, "repo")
	if scope == history.ScopeHistory && len(live) > 0 {
		q.ExcludeSessions = live
	}
	// one row past the page tells whether a next page exists, so `next` is null exactly at the end
	found, err := h.Roots(q)
	if err != nil {
		return ApiResponse{}, err
	}
	more := int64(len(found)) > limit
	roots := found
	if more {
		roots = found[:limit]
	}
	group, err := unattachedGroupFor(h, query, scope, now)
	if err != nil {
		return ApiResponse{}, err
	}
	ids := make([]string, 0, len(roots)+1)
	for _, root := range roots {
		ids = append(ids, root.ID)
	}
	if group != nil {
		ids = append(ids, group.ID)
	}
	costs, err := costsOfRoots(h, ids)
	if err != nil {
		return ApiResponse{}, err
	}
	page := make([]costedRoot, 0, len(roots)+1)
	for _, root := range roots {
		page = append(page, costedRoot{RootSummary: root, CostUsd: costs[root.ID]})
	}
	if group != nil {
		page = append(page, costedRoot{RootSummary: *group, CostUsd: costs[group.ID]})
	}
	var next *int64
	if more && len(roots) > 0 {
		last := roots[len(roots)-1].LastAt
		next = &last
	}
	return jsonOf(200, rootsAnswer{Roots: page, Next: next}), nil
}

// unattachedGroupFor answers the group in the history scope alone.
func unattachedGroupFor(h *history.History, query url.Values, scope history.Scope, now int64) (*shared.RootSummary, error) {
	if scope != history.ScopeHistory {
		return nil, nil
	}
	return unattachedGroup(h, query, now)
}

// historyTree answers every node of one tree with its own estimated cost.
func historyTree(h *history.History, rawID string, now int64) (ApiResponse, error) {
	id := decodeOf(rawID)
	if id == nil {
		return errorOf(400, "id is not valid URI encoding"), nil
	}
	if *id == "" {
		return errorOf(404, "missing root id"), nil
	}
	liveMs := int64(HistoryLiveMs)
	nodes, err := h.Tree(*id, history.TreeQuery{Now: &now, LiveMs: &liveMs})
	if err != nil {
		return ApiResponse{}, err
	}
	if len(nodes) == 0 {
		return errorOf(404, "unknown root: "+*id), nil
	}
	costs, err := costsOfNodes(h, *id)
	if err != nil {
		return ApiResponse{}, err
	}
	page := make([]costedNode, 0, len(nodes))
	for _, node := range nodes {
		page = append(page, costedNode{TreeNodeRow: node, CostUsd: costs[node.ID]})
	}
	return jsonOf(200, treeAnswer{Nodes: page}), nil
}

// beforeOf parses the requests cursor: the last request of the previous page as
// "<ts>:<id>"; ids may hold colons.
func beforeOf(raw string) (*history.RequestCursor, string) {
	if raw == "" {
		return nil, ""
	}
	colon := strings.Index(raw, ":")
	if colon <= 0 {
		return nil, "before: the last request as ts:id"
	}
	digits, id := raw[:colon], raw[colon+1:]
	if id == "" || !onlyDigitsOf(digits) {
		return nil, "before: the last request as ts:id"
	}
	ts, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return nil, "before: the last request as ts:id"
	}
	return &history.RequestCursor{Ts: ts, ID: id}, ""
}

// onlyDigitsOf reports whether s is one or more ASCII digits.
func onlyDigitsOf(s string) bool {
	if s == "" {
		return false
	}
	for at := 0; at < len(s); at++ {
		if s[at] < '0' || s[at] > '9' {
			return false
		}
	}
	return true
}

// historyRequests answers one page of a node's or a tree's requests, newest first, with
// prices and the keyset cursor.
func historyRequests(h *history.History, query url.Values) (ApiResponse, error) {
	nodeID := strParam(query, "node")
	rootID := strParam(query, "root")
	if nodeID == nil && rootID == nil {
		return errorOf(400, "node or root: name the request's node or its tree"), nil
	}
	limit := clampLimit(query.Get("limit"))
	before, refusal := beforeOf(query.Get("before"))
	if refusal != "" {
		return errorOf(400, refusal), nil
	}
	q := history.RequestsQuery{Limit: limit + 1, Before: before}
	if nodeID != nil {
		q.NodeID = nodeID
	} else {
		q.RootID = rootID
	}
	// one row past the page tells whether a next page exists, so `next` is null exactly at the end
	found, err := h.RequestsOf(q)
	if err != nil {
		return ApiResponse{}, err
	}
	more := int64(len(found)) > limit
	requests := found
	if more {
		requests = found[:limit]
	}
	page := make([]costedRequest, 0, len(requests))
	for _, request := range requests {
		var price *cost.PriceResult
		if figured := cost.RequestCost(pricedOfRecord(request)); figured != nil {
			price = figured
		}
		page = append(page, costedRequest{RequestRecord: request, Cost: price})
	}
	var next *string
	if more && len(requests) > 0 {
		last := fmt.Sprintf("%d:%s", requests[len(requests)-1].Ts, requests[len(requests)-1].ID)
		next = &last
	}
	return jsonOf(200, requestsPageAnswer{Requests: page, Next: next}), nil
}

// The content cap: a content response never carries more than a megabyte of text; longer
// blocks are cut with a marker.
const (
	maxContentBytes = 1024 * 1024
	minBlockBytes   = 256
	markerReserve   = 64 // room for the "…[truncated N bytes]" marker itself
	capGuardRounds  = 4096
)

// cutText is the cap's rule at any budget: cut on a UTF-16 boundary, then name the bytes
// that did not fit. The first cut takes keepBytes code units, then whole units come off the
// end until the bytes fit — a boundary inside a surrogate pair keeps the lone unit it cuts
// out, so a later cut of the same text still counts it as one unit.
func cutText(text string, keepBytes int64) string {
	if int64(len(text)) <= keepBytes {
		return text
	}
	units := wtf16Units(text)
	keep := min(max(int64(0), keepBytes), int64(len(units)))
	kept := units[:keep]
	keptBytes := unitsByteLen(kept)
	for len(kept) > 0 && keptBytes > keepBytes {
		keptBytes -= lastUnitByteCost(kept)
		kept = kept[:len(kept)-1]
	}
	out := unitsText(kept)
	return fmt.Sprintf("%s…[truncated %d bytes]", out, len(text)-len(out))
}

// lastUnitByteCost is what dropping the run's last code unit takes off its byte length: a
// pair's low half leaves its high alone (the pair's four bytes become the lone unit's
// three), a lone surrogate costs its WTF-8 form, anything else its own encoding.
func lastUnitByteCost(units []uint16) int64 {
	last := units[len(units)-1]
	if isLowSurrogate(last) && len(units) >= 2 && isHighSurrogate(units[len(units)-2]) {
		return 1
	}
	return int64(unitByteLen(last))
}

// wtf16Units lists text's UTF-16 code units; a WTF-8 encoded lone surrogate — what an
// earlier cut can leave behind — counts as the one unit it is.
func wtf16Units(text string) []uint16 {
	units := make([]uint16, 0, len(text))
	for at := 0; at < len(text); {
		if at+3 <= len(text) && text[at] == 0xED && text[at+1] >= 0xA0 && text[at+1] <= 0xBF &&
			text[at+2] >= 0x80 && text[at+2] <= 0xBF {
			units = append(units, 0xD000|uint16(text[at+1]&0x3F)<<6|uint16(text[at+2]&0x3F))
			at += 3
			continue
		}
		r, size := utf8.DecodeRuneInString(text[at:])
		if r > 0xFFFF {
			hi, lo := utf16.EncodeRune(r)
			units = append(units, uint16(hi), uint16(lo))
		} else {
			units = append(units, uint16(r))
		}
		at += size
	}
	return units
}

// isHighSurrogate and isLowSurrogate name the halves of a surrogate pair.
func isHighSurrogate(unit uint16) bool { return unit >= 0xD800 && unit <= 0xDBFF }

func isLowSurrogate(unit uint16) bool { return unit >= 0xDC00 && unit <= 0xDFFF }

// unitByteLen is one code unit's own encoding: a lone surrogate takes the 3 bytes of its
// WTF-8 form (the replacement a JavaScript byte length reads it as), a pair's halves take
// 2 bytes each and re-join into the rune's 4.
func unitByteLen(unit uint16) int {
	switch {
	case isHighSurrogate(unit) || isLowSurrogate(unit):
		return 3
	case unit < 0x80:
		return 1
	case unit < 0x800:
		return 2
	default:
		return 3
	}
}

// unitsByteLen is the encoded size of a run of code units, pairs joined.
func unitsByteLen(units []uint16) int64 {
	var total int64
	for at := 0; at < len(units); at++ {
		if at+1 < len(units) {
			if _, ok := utf16DecodePair(units[at], units[at+1]); ok {
				total += 4 // the pair's rune
				at++
				continue
			}
		}
		total += int64(unitByteLen(units[at]))
	}
	return total
}

// unitsText encodes code units back to text: a surrogate pair becomes its rune, a lone
// half keeps its WTF-8 form.
func unitsText(units []uint16) string {
	out := make([]byte, 0, 4*len(units))
	for at := 0; at < len(units); at++ {
		if at+1 < len(units) {
			if r, ok := utf16DecodePair(units[at], units[at+1]); ok {
				out = utf8.AppendRune(out, r)
				at++
				continue
			}
		}
		out = appendWtf8Unit(out, units[at])
	}
	return string(out)
}

// utf16DecodePair decodes a surrogate pair; ok is false when the two units are not a pair.
func utf16DecodePair(hi, lo uint16) (rune, bool) {
	if hi >= 0xD800 && hi <= 0xDBFF && lo >= 0xDC00 && lo <= 0xDFFF {
		return (rune(hi-0xD800)<<10 | rune(lo-0xDC00)) + 0x10000, true
	}
	return 0, false
}

// appendWtf8Unit encodes one code unit; a lone surrogate takes its WTF-8 form.
func appendWtf8Unit(out []byte, unit uint16) []byte {
	switch {
	case unit < 0x80:
		return append(out, byte(unit))
	case unit < 0x800:
		return append(out, byte(0xC0|unit>>6), byte(0x80|unit&0x3F))
	default:
		return append(out, byte(0xE0|unit>>12), byte(0x80|unit>>6&0x3F), byte(0x80|unit&0x3F))
	}
}

// unitBytesJSON is what one block costs the response: its JSON.
func unitBytesJSON(value *jsfmt.Value) int64 {
	if value.Kind() == jsfmt.KindString {
		// its quotes
		return int64(len(value.Text()) + 2)
	}
	return int64(len(value.EncodeJSON()))
}

// oneSide is one stored side as the cap reads it: nothing, a plain text, or blocks.
type oneSide struct {
	text   *string
	blocks []*jsfmt.Value
}

// sideBytesOf is what one side costs the response: "null", a text's quotes, or the JSON
// punctuation around blocks.
func sideBytesOf(side oneSide) int64 {
	if side.text != nil {
		return int64(len(*side.text) + 2)
	}
	if side.blocks == nil {
		return 4 // "null"
	}
	total := int64(2) // the brackets
	for _, block := range side.blocks {
		total += unitBytesJSON(block) + 1 // a comma
	}
	return total
}

// contentUnit is one shrinkable unit: a block of a block side, or a whole plain-text side,
// with its wire size.
type contentUnit struct {
	side  int
	at    int
	bytes int64
}

// largestUnit answers the biggest unit left across both sides, nil when neither side holds
// one.
func largestUnit(sides []oneSide) *contentUnit {
	var best *contentUnit
	consider := func(bytes int64, side, at int) {
		if best == nil || best.bytes < bytes {
			best = &contentUnit{side: side, at: at, bytes: bytes}
		}
	}
	for sideAt, side := range sides {
		if side.text != nil {
			consider(int64(len(*side.text)), sideAt, 0)
			continue
		}
		for blockAt, block := range side.blocks {
			consider(unitBytesJSON(block), sideAt, blockAt)
		}
	}
	return best
}

// withTextField answers one object value with its text field replaced in place, the other
// fields and their order untouched.
func withTextField(value *jsfmt.Value, key, text string) *jsfmt.Value {
	keys, fields := value.Keys(), value.Fields()
	next := make([]*jsfmt.Value, len(fields))
	for at, field := range fields {
		next[at] = field
		if keys[at] == key {
			next[at] = jsfmt.String(text)
		}
	}
	return jsfmt.Object(keys, next)
}

// shaveUnit cuts one oversized unit down to about half its bytes, marker included; a block
// with no text of its own (a giant tool input) becomes a text block carrying a cut of its
// JSON.
func shaveUnit(value *jsfmt.Value, bytes int64) *jsfmt.Value {
	keep := keepOf(bytes)
	if value.Kind() == jsfmt.KindString {
		return jsfmt.String(cutText(value.Text(), keep))
	}
	if value.Kind() == jsfmt.KindObject {
		if field := fieldOf(value, "text"); field != nil && field.Kind() == jsfmt.KindString {
			return withTextField(value, "text", cutText(field.Text(), keep))
		}
	}
	return jsfmt.Object(
		[]string{"type", "text"},
		[]*jsfmt.Value{jsfmt.String("text"), jsfmt.String(cutText(value.EncodeJSON(), keep))},
	)
}

// cutUnit cuts one unit of one side in place.
func cutUnit(side *oneSide, unit contentUnit) {
	if side.text != nil {
		if unit.at == 0 {
			*side.text = cutText(*side.text, keepOf(unit.bytes))
		}
		return
	}
	if unit.at < len(side.blocks) {
		side.blocks[unit.at] = shaveUnit(side.blocks[unit.at], unit.bytes)
	}
}

// capSides holds both sides to the cap by repeatedly cutting the biggest unit left; a text
// side counts as one unit.
func capSides(sides []oneSide) {
	totalOf := func() int64 {
		var total int64
		for _, side := range sides {
			total += sideBytesOf(side)
		}
		return total
	}
	for round := 0; round < capGuardRounds && totalOf() > maxContentBytes; round++ {
		unit := largestUnit(sides)
		if unit == nil || unit.bytes <= minBlockBytes {
			return // nothing sizeable left; leave the rest
		}
		cutUnit(&sides[unit.side], *unit)
	}
}

// sideValue answers one side as the response carries it: null, the plain text, or the
// blocks.
func sideValue(side oneSide) any {
	if side.text != nil {
		return shared.JSON{Value: jsfmt.String(*side.text)}
	}
	if side.blocks == nil {
		return nil
	}
	return shared.JSON{Value: jsfmt.Array(side.blocks)}
}

// sideOf parses one stored side as the dashboard reads it: its blocks back from JSON, or
// the plain text it was.
func sideOf(text *string) oneSide {
	if text == nil {
		return oneSide{}
	}
	if value, err := jsfmt.DecodeValue([]byte(*text)); err == nil && value.Kind() == jsfmt.KindArray {
		return oneSide{blocks: value.Items()}
	}
	// a side that never was JSON comes back as the text it is
	return oneSide{text: text}
}

// historyContent answers one request's stored sides, capped at a megabyte.
func historyContent(h *history.History, rawID string) (ApiResponse, error) {
	id := decodeOf(rawID)
	if id == nil {
		return errorOf(400, "id is not valid URI encoding"), nil
	}
	if *id == "" {
		return errorOf(404, "missing request id"), nil
	}
	stored, err := h.Content(*id)
	if err != nil {
		return ApiResponse{}, err
	}
	if stored == nil {
		return errorOf(404, "unknown request: "+*id), nil
	}
	sides := []oneSide{sideOf(stored.Input), sideOf(stored.Output)}
	capSides(sides)
	return jsonOf(200, contentAnswer{
		Input:  sideValue(sides[0]),
		Output: sideValue(sides[1]),
		Bytes:  stored.Bytes,
	}), nil
}

// The flow chart: the window's requests and token kinds as bars, one SQL GROUP BY.
const defaultFlowBuckets = 60

// historyFlow answers the token flow over one window, zero-filled.
func historyFlow(h *history.History, query url.Values) (ApiResponse, error) {
	from := numParam(query, "from")
	if from == nil {
		return errorOf(400, "from: epoch ms the window starts at"), nil
	}
	to := numParam(query, "to")
	if to == nil {
		return errorOf(400, "to: epoch ms the window ends at"), nil
	}
	if *from >= *to {
		return errorOf(400, "from must be before to"), nil
	}
	target := int64(defaultFlowBuckets)
	if asked, ok := parseInt10(query.Get("buckets")); ok && asked > 0 {
		target = asked
	}
	plan := shared.BucketPlan(*from, *to, target)
	series, err := h.Flow(history.FlowQuery{From: *from, To: *to, BucketMs: plan.BucketMs, Buckets: plan.Buckets})
	if err != nil {
		return ApiResponse{}, err
	}
	return jsonOf(200, series), nil
}

// A context page holds this many messages unless the reader asks for another count.
const (
	contextDefaultLimit = 40
	contextMaxLimit     = 200
)

// contextQuery reads the page query the conversation reads share: the clamped limit and the
// well-formed cursor.
func contextQuery(query url.Values) (history.ContextQuery, *ApiResponse) {
	rawCursor := query.Get("cursor")
	if rawCursor != "" && !cursorShaped(rawCursor) {
		refusal := errorOf(400, "cursor: the page's oldest message as ts:requestId:side")
		return history.ContextQuery{}, &refusal
	}
	limit := int64(contextDefaultLimit)
	if asked := numParam(query, "limit"); asked != nil {
		limit = min(max(*asked, 1), contextMaxLimit)
	}
	parsed := history.ContextQuery{Limit: limit}
	if rawCursor != "" {
		parsed.Cursor = &rawCursor
	}
	return parsed, nil
}

// cursorShaped reports whether a cursor reads as "<digits>:<id>:<0 or 1>", the id anything.
func cursorShaped(raw string) bool {
	first := strings.Index(raw, ":")
	last := strings.LastIndex(raw, ":")
	if first <= 0 || last < first+2 {
		return false
	}
	if !onlyDigitsOf(raw[:first]) {
		return false
	}
	side := raw[last+1:]
	return side == "0" || side == "1"
}

// textHold is one place a block carries text: the message, the block, the field and the
// text's byte size.
type textHold struct {
	message int
	block   int
	key     string
	bytes   int64
}

// textHoldsOf lists where one block carries text; a block without text holds nothing.
func textHoldsOf(block *jsfmt.Value) []textHold {
	if block == nil || block.Kind() != jsfmt.KindObject {
		return nil
	}
	holds := []textHold{}
	for _, key := range []string{"text", "thinking"} {
		if field := fieldOf(block, key); field != nil && field.Kind() == jsfmt.KindString {
			holds = append(holds, textHold{key: key, bytes: int64(len(field.Text()))})
		}
	}
	return holds
}

// messageWireBytes is what one capped message costs the response.
func messageWireBytes(message shared.ContextMessage) int64 {
	total := int64(2) // the brackets
	for _, block := range message.Blocks {
		total += unitBytesJSON(block.Value) + 1 // a comma
	}
	return total
}

// biggestContextText answers the biggest text any block on the page carries, nil when no
// block carries text any more.
func biggestContextText(messages []shared.ContextMessage) *textHold {
	var worst *textHold
	for messageAt := range messages {
		for blockAt := range messages[messageAt].Blocks {
			for _, hold := range textHoldsOf(messages[messageAt].Blocks[blockAt].Value) {
				hold.message, hold.block = messageAt, blockAt
				if worst == nil || worst.bytes < hold.bytes {
					held := hold
					worst = &held
				}
			}
		}
	}
	return worst
}

// capContextPage holds one page's texts to the budget by repeatedly cutting the biggest
// text left; blocks without text of their own (a giant tool input) stay whole, and the page
// stays honest about what was cut.
func capContextPage(messages []shared.ContextMessage) {
	for round := 0; round < capGuardRounds; round++ {
		var total int64
		for _, message := range messages {
			total += messageWireBytes(message)
		}
		if total <= maxContentBytes {
			return
		}
		worst := biggestContextText(messages)
		if worst == nil || worst.bytes <= minBlockBytes {
			return
		}
		block := messages[worst.message].Blocks[worst.block].Value
		cut := cutText(fieldOf(block, worst.key).Text(), keepOf(worst.bytes))
		messages[worst.message].Blocks[worst.block] = shared.JSON{
			Value: withTextField(block, worst.key, cut),
		}
	}
}

// keepOf is the byte budget one shave leaves the text it cuts.
func keepOf(bytes int64) int64 {
	return max(int64(minBlockBytes), bytes/2-markerReserve)
}

// historyContext answers the conversation a request sat in, rebuilt from the stored sides of
// its node's requests.
func historyContext(h *history.History, rawID string, query url.Values) (ApiResponse, error) {
	return historyLookup(rawID, query, "request", h.Context)
}

// historyLookup runs one context-shaped route over a lookup of the history: decode the id,
// refuse an empty one with the noun, parse the page query, call the lookup, and answer the
// noun's 404 when it holds nothing. The noun is "request" or "agent"; it builds both 404
// texts.
func historyLookup(rawID string, query url.Values, noun string,
	lookup func(string, history.ContextQuery) (*shared.ContextAnswer, error)) (ApiResponse, error) {
	id := decodeOf(rawID)
	if id == nil {
		return errorOf(400, "id is not valid URI encoding"), nil
	}
	if *id == "" {
		return errorOf(404, "missing "+noun+" id"), nil
	}
	parsed, refusal := contextQuery(query)
	if refusal != nil {
		return *refusal, nil
	}
	found, err := lookup(*id, parsed)
	if err != nil {
		return ApiResponse{}, err
	}
	if found == nil {
		return errorOf(404, "unknown "+noun+": "+*id), nil
	}
	capContextPage(found.Messages)
	return jsonOf(200, notedContext{ContextAnswer: *found, Note: ContextNote}), nil
}

// historyAgentTranscript answers an agent's whole conversation, keyed by its history node —
// the context route's shape, with the newest answer included. A node with no stored
// requests answers an empty page, not a 404.
func historyAgentTranscript(h *history.History, rawID string, query url.Values) (ApiResponse, error) {
	return historyLookup(rawID, query, "agent", h.AgentTranscript)
}

// historyCapture answers the request's stored prompt capture, decompressed on read, with
// the headers its route line recorded.
func historyCapture(h *history.History, rawID string) (ApiResponse, error) {
	id := decodeOf(rawID)
	if id == nil {
		return errorOf(400, "id is not valid URI encoding"), nil
	}
	if *id == "" {
		return errorOf(404, "missing request id"), nil
	}
	found, err := h.Capture(*id)
	if err != nil {
		return ApiResponse{}, err
	}
	if found == nil {
		return errorOf(404, "no capture for request: "+*id), nil
	}
	return jsonOf(200, found), nil
}

// captureIdOf answers the request id a capture path carries, nil when the path is not one;
// the id is everything between the prefix and the suffix, slashes included.
func captureIdOf(path string) *string {
	return suffixedIdOf(path, "/api/history/request/", "/capture")
}

// agentTranscriptIdOf answers the node id an agent-transcript path carries, nil when the
// path is not one. The id may hold slashes — subagent nodes are "<session>/<agentId>" — so
// both ends are matched and everything between them is the id.
func agentTranscriptIdOf(path string) *string {
	return suffixedIdOf(path, "/api/history/agent/", "/transcript")
}

// suffixedIdOf answers what sits between a prefix and a suffix of the path.
func suffixedIdOf(path, prefix, suffix string) *string {
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return nil
	}
	id := path[len(prefix) : len(path)-len(suffix)]
	return &id
}

// historySuffixed answers the reads whose id sits between a prefix and a suffix: a
// request's capture, an agent's transcript.
func historySuffixed(h *history.History, path string, query url.Values) (ApiResponse, bool, error) {
	if capture := captureIdOf(path); capture != nil {
		answer, err := historyCapture(h, *capture)
		return answer, true, err
	}
	if agent := agentTranscriptIdOf(path); agent != nil {
		answer, err := historyAgentTranscript(h, *agent, query)
		return answer, true, err
	}
	return ApiResponse{}, false, nil
}

// historyRoute answers the history reads; false when the path is not one of them.
func historyRoute(h *history.History, insights InsightsView, path string, query url.Values, live func() []string, now int64) (ApiResponse, bool, error) {
	switch path {
	case "/api/history/roots":
		answer, err := historyRoots(h, query, live(), now)
		return answer, true, err
	case "/api/history/requests":
		answer, err := historyRequests(h, query)
		return answer, true, err
	case "/api/history/repos":
		repos, err := h.Repos()
		if err != nil {
			return ApiResponse{}, true, err
		}
		rows := make([]repoRow, 0, len(repos))
		for _, entry := range repos {
			rows = append(rows, repoRow{Repo: entry.Repo, Roots: entry.Roots})
		}
		return jsonOf(200, reposAnswer{Repos: rows}), true, nil
	case "/api/history/flow":
		answer, err := historyFlow(h, query)
		return answer, true, err
	case "/api/history/stats":
		q := history.StatsQuery{Now: now, LiveMs: HistoryLiveMs}
		if ids := live(); len(ids) > 0 {
			q.ExcludeSessions = ids
		}
		stats, err := h.Stats(q)
		if err != nil {
			return ApiResponse{}, true, err
		}
		retention := settings.DefaultSettings.HistoryRetentionDays
		if insights != nil {
			retention = insights.Settings().HistoryRetentionDays
		}
		return jsonOf(200, statsAnswer{
			Bytes:         stats.Bytes,
			Nodes:         stats.Nodes,
			Requests:      stats.Requests,
			Roots:         stats.Roots,
			RetentionDays: retention,
		}), true, nil
	}
	if id, ok := strings.CutPrefix(path, "/api/history/tree/"); ok {
		answer, err := historyTree(h, id, now)
		return answer, true, err
	}
	if id, ok := strings.CutPrefix(path, "/api/history/content/"); ok {
		answer, err := historyContent(h, id)
		return answer, true, err
	}
	if id, ok := strings.CutPrefix(path, "/api/history/context/"); ok {
		answer, err := historyContext(h, id, query)
		return answer, true, err
	}
	return historySuffixed(h, path, query)
}

// historyPaths are the reads the exact-match table serves.
var historyPaths = map[string]struct{}{
	"/api/history/roots":    {},
	"/api/history/requests": {},
	"/api/history/repos":    {},
	"/api/history/flow":     {},
	"/api/history/stats":    {},
}

// isHistoryPath reports whether the path is one of the history reads.
func isHistoryPath(path string) bool {
	if _, ok := historyPaths[path]; ok {
		return true
	}
	return strings.HasPrefix(path, "/api/history/tree/") ||
		strings.HasPrefix(path, "/api/history/content/") ||
		strings.HasPrefix(path, "/api/history/context/") ||
		(strings.HasPrefix(path, "/api/history/request/") && strings.HasSuffix(path, "/capture")) ||
		(strings.HasPrefix(path, "/api/history/agent/") && strings.HasSuffix(path, "/transcript"))
}

// historyAnswer answers the /api/history reads, the 503 included; false when the path is
// not one of them.
func historyAnswer(h *history.History, insights InsightsView, path string, query url.Values, live func() []string, now int64) (ApiResponse, bool, error) {
	if !isHistoryPath(path) {
		return ApiResponse{}, false, nil
	}
	if h == nil {
		return errorOf(503, "history unavailable"), true, nil
	}
	return historyRoute(h, insights, path, query, live, now)
}

// sessionRoute answers one session's detail; false when the path is not a session id.
func sessionRoute(st *store.Store, path string) (ApiResponse, bool) {
	raw, ok := strings.CutPrefix(path, "/api/sessions/")
	if !ok {
		return ApiResponse{}, false
	}
	id, err := url.PathUnescape(raw)
	if err != nil {
		// a malformed escape cannot name a session; the raw text answers for it
		id = raw
	}
	if id == "" {
		return errorOf(404, "missing session id"), true
	}
	session := st.SessionDetail(id)
	if session == nil {
		return errorOf(404, "unknown session: "+id), true
	}
	return jsonOf(200, sessionAnswer{Session: session}), true
}

// HandleApi routes one request; unknown /api paths get a 404 with the known routes listed.
func HandleApi(st *store.Store, info AppInfo, request ApiRequest, insights InsightsView, quotas *Quotas, h *history.History, now func() time.Time) (ApiResponse, error) {
	if now == nil {
		now = time.Now
	}
	path, query := request.Path, request.Query
	if path == "/api/quota" {
		return HandleQuotaRead(quotas), nil
	}
	if insights != nil {
		if derived, ok := insightRoute(insights, path, query); ok {
			return derived, nil
		}
	}
	historic, ok, err := historyAnswer(h, insights, path, query, func() []string { return liveSessionIds(st) }, now().UnixMilli())
	if err != nil {
		return ApiResponse{}, err
	}
	if ok {
		return historic, nil
	}
	switch {
	case path == "/api/summary":
		return jsonOf(200, summaryAnswer{Summary: st.Summary()}), nil
	case path == "/api/sessions":
		return jsonOf(200, sessionsAnswer{Sessions: store.SessionTree(st.SessionList())}), nil
	case path == "/api/usage":
		if h == nil {
			return errorOf(503, "history unavailable"), nil
		}
		return usageRoute(h, query, now)
	case path == "/api/usage/today":
		if h == nil {
			return errorOf(503, "history unavailable"), nil
		}
		return todayRoute(h, now)
	case path == "/api/blocks":
		if h == nil {
			return errorOf(503, "history unavailable"), nil
		}
		return blocksRoute(h, query, now)
	}
	if session, ok := sessionRoute(st, path); ok {
		return session, nil
	}
	if listed, ok := listRoute(st, path, query); ok {
		return listed, nil
	}
	if path == "/api/models" {
		return modelsRoute(st, query), nil
	}
	if path == "/api/health" {
		return jsonOf(200, healthAnswer{
			OK:        true,
			Version:   info.Version,
			StartedAt: info.StartedAt,
			Port:      info.Port,
			Sessions:  int64(st.SessionCount()),
			UptimeMs:  now().UnixMilli() - info.StartedAt,
		}), nil
	}
	return jsonOf(404, notFoundAnswer{Error: "no such route: " + path, Routes: Routes}), nil
}

// fieldOf answers an object's field, nil when the object does not carry it.
func fieldOf(object *jsfmt.Value, key string) *jsfmt.Value {
	if object == nil || object.Kind() != jsfmt.KindObject {
		return nil
	}
	fields := object.Fields()
	for at, name := range object.Keys() {
		if name == key && at < len(fields) {
			return fields[at]
		}
	}
	return nil
}

// ptrOf points at one value.
func ptrOf[T any](value T) *T { return &value }

// derefText answers the text a nullable string holds, empty when it holds none.
func derefText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// derepOf answers the value a pointer holds, or the fallback when it is nil.
func derepOf[T any](value *T, fallback T) T {
	if value == nil {
		return fallback
	}
	return *value
}

// derefCount answers the counter a nullable count holds, zero when it holds none.
func derefCount(value *int64) int64 { return derepOf(value, 0) }
