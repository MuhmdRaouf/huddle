// Package budget keeps the user's spending budgets (budgets.json), measures spend against
// them from the usage ledger and publishes budget-status.json for the provider plugins'
// routers to read. A budget with action "stop" at or over 100 percent for its current period
// puts its scope in stopped; the routers then refuse that scope's requests (claude-* requests
// never). No file means no budgets; an unreadable file is treated the same, never as an error
// that blocks anything.
package budget

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/radar/clock"
	"github.com/MuhmdRaouf/huddle/internal/radar/cost"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
	"github.com/MuhmdRaouf/huddle/internal/radar/settings"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// Period is a budget's period.
type Period string

const (
	PeriodDay   Period = "day"
	PeriodWeek  Period = "week"
	PeriodMonth Period = "month"
)

// Periods lists the periods a budget can run over, in the order the dashboard offers them.
var Periods = []Period{PeriodDay, PeriodWeek, PeriodMonth}

// Action is what happens when a budget is reached.
type Action string

const (
	ActionWarn Action = "warn"
	ActionStop Action = "stop"
)

// Budget is one spending limit the user set.
type Budget struct {
	ID       string     `json:"id"`
	Scope    string     `json:"scope"`
	Period   Period     `json:"period"`
	LimitUsd shared.USD `json:"limitUsd"`
	Action   Action     `json:"action"`
}

// Percent is a share of a limit (100 = the limit), one decimal.
type Percent float64

// MarshalJSON prints the share exactly as JavaScript's String(n). A limit that passed
// validation but rounded to zero cents makes the share infinite, which JSON.stringify
// writes as null.
func (p Percent) MarshalJSON() ([]byte, error) {
	if math.IsInf(float64(p), 0) || math.IsNaN(float64(p)) {
		return []byte("null"), nil
	}
	return []byte(jsfmt.JSNumber(float64(p))), nil
}

// BudgetSpend is one budget's spend for its current period.
type BudgetSpend struct {
	ID          string     `json:"id"`
	SpentUsd    shared.USD `json:"spentUsd"`
	LimitUsd    shared.USD `json:"limitUsd"`
	Pct         Percent    `json:"pct"`
	Scope       string     `json:"scope"`
	Period      Period     `json:"period"`
	Action      Action     `json:"action"`
	PeriodStart int64      `json:"periodStart"`
}

// BudgetStatus is the whole status document the routers read.
type BudgetStatus struct {
	Version   int           `json:"version"`
	UpdatedAt int64         `json:"updatedAt"`
	Stopped   []string      `json:"stopped"`
	Spend     []BudgetSpend `json:"spend"`
}

// MaxBudgets caps how many budgets one file may hold.
const MaxBudgets = 50

// idPattern and scopePattern are the shapes a budget's id and scope must take.
var (
	idPattern    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)
	scopePattern = regexp.MustCompile(`^(total|provider:[a-z0-9][a-z0-9._-]{0,39})$`)
)

// BudgetsPath is the user's budgets, in radar's state dir.
func BudgetsPath(ctx context.Context, env domain.EnvLookup) string {
	return filepath.Join(paths.StateDir(ctx, env), "budgets.json")
}

// BudgetStatusPath is the status document the provider plugins' routers read, beside the
// budgets it summarizes.
func BudgetStatusPath(ctx context.Context, env domain.EnvLookup) string {
	return filepath.Join(paths.StateDir(ctx, env), "budget-status.json")
}

