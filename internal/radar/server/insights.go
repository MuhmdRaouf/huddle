package server

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/safego"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/radar/alerts"
	"github.com/MuhmdRaouf/huddle/internal/radar/budget"
	"github.com/MuhmdRaouf/huddle/internal/radar/cost"
	"github.com/MuhmdRaouf/huddle/internal/radar/routerhealth"
	"github.com/MuhmdRaouf/huddle/internal/radar/settings"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

// DefaultTickMs is the cadence the server flushes insights on.
const DefaultTickMs = 10_000

// alertCacheMs is how long one alert answer holds while nothing it was built from moves.
const alertCacheMs = 2000

// The router health view the dashboard serves: one day, folded hourly.
const (
	routerHealthWindowMs = 86_400_000
	routerHealthBuckets  = 24
)

// dayMs is one day in the epoch milliseconds every timestamp counts.
const dayMs = 86_400_000

// InsightsStore is the store insights reads: the whole record set for the detectors, the
// session views for names, the side records for router health, and the change feed that
// keeps the ledger current. *store.Store satisfies it; tests wrap it to count reads.
type InsightsStore interface {
	RequestsFor(filter store.RequestFilter) []shared.RequestRecord
	OnUpdate(listener func(store.Change)) func()
	Version() int64
	Raw() store.RawRecords
	SessionList() []store.SessionListItem
	SessionDetail(id string) *shared.SessionView
	APIErrors() []shared.APIErrorRecord
	RouterEvents() []shared.RouterEventRecord
}

// InsightsOptions shapes one Insights.
type InsightsOptions struct {
	Env   domain.EnvLookup
	Store InsightsStore
	Now   func() time.Time
	// Zone names the hub's zone, read on every call, for the day spend and attribution start
	// at; nil counts UTC days.
	Zone func() *time.Location
	// TickMs is the flush cadence; zero leaves the timer off (tests tick themselves).
	TickMs     int64
	Notifier   *alerts.Notifier
	Runner     alerts.Runner
	Thresholds *alerts.Thresholds
	// Quotas holds the statusline's plan-limits snapshots, the source of Claude's 5-hour
	// window alert; nil raises none.
	Quotas *Quotas
	// Watcher is the ingest backlog word; while the first pass is still draining, its
	// half-filled store can read as an alert worth a notification, so desktop notifications
	// are held until it says the backlog is gone. Nil never holds.
	Watcher Watcher
}

// alertCacheState is one held alert answer, with the versions it was built at.
type alertCacheState struct {
	at      int64
	version int64
	ledger  int64
	quota   int64
	alerts  []shared.Alert
}

// advisorCacheState is one advisor report, per store version.
type advisorCacheState struct {
	version int64
	report  cost.AdvisorReport
}

// metricsCacheState is one exposition, per store version, ledger version and radar version.
type metricsCacheState struct {
	version    int64
	ledger     int64
	forVersion string
	text       string
}

// Insights is everything the dashboard derives beyond the raw store, wired once per
// server: the usage ledger (fed from the store), budgets and their status file, the alerts
// engine with dismissals, desktop notifications, attribution, router health, the model
// advisor and the metrics text. The advisor and the metrics text are cached per store
// version, so a scrape or a repeated view costs no copy of the store. Nothing here blocks
// or fails into the request path: a broken file is the defaults, a failed write is retried
// on the next tick.
type Insights struct {
	ctx        context.Context
	env        domain.EnvLookup
	store      InsightsStore
	nowFn      func() time.Time
	zoneFn     func() *time.Location
	thresholds alerts.Thresholds

	mu          sync.Mutex
	ledger      *cost.Ledger
	budgets     []budget.Budget
	settings    settings.Settings
	dismissed   []settings.Dismissal
	alertCache  *alertCacheState
	advisor     *advisorCacheState
	metrics     *metricsCacheState
	namedAgents map[string]struct{}

	notifier    *alerts.Notifier
	quotas      *Quotas
	watcher     Watcher
	unsubscribe func()
	stop        chan struct{}
	stopOnce    sync.Once
}

// CreateInsights builds one Insights and runs its first tick at once, so names, the ledger
// file and budget-status.json are there from the first moment; after that a timer (TickMs,
// DefaultTickMs when the caller names nothing) flushes every 10 s.
func CreateInsights(ctx context.Context, options InsightsOptions) *Insights {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	thresholds := alerts.DefaultThresholds
	if options.Thresholds != nil {
		thresholds = *options.Thresholds
	}
	env := options.Env
	if env == nil {
		env = domain.EnvLookup{}
	}
	i := &Insights{
		ctx:         ctx,
		env:         env,
		store:       options.Store,
		nowFn:       now,
		zoneFn:      options.Zone,
		thresholds:  thresholds,
		budgets:     budget.ReadBudgets(ctx, env),
		settings:    settings.ReadSettings(ctx, env),
		dismissed:   settings.ReadDismissed(ctx, env, now().UnixMilli()),
		namedAgents: map[string]struct{}{},
		quotas:      options.Quotas,
		watcher:     options.Watcher,
		stop:        make(chan struct{}),
	}
	i.ledger = cost.NewLedger(ctx, env, now)
	notifier := options.Notifier
	if notifier == nil {
		notifier = alerts.NewNotifier(ctx, alerts.NotifierOptions{
			Env:     env,
			Enabled: i.notificationsOn,
			Runner:  options.Runner,
			Now:     now,
		})
	}
	i.notifier = notifier
	for _, request := range options.Store.RequestsFor(store.RequestFilter{}) {
		i.ledger.Record(request)
	}
	i.unsubscribe = options.Store.OnUpdate(func(change store.Change) {
		i.mu.Lock()
		defer i.mu.Unlock()
		for _, request := range change.Requests {
			i.ledger.Record(request)
		}
	})
	i.Tick(ctx)
	if options.TickMs > 0 {
		ticker := time.NewTicker(time.Duration(options.TickMs) * time.Millisecond)
		safego.Go(nil, func() {
			defer ticker.Stop()
			for {
				select {
				case <-i.ctx.Done():
					return
				case <-i.stop:
					return
				case <-ticker.C:
					i.Tick(i.ctx)
				}
			}
		})
	}
	return i
}

// notificationsOn is the switch the notifier reads on every call: the live preference.
func (i *Insights) notificationsOn() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.settings.Notifications
}

// nowMs is the clock every window reads.
func (i *Insights) nowMs() int64 { return i.nowFn().UnixMilli() }

// zone is the hub's zone; nil is UTC.
func (i *Insights) zone() *time.Location {
	if i.zoneFn == nil {
		return nil
	}
	return i.zoneFn()
}

// ledgerRowsLocked is every ledger row inside its keep window.
func (i *Insights) ledgerRowsLocked() []cost.LedgerRow {
	return i.ledger.Rows(i.nowMs() - cost.LedgerDays*dayMs)
}

// statusLocked is the whole budget status document, built from the current rows.
func (i *Insights) statusLocked() budget.BudgetStatus {
	return budget.ComputeStatus(i.budgets, i.ledgerRowsLocked(), i.nowMs())
}

// allAlertsLocked detects every active alert, serving the held answer while it is young
// enough and nothing it was built from has moved.
func (i *Insights) allAlertsLocked() []shared.Alert {
	at := i.nowMs()
	version := i.store.Version()
	window, quotaAt := i.claudeWindow()
	if held := i.alertCache; held != nil && at-held.at < alertCacheMs &&
		held.version == version && held.ledger == i.ledger.Version() && held.quota == quotaAt {
		return held.alerts
	}
	raw := i.store.Raw()
	spend := []budget.BudgetSpend{}
	if len(i.budgets) > 0 {
		spend = i.statusLocked().Spend
	}
	detected := alerts.DetectAlerts(alerts.AlertInput{
		Now:          at,
		Sessions:     i.store.SessionList(),
		Requests:     raw.Requests,
		Tools:        raw.Tools,
		Events:       raw.Events,
		APIErrors:    i.store.APIErrors(),
		Budgets:      spend,
		RouterEvents: i.store.RouterEvents(),
		ClaudeWindow: window,
		PluginSkew:   alerts.ReadPluginSkew(i.ctx, i.env),
	}, i.thresholds)
	i.alertCache = &alertCacheState{
		at: at, version: version, ledger: i.ledger.Version(), quota: quotaAt, alerts: detected,
	}
	return detected
}