// ValidateBudget reads one budget from untrusted JSON, or refuses it with a sentence saying
// what is wrong with it.
func ValidateBudget(input *jsfmt.Value) (*Budget, string) {
	if input == nil || input.Kind() != jsfmt.KindObject {
		return nil, "a budget must be an object"
	}
	id, ok := stringField(fieldOf(input, "id"))
	if !ok || !idPattern.MatchString(id) {
		return nil, "id: 1-40 letters, digits, _ or -"
	}
	scope, ok := stringField(fieldOf(input, "scope"))
	if !ok || !scopePattern.MatchString(scope) {
		return nil, `scope: "total" or "provider:<plugin>"`
	}
	period, ok := stringField(fieldOf(input, "period"))
	if !ok || !slices.Contains(Periods, Period(period)) {
		return nil, "period: day, week or month"
	}
	limit, ok := numberField(fieldOf(input, "limitUsd"))
	if !ok || math.IsInf(limit, 0) || limit <= 0 || limit > 1_000_000 {
		return nil, "limitUsd: a positive amount up to 1,000,000"
	}
	action, _ := stringField(fieldOf(input, "action"))
	if action != string(ActionWarn) && action != string(ActionStop) {
		return nil, "action: warn or stop"
	}
	return &Budget{
		ID:       id,
		Scope:    scope,
		Period:   Period(period),
		LimitUsd: shared.USD(math.Round(limit*100) / 100),
		Action:   Action(action),
	}, ""
}

// ValidateBudgets reads a whole budgets list from untrusted JSON — the list itself, or the
// budgets field of an object — or refuses it with the first problem found.
func ValidateBudgets(input *jsfmt.Value) ([]Budget, string) {
	var list *jsfmt.Value
	if input != nil && input.Kind() == jsfmt.KindArray {
		list = input
	} else if input != nil && input.Kind() == jsfmt.KindObject {
		list = fieldOf(input, "budgets")
	}
	if list == nil || list.Kind() != jsfmt.KindArray {
		return nil, "expected {budgets: [...]}"
	}
	items := list.Items()
	if len(items) > MaxBudgets {
		return nil, fmt.Sprintf("at most %d budgets", MaxBudgets)
	}
	out := make([]Budget, 0, len(items))
	ids := map[string]bool{}
	for index, item := range items {
		budget, refusal := ValidateBudget(item)
		if refusal != "" {
			return nil, fmt.Sprintf("budget %d: %s", index+1, refusal)
		}
		if ids[budget.ID] {
			return nil, fmt.Sprintf("budget %d: duplicate id %s", index+1, budget.ID)
		}
		ids[budget.ID] = true
		out = append(out, *budget)
	}
	return out, ""
}

// ReadBudgets loads the budgets on disk; missing, unreadable or malformed entries are
// skipped, never fatal.
func ReadBudgets(ctx context.Context, env domain.EnvLookup) []Budget {
	list := fieldOf(readObject(BudgetsPath(ctx, env)), "budgets")
	if list == nil || list.Kind() != jsfmt.KindArray {
		return nil
	}
	out := []Budget{}
	seen := map[string]bool{}
	for _, item := range list.Items() {
		budget, refusal := ValidateBudget(item)
		if refusal != "" || seen[budget.ID] {
			continue
		}
		seen[budget.ID] = true
		out = append(out, *budget)
	}
	return out
}

// WriteBudgets stores the budgets: version first, then the list.
func WriteBudgets(ctx context.Context, env domain.EnvLookup, budgets []Budget) error {
	if budgets == nil {
		budgets = []Budget{}
	}
	return settings.WriteJSONAtomic(BudgetsPath(ctx, env), budgetsFile{Version: 1, Budgets: budgets})
}

// budgetsFile is budgets.json's shape.
type budgetsFile struct {
	Version int      `json:"version"`
	Budgets []Budget `json:"budgets"`
}

// PeriodStart is the UTC start of the current day, week (Monday) or month.
func PeriodStart(period Period, now int64) int64 {
	midnight := clock.Midnight(time.UnixMilli(now))
	if period == PeriodMonth {
		return midnight.AddDate(0, 0, 1-midnight.Day()).UnixMilli()
	}
	if period == PeriodDay {
		return midnight.UnixMilli()
	}
	sinceMonday := (int(midnight.Weekday()) + 6) % 7
	return midnight.AddDate(0, 0, -sinceMonday).UnixMilli()
}

// InScope says whether a request on this model counts against this scope: "total" counts
// every priced request, a provider scope only its own models.
func InScope(scope string, model string) bool {
	if scope == "total" {
		return true
	}
	plugin := ""
	if name, ok := cost.PluginOf(model); ok {
		plugin = name
	}
	return scope == "provider:"+plugin
}