// claudeWindow is Claude's 5-hour window off the newest plan-limits snapshot, and that
// snapshot's time; nil when no snapshot names both the used share and the reset.
func (i *Insights) claudeWindow() (*alerts.ClaudeWindow, int64) {
	if i.quotas == nil {
		return nil, 0
	}
	snapshot, ok := i.quotas.Newest()
	if !ok || snapshot.FiveHour == nil || snapshot.FiveHourResetsAt == nil {
		return nil, snapshot.At
	}
	return &alerts.ClaudeWindow{
		UsedPct:  *snapshot.FiveHour,
		ResetsAt: *snapshot.FiveHourResetsAt * 1000,
	}, snapshot.At
}

// nameThingsLocked remembers what the store's sessions and agents are called, so old ledger
// rows keep a readable label. A session's agents are named while it runs and once more
// after it ends; what a name once was, it stays.
func (i *Insights) nameThingsLocked() {
	for _, session := range i.store.SessionList() {
		i.ledger.NameSession(session.ID, cost.SessionNames{
			Project:         session.Project,
			Title:           session.Title,
			Repo:            session.Repo,
			ParentSessionID: session.ParentSessionID,
			Name:            session.Name,
		})
		if !session.Live {
			if _, named := i.namedAgents[session.ID]; named {
				continue
			}
		}
		i.namedAgents[session.ID] = struct{}{}
		i.nameAgentsLocked(session.ID)
	}
}

// nameAgentsLocked names one session's agents in the ledger: a subagent's display name,
// its type and its task — never the main agent, whose name is always itself.
func (i *Insights) nameAgentsLocked(sessionID string) {
	detail := i.store.SessionDetail(sessionID)
	if detail == nil {
		return
	}
	for _, agent := range detail.Agents {
		named := agent.Name != nil && *agent.Name != agent.ID
		if agent.ID == "main" || !named {
			continue
		}
		name := agent.ID
		if agent.Name != nil {
			name = *agent.Name
		}
		i.ledger.NameAgent(sessionID, agent.ID, cost.AgentNames{
			Name:        name,
			Type:        agent.AgentType,
			Description: agent.Description,
		})
	}
}

// namesLocked is the ledger's names with what the store knows right now laid over them (it
// may not have ticked yet).
func (i *Insights) namesLocked() cost.LedgerNames {
	known := i.ledger.Names()
	for _, session := range i.store.SessionList() {
		previous := known.Sessions[session.ID]
		known.Sessions[session.ID] = cost.SessionNames{
			Project:         pickPtr(session.Project, previous.Project),
			Title:           pickPtr(session.Title, previous.Title),
			Repo:            pickPtr(session.Repo, previous.Repo),
			ParentSessionID: pickPtr(session.ParentSessionID, previous.ParentSessionID),
			Name:            pickPtr(session.Name, previous.Name),
		}
	}
	return known
}

// Ledger is the usage ledger, kept current with the store.
func (i *Insights) Ledger() *cost.Ledger { return i.ledger }

// Alerts lists the active alerts the user has not dismissed.
func (i *Insights) Alerts() []shared.Alert {
	i.mu.Lock()
	defer i.mu.Unlock()
	active := []shared.Alert{}
	for _, alert := range i.allAlertsLocked() {
		if !i.dismissedHasLocked(alert.ID) {
			active = append(active, alert)
		}
	}
	return active
}

// dismissedHasLocked says whether one alert id sits in the dismissal list.
func (i *Insights) dismissedHasLocked(id string) bool {
	for _, entry := range i.dismissed {
		if entry.ID == id {
			return true
		}
	}
	return false
}

// Dismiss silences one alert by id until the same thing happens again; an id that is empty
// or longer than 300 UTF-16 units is refused. A failed file write leaves the dismissal
// holding for this run; the file catches up on the next dismissal.
func (i *Insights) Dismiss(id string) bool {
	if id == "" || jsfmt.UTF16Len(id) > 300 {
		return false
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.dismissed = append(i.dismissed, settings.Dismissal{ID: id, At: float64(i.nowMs())})
	_ = settings.WriteDismissed(i.ctx, i.env, i.dismissed)
	return true
}

// Budgets lists the user's budgets.
func (i *Insights) Budgets() []budget.Budget {
	i.mu.Lock()
	defer i.mu.Unlock()
	return slices.Clone(i.budgets)
}

// SetBudgets validates a whole budgets list and saves it; refusal is the sentence the
// dashboard shows next to the form, and the write error is the caller's to answer for.
func (i *Insights) SetBudgets(ctx context.Context, input *jsfmt.Value) ([]budget.Budget, string, error) {
	next, refusal := budget.ValidateBudgets(input)
	if refusal != "" {
		return nil, refusal, nil
	}
	if err := budget.WriteBudgets(ctx, i.env, next); err != nil {
		return nil, "", err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.budgets = next
	i.alertCache = nil
	i.metrics = nil
	if err := budget.WriteStatus(ctx, i.env, i.statusLocked()); err != nil {
		return nil, "", err
	}
	return next, "", nil
}

// BudgetStatus is the whole status document the provider plugins' routers read.
func (i *Insights) BudgetStatus() budget.BudgetStatus {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.statusLocked()
}

// Settings is the dashboard's preferences.
func (i *Insights) Settings() settings.Settings {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.settings
}

// SetSettings validates a partial settings update and saves it; refusal is the sentence
// the dashboard shows, and the write error is the caller's to answer for.
func (i *Insights) SetSettings(ctx context.Context, input *jsfmt.Value) (settings.Settings, string, error) {
	i.mu.Lock()
	current := i.settings
	i.mu.Unlock()
	next, refusal := settings.ValidateSettings(input, current)
	if refusal != "" {
		return settings.Settings{}, refusal, nil
	}
	if err := settings.WriteSettings(ctx, i.env, next); err != nil {
		return settings.Settings{}, "", err
	}
	i.mu.Lock()
	i.settings = next
	i.mu.Unlock()
	return next, "", nil
}

// Attribution groups the ledger rows on one axis over one range.
func (i *Insights) Attribution(by cost.AttributionBy, span cost.AttributionRange) []cost.AttributionRow {
	i.mu.Lock()
	defer i.mu.Unlock()
	return cost.Attribute(i.ledgerRowsLocked(), by, i.namesLocked(), cost.RangeStart(span, i.nowMs(), i.zone()))
}

// Tree is the costs drill-down over the same rows: repos → sessions → agents → models.
func (i *Insights) Tree(span cost.AttributionRange) []cost.AttributionNode {
	i.mu.Lock()
	defer i.mu.Unlock()
	return cost.AttributionTree(i.ledgerRowsLocked(), i.namesLocked(), cost.RangeStart(span, i.nowMs(), i.zone()))
}

// SpendToday is the estimated spend since midnight in the hub's zone and the calls it could
// not price.
func (i *Insights) SpendToday() cost.Spend {
	i.mu.Lock()
	defer i.mu.Unlock()
	return cost.SpendSince(i.ledgerRowsLocked(), cost.RangeStart(cost.RangeDay, i.nowMs(), i.zone()))
}

// Providers lists the provider plugins with requests in the ledger, plus the known ones.
func (i *Insights) Providers() []string {
	i.mu.Lock()
	defer i.mu.Unlock()
	seen := map[string]struct{}{}
	out := make([]string, 0, len(cost.KnownPlugins))
	add := func(name string) {
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	for _, plugin := range cost.KnownPlugins {
		add(plugin)
	}
	for _, row := range i.ledgerRowsLocked() {
		if plugin, ok := cost.PluginOf(row.Model); ok {
			add(plugin)
		}
	}
	for _, event := range i.store.RouterEvents() {
		add(event.Plugin)
	}
	return out
}

// RouterHealth folds the router events into per-provider health over the last day.
func (i *Insights) RouterHealth() routerhealth.RouterHealth {
	return routerhealth.Health(i.store.RouterEvents(), i.nowMs(), routerHealthWindowMs, routerHealthBuckets)
}

// Advisor is the model advisor's findings, one build per store version.
func (i *Insights) Advisor() cost.AdvisorReport {
	i.mu.Lock()
	defer i.mu.Unlock()
	version := i.store.Version()
	if held := i.advisor; held != nil && held.version == version {
		return held.report
	}
	raw := i.store.Raw()
	report := cost.Advise(raw.Requests, raw.Tools)
	i.advisor = &advisorCacheState{version: version, report: report}
	return report
}

// Metrics renders the Prometheus exposition for one radar version, one build per store
// version, ledger version and radar version.
func (i *Insights) Metrics(version string) string {
	i.mu.Lock()
	defer i.mu.Unlock()
	storeVersion := i.store.Version()
	ledgerVersion := i.ledger.Version()
	if held := i.metrics; held != nil && held.forVersion == version &&
		held.version == storeVersion && held.ledger == ledgerVersion {
		return held.text
	}
	raw := i.store.Raw()
	text := RenderMetrics(MetricsInput{
		Version:      version,
		Ledger:       i.ledgerRowsLocked(),
		Names:        i.namesLocked(),
		Requests:     raw.Requests,
		Tools:        raw.Tools,
		APIErrors:    i.store.APIErrors(),
		RouterEvents: i.store.RouterEvents(),
		Alerts:       i.allAlertsLocked(),
		Sessions:     i.store.SessionList(),
		Budgets:      i.statusLocked().Spend,
	})
	i.metrics = &metricsCacheState{version: storeVersion, ledger: ledgerVersion, forVersion: version, text: text}
	return text
}

// Tick names what the store knows, flushes the ledger, rewrites budget-status.json (so
// routers always see a fresh updatedAt) and notifies about new alerts — the notifications
// held while the watcher says an ingest pass is still draining, so a start never raises a
// false alert from a half-filled store. A failure inside costs this tick only; the next one
// tries again.
func (i *Insights) Tick(ctx context.Context) {
	// the backlog word is read on both sides of the alerts, each time outside the lock: the
	// pass itself feeds the store (and so this lock) while holding its own, a pass that
	// ended after the first read must not be notified behind, and a pass that starts between
	// the two reads feeds the very snapshot this tick would notify from — that one waits for
	// the next tick
	holding := i.watcher != nil && i.watcher.CatchingUp()
	i.mu.Lock()
	i.nameThingsLocked()
	i.ledger.Flush(ctx)
	if len(i.budgets) > 0 {
		// a failed status write costs this tick only; the next one retries, and a router
		// reading the stale file stops nothing
		_ = budget.WriteStatus(ctx, i.env, i.statusLocked())
	}
	if holding {
		i.mu.Unlock()
		return
	}
	fresh := i.allAlertsLocked()
	i.mu.Unlock()
	if i.watcher != nil && i.watcher.CatchingUp() {
		return
	}
	i.notifier.Consider(ctx, fresh)
}

// Stop ends the timer and the store subscription, and flushes the ledger.
func (i *Insights) Stop() {
	i.stopOnce.Do(func() {
		close(i.stop)
		if i.unsubscribe != nil {
			i.unsubscribe()
		}
		i.mu.Lock()
		defer i.mu.Unlock()
		i.ledger.Flush(i.ctx)
	})
}

// pickPtr is the first non-nil pointer.
func pickPtr(values ...*string) *string {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}