// SpendFor measures one budget's current period: every priced request in scope since the
// period began.
func SpendFor(budget Budget, rows []cost.LedgerRow, now int64) BudgetSpend {
	start := PeriodStart(budget.Period, now)
	var spent *shared.USD
	for _, row := range rows {
		if row.Ts >= start && row.Ts <= now && InScope(budget.Scope, row.Model) {
			if price := cost.RequestCost(pricedRow(row)); price != nil {
				spent = cost.AddCost(spent, &price.USD)
			}
		}
	}
	spentUsd := 0.0
	if spent != nil {
		spentUsd = float64(*spent)
	}
	return BudgetSpend{
		ID:          budget.ID,
		SpentUsd:    shared.USD(math.Round(spentUsd*10_000) / 10_000),
		LimitUsd:    budget.LimitUsd,
		Pct:         Percent(math.Round(spentUsd/float64(budget.LimitUsd)*1000) / 10),
		Scope:       budget.Scope,
		Period:      budget.Period,
		Action:      budget.Action,
		PeriodStart: start,
	}
}

// pricedRow is a stored row as the price reads it: no upstream (a row never carried one),
// no stop reason — it prices as it stands.
func pricedRow(row cost.LedgerRow) cost.PricedRequest {
	return cost.PricedRequest{
		Model:        row.Model,
		Ts:           row.Ts,
		Tokens:       row.Tokens,
		CacheWrite1h: row.CacheWrite1h,
		Speed:        row.Speed,
		Geo:          row.Geo,
		ServiceTier:  row.ServiceTier,
	}
}

// ComputeStatus builds the whole status document: one spend line per budget, and every scope
// a stop budget has reached.
func ComputeStatus(budgets []Budget, rows []cost.LedgerRow, now int64) BudgetStatus {
	spend := make([]BudgetSpend, 0, len(budgets))
	for _, budget := range budgets {
		spend = append(spend, SpendFor(budget, rows, now))
	}
	stopped := []string{}
	seen := map[string]bool{}
	for _, entry := range spend {
		if entry.Action == ActionStop && entry.Pct >= 100 && !seen[entry.Scope] {
			seen[entry.Scope] = true
			stopped = append(stopped, entry.Scope)
		}
	}
	slices.Sort(stopped)
	return BudgetStatus{Version: 1, UpdatedAt: now, Stopped: stopped, Spend: spend}
}

// WriteStatus publishes the status; the write's failure is the caller's to answer for. A
// router reading a missing or stale file simply stops nothing, so the timer's tick ignores
// a failure there and the next one retries.
func WriteStatus(ctx context.Context, env domain.EnvLookup, status BudgetStatus) error {
	return settings.WriteJSONAtomic(BudgetStatusPath(ctx, env), status)
}

// readObject reads a state file as a JSON object; a missing, broken or non-object file is
// nil, never an error.
func readObject(path string) *jsfmt.Value {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	value, err := jsfmt.DecodeValue(data)
	if err != nil || value.Kind() != jsfmt.KindObject {
		return nil
	}
	return value
}

// fieldOf reads one field of an object, the last one when a key repeats: JSON.parse keeps
// the last.
func fieldOf(object *jsfmt.Value, key string) *jsfmt.Value {
	if object == nil {
		return nil
	}
	var found *jsfmt.Value
	keys, fields := object.Keys(), object.Fields()
	for i, name := range keys {
		if name == key {
			found = fields[i]
		}
	}
	return found
}

// stringField reads a JSON string.
func stringField(value *jsfmt.Value) (string, bool) {
	if value == nil || value.Kind() != jsfmt.KindString {
		return "", false
	}
	return value.Text(), true
}

// numberField reads one JSON number. The literal is re-rendered the way JavaScript prints
// it first, so an out-of-range literal (1e999) reads as ±Inf, the way JSON.parse yields
// Infinity; ParseFloat cannot then fail.
func numberField(value *jsfmt.Value) (float64, bool) {
	if value == nil || value.Kind() != jsfmt.KindNumber {
		return 0, false
	}
	f, _ := strconv.ParseFloat(value.EncodeJSON(), 64)
	return f, true
}
