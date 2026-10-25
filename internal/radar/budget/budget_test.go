package budget

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/radar/cost"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// nowMs is the suite's clock: a Thursday, 15:00 UTC.
var nowMs = time.Date(2026, time.October, 8, 15, 0, 0, 0, time.UTC).UnixMilli()

const msPerDay = 86_400_000

// testEnv is a whole throwaway world: HOME and RADAR_HOME under one temp dir, built from
// scratch so no stray variable from the surrounding shell can leak a real path in.
type testEnv struct {
	env   domain.EnvLookup
	home  string
	state string
}

func makeEnv(t *testing.T) testEnv {
	t.Helper()
	home := t.TempDir()
	return testEnv{
		env: domain.EnvLookup{
			"HOME":              home,
			"RADAR_HOME":        filepath.Join(home, "state"),
			"CLAUDE_CONFIG_DIR": filepath.Join(home, "claude"),
			"ZAI_STATE_DIR":     filepath.Join(home, "zai"),
		},
		home:  home,
		state: filepath.Join(home, "state"),
	}
}

func writeText(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustValue(t *testing.T, text string) *jsfmt.Value {
	t.Helper()
	value, err := jsfmt.DecodeValue([]byte(text))
	if err != nil {
		t.Fatalf("bad test JSON %q: %v", text, err)
	}
	return value
}

// row is one ledger row: a million input tokens on the named model.
func row(id string, ts int64, model string) cost.LedgerRow {
	return cost.LedgerRow{
		ID:        id,
		Ts:        ts,
		Model:     model,
		SessionID: "s",
		AgentID:   "main",
		Tokens:    shared.Tokens{Input: 1_000_000},
	}
}

// budgetJSON builds one budget literal, any field replaced by the given raw JSON.
func budgetJSON(replace map[string]string) string {
	parts := map[string]string{
		`"id"`:       `"b1"`,
		`"scope"`:    `"provider:zai"`,
		`"period"`:   `"day"`,
		`"limitUsd"`: `5.555`,
		`"action"`:   `"stop"`,
	}
	for key, value := range replace {
		parts[key] = value
	}
	order := []string{`"id"`, `"scope"`, `"period"`, `"limitUsd"`, `"action"`}
	fields := make([]string, len(order))
	for i, key := range order {
		fields[i] = key + ":" + parts[key]
	}
	return "{" + strings.Join(fields, ",") + "}"
}

func TestBudgetValidation(t *testing.T) {
	t.Run("accepts a well-formed budget and rounds the limit to cents", func(t *testing.T) {
		budget, refusal := ValidateBudget(mustValue(t, budgetJSON(nil)))
		if refusal != "" {
			t.Fatalf("validateBudget(good) refused: %q", refusal)
		}
		want := Budget{ID: "b1", Scope: "provider:zai", Period: PeriodDay, LimitUsd: shared.USD(5.56), Action: ActionStop}
		if !reflect.DeepEqual(*budget, want) {
			t.Fatalf("validateBudget(good) = %+v, want %+v", *budget, want)
		}
		total, refusal := ValidateBudget(mustValue(t, `{"id":"b1","scope":"total","period":"day","limitUsd":5.555,"action":"stop"}`))
		if refusal != "" || total.Scope != "total" {
			t.Fatalf("validateBudget(total) = (%+v, %q), want scope total", total, refusal)
		}
	})

	t.Run("says what is wrong with a bad one", func(t *testing.T) {
		cases := []struct {
			name  string
			input string
			want  string
		}{
			{"not an object", "null", "object"},
			{"id with a space", budgetJSON(map[string]string{`"id"`: `"has space"`}), "id"},
			{"empty provider scope", budgetJSON(map[string]string{`"scope"`: `"provider:"`}), "scope"},
			{"bare provider name", budgetJSON(map[string]string{`"scope"`: `"zai"`}), "scope"},
			{"unknown period", budgetJSON(map[string]string{`"period"`: `"year"`}), "period"},
			{"zero limit", budgetJSON(map[string]string{`"limitUsd"`: `0`}), "limitUsd"},
			{"string limit", budgetJSON(map[string]string{`"limitUsd"`: `"5"`}), "limitUsd"},
			{"unknown action", budgetJSON(map[string]string{`"action"`: `"block"`}), "action"},
		}
		for _, tc := range cases {
			_, refusal := ValidateBudget(mustValue(t, tc.input))
			if !strings.Contains(refusal, tc.want) {
				t.Fatalf("%s: refusal %q, want it to say %q", tc.name, refusal, tc.want)
			}
		}
	})

	t.Run("validates whole lists: shape, size and duplicate ids", func(t *testing.T) {
		if list, refusal := ValidateBudgets(mustValue(t, `{"budgets":[`+budgetJSON(nil)+`]}`)); refusal != "" || len(list) != 1 {
			t.Fatalf(`validateBudgets({budgets:[good]}) = (%d, %q), want 1 budget`, len(list), refusal)
		}
		if list, refusal := ValidateBudgets(mustValue(t, `[`+budgetJSON(nil)+`]`)); refusal != "" || len(list) != 1 {
			t.Fatalf("validateBudgets([good]) = (%d, %q), want 1 budget", len(list), refusal)
		}
		if _, refusal := ValidateBudgets(mustValue(t, `{}`)); !strings.Contains(refusal, "budgets") {
			t.Fatalf(`validateBudgets({}) = %q, want it to say budgets`, refusal)
		}
		if _, refusal := ValidateBudgets(mustValue(t, `"x"`)); !strings.Contains(refusal, "budgets") {
			t.Fatalf(`validateBudgets("x") = %q, want it to say budgets`, refusal)
		}
		if _, refusal := ValidateBudgets(mustValue(t, `[`+budgetJSON(nil)+`,`+budgetJSON(nil)+`]`)); !strings.Contains(refusal, "duplicate") {
			t.Fatalf("validateBudgets([good, good]) = %q, want it to say duplicate", refusal)
		}
		two := `[` + budgetJSON(nil) + `,` + budgetJSON(map[string]string{`"id"`: `"b2"`, `"period"`: `"x"`}) + `]`
		if _, refusal := ValidateBudgets(mustValue(t, two)); !strings.HasPrefix(refusal, "budget 2: period") {
			t.Fatalf("validateBudgets(bad second) = %q, want budget 2: period…", refusal)
		}
		entries := make([]string, MaxBudgets+1)
		for i := range entries {
			entries[i] = fmt.Sprintf(`{"id":"b%d","scope":"provider:zai","period":"day","limitUsd":5.555,"action":"stop"}`, i)
		}
		if _, refusal := ValidateBudgets(mustValue(t, `[`+strings.Join(entries, ",")+`]`)); !strings.Contains(refusal, "at most") {
			t.Fatalf("validateBudgets(51 budgets) = %q, want it to say at most", refusal)
		}
	})
}

func TestBudgetsFile(t *testing.T) {
	t.Run("is absent by default, written 0600 and atomically, and read back tolerantly", func(t *testing.T) {
		ctx := context.Background()
		env := makeEnv(t)
		if got := ReadBudgets(ctx, env.env); len(got) != 0 {
			t.Fatalf("readBudgets with no file = %+v, want none", got)
		}
		one := []Budget{{ID: "b1", Scope: "total", Period: PeriodMonth, LimitUsd: 10, Action: ActionWarn}}
		if err := WriteBudgets(ctx, env.env, one); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(BudgetsPath(ctx, env.env))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("budgets.json mode %o, want 0600", info.Mode().Perm())
		}
		want := "{\n  \"version\": 1,\n  \"budgets\": [\n    {\n" +
			"      \"id\": \"b1\",\n      \"scope\": \"total\",\n      \"period\": \"month\",\n" +
			"      \"limitUsd\": 10,\n      \"action\": \"warn\"\n    }\n  ]\n}\n"
		data, err := os.ReadFile(BudgetsPath(ctx, env.env))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != want {
			t.Fatalf("budgets.json =\n%s\nwant\n%s", string(data), want)
		}
		if got := ReadBudgets(ctx, env.env); len(got) != 1 || got[0] != one[0] {
			t.Fatalf("readBudgets after write = %+v, want %+v", got, one)
		}
		writeText(t, BudgetsPath(ctx, env.env), `{"budgets":[`+
			`{"id":"b1","scope":"total","period":"month","limitUsd":10,"action":"warn"},`+
			`{"id":"b1","scope":"total","period":"day","limitUsd":1,"action":"warn"},`+
			`{"id":"bad"}]}`)
		if got := ReadBudgets(ctx, env.env); len(got) != 1 || got[0].Period != PeriodMonth {
			t.Fatalf("readBudgets tolerant = %+v, want the one valid month budget", got)
		}
		writeText(t, BudgetsPath(ctx, env.env), "{oops")
		if got := ReadBudgets(ctx, env.env); len(got) != 0 {
			t.Fatalf("readBudgets(broken json) = %+v, want none", got)
		}
		writeText(t, BudgetsPath(ctx, env.env), `{"budgets":"no"}`)
		if got := ReadBudgets(ctx, env.env); len(got) != 0 {
			t.Fatalf("readBudgets(budgets not a list) = %+v, want none", got)
		}
	})
}

func TestSpend(t *testing.T) {
	t.Run("starts periods at UTC midnight, Monday and the 1st", func(t *testing.T) {
		midnight := time.Date(2026, time.October, 8, 0, 0, 0, 0, time.UTC).UnixMilli()
		if got := PeriodStart(PeriodDay, nowMs); got != midnight {
			t.Fatalf("periodStart(day) = %d, want %d", got, midnight)
		}
		monday := time.Date(2026, time.October, 5, 0, 0, 0, 0, time.UTC).UnixMilli()
		if got := PeriodStart(PeriodWeek, nowMs); got != monday {
			t.Fatalf("periodStart(week) = %d, want %d", got, monday)
		}
		sunday := time.Date(2026, time.October, 11, 9, 0, 0, 0, time.UTC).UnixMilli()
		if got := PeriodStart(PeriodWeek, sunday); got != monday {
			t.Fatalf("periodStart(week, sunday) = %d, want %d", got, monday)
		}
		first := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
		if got := PeriodStart(PeriodMonth, nowMs); got != first {
			t.Fatalf("periodStart(month) = %d, want %d", got, first)
		}
	})

	t.Run("counts a provider scope's own models and the total scope every priced one", func(t *testing.T) {
		if !InScope("total", "claude-x") {
			t.Fatal("inScope(total, claude-x) = false, want true")
		}
		if !InScope("provider:zai", "glm-5.3") {
			t.Fatal("inScope(provider:zai, glm-5.3) = false, want true")
		}
		if InScope("provider:zai", "kimi-k3") {
			t.Fatal("inScope(provider:zai, kimi-k3) = true, want false")
		}
		if InScope("provider:zai", "claude-x") {
			t.Fatal("inScope(provider:zai, claude-x) = true, want false")
		}
	})

	t.Run("sums the current period only and reports a percentage", func(t *testing.T) {
		rows := []cost.LedgerRow{
			row("a", nowMs-1000, "glm-5.3"),
			row("b", nowMs-2*msPerDay, "glm-5.3"),
			row("c", nowMs-1000, "kimi-k3"),
			row("d", nowMs-1000, "claude-x"),
			row("e", nowMs+1000, "glm-5.3"),
		}
		midnight := time.Date(2026, time.October, 8, 0, 0, 0, 0, time.UTC).UnixMilli()
		zai := SpendFor(Budget{ID: "z", Scope: "provider:zai", Period: PeriodDay, LimitUsd: 2, Action: ActionStop}, rows, nowMs)
		if zai.SpentUsd != 1.4 || zai.Pct != 70 || zai.PeriodStart != midnight {
			t.Fatalf("spendFor(zai day) = %+v, want spent 1.4, pct 70, start %d", zai, midnight)
		}
		total := SpendFor(Budget{ID: "t", Scope: "total", Period: PeriodMonth, LimitUsd: 4, Action: ActionWarn}, rows, nowMs)
		if math.Abs(float64(total.SpentUsd)-5.8) >= 0.005 {
			t.Fatalf("spendFor(total month) spent %v, want about 5.8", total.SpentUsd)
		}
		none := SpendFor(Budget{ID: "n", Scope: "provider:qwen", Period: PeriodDay, LimitUsd: 1, Action: ActionWarn}, rows, nowMs)
		if none.SpentUsd != 0 || none.Pct != 0 {
			t.Fatalf("spendFor(qwen day) = %+v, want spent 0, pct 0", none)
		}
	})

	t.Run("lists a stop budget's scope as stopped at 100%, never a warn budget's", func(t *testing.T) {
		rows := []cost.LedgerRow{row("a", nowMs-1000, "glm-5.3")}
		status := ComputeStatus([]Budget{
			{ID: "z", Scope: "provider:zai", Period: PeriodDay, LimitUsd: 1, Action: ActionStop},
			{ID: "z2", Scope: "provider:zai", Period: PeriodWeek, LimitUsd: 1, Action: ActionStop},
			{ID: "t", Scope: "total", Period: PeriodDay, LimitUsd: 1, Action: ActionWarn},
			{ID: "k", Scope: "provider:kimi", Period: PeriodDay, LimitUsd: 1, Action: ActionStop},
		}, rows, nowMs)
		if status.Version != 1 || status.UpdatedAt != nowMs ||
			!reflect.DeepEqual(status.Stopped, []string{"provider:zai"}) {
			t.Fatalf("computeStatus = %+v, want version 1, updated %d, stopped [provider:zai]", status, nowMs)
		}
		ids := make([]string, len(status.Spend))
		for i, entry := range status.Spend {
			ids[i] = entry.ID
		}
		if !reflect.DeepEqual(ids, []string{"z", "z2", "t", "k"}) {
			t.Fatalf("computeStatus spend ids = %v, want [z z2 t k]", ids)
		}
	})

	t.Run("a limit that rounds to zero cents overflows the share to null and stops the scope", func(t *testing.T) {
		// limitUsd 0.004 is a positive amount, but rounds to zero cents: anything priced is
		// infinitely over, and the infinite share must reach the file as null
		budget, refusal := ValidateBudget(mustValue(t, budgetJSON(map[string]string{`"limitUsd"`: `0.004`})))
		if refusal != "" || budget.LimitUsd != 0 {
			t.Fatalf("validateBudget(0.004) = (%+v, %q), want the limit rounded to zero", budget, refusal)
		}
		rows := []cost.LedgerRow{row("a", nowMs-1000, "glm-5.3")}
		spend := SpendFor(*budget, rows, nowMs)
		if !math.IsInf(float64(spend.Pct), 1) {
			t.Fatalf("spendFor(zero limit) pct = %v, want +Inf", spend.Pct)
		}
		status := ComputeStatus([]Budget{*budget}, rows, nowMs)
		if !reflect.DeepEqual(status.Stopped, []string{"provider:zai"}) {
			t.Fatalf("computeStatus stopped = %v, want provider:zai", status.Stopped)
		}
		ctx := context.Background()
		env := makeEnv(t)
		if err := WriteStatus(ctx, env.env, status); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(BudgetStatusPath(ctx, env.env))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(data, []byte(`"pct": null`)) {
			t.Fatalf("budget-status.json pct line missing null:\n%s", string(data))
		}
		var parsed any
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("budget-status.json is not JSON: %v", err)
		}
	})

	t.Run("publishes budget-status.json 0600 and reports a failed write", func(t *testing.T) {
		ctx := context.Background()
		env := makeEnv(t)
		status := ComputeStatus(nil, nil, nowMs)
		if err := WriteStatus(ctx, env.env, status); err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf("{\n  \"version\": 1,\n  \"updatedAt\": %d,\n  \"stopped\": [],\n  \"spend\": []\n}\n", nowMs)
		data, err := os.ReadFile(BudgetStatusPath(ctx, env.env))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != want {
			t.Fatalf("budget-status.json =\n%s\nwant\n%s", string(data), want)
		}
		var parsed, asWritten any
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(status)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(encoded, &asWritten); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(parsed, asWritten) {
			t.Fatalf("budget-status.json = %v, want the status %v", parsed, asWritten)
		}
		info, err := os.Stat(BudgetStatusPath(ctx, env.env))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("budget-status.json mode %o, want 0600", info.Mode().Perm())
		}
		writeText(t, filepath.Join(env.home, "blocked"), "file")
		blocked := domain.EnvLookup{}
		for key, value := range env.env {
			blocked[key] = value
		}
		blocked["RADAR_HOME"] = filepath.Join(env.home, "blocked", "state")
		if err := WriteStatus(ctx, blocked, status); err == nil {
			t.Fatal("writeStatus under a file = nil, want the failure")
		}
	})
}

func TestScopeLabelsForPeople(t *testing.T) {
	t.Run("names scopes and plugins for people", func(t *testing.T) {
		cases := []struct{ scope, want string }{
			{"total", "Total"},
			{"provider:zai", "Z.ai"},
			{"provider:acme", "acme"},
			{"odd", "odd"},
		}
		for _, tc := range cases {
			if got := shared.ScopeLabel(tc.scope); got != tc.want {
				t.Fatalf("scopeLabel(%q) = %q, want %q", tc.scope, got, tc.want)
			}
		}
		if got := shared.PluginLabel("deepseek"); got != "DeepSeek" {
			t.Fatalf("pluginLabel(deepseek) = %q, want DeepSeek", got)
		}
	})
}

// The routers gate provider traffic on this file: they read the whole bytes as one JSON
// value and hand it to the domain's stop decision. The writer must stay readable through
// exactly that path.
func TestStatusFileFeedsTheRoutersBudgetGate(t *testing.T) {
	readAsTheRouterDoes := func(t *testing.T, path string) any {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var parsed any
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("the router could not parse %s: %v", path, err)
		}
		return parsed
	}

	t.Run("stops the plugin whose scope is over, with the over budget's period", func(t *testing.T) {
		ctx := context.Background()
		env := makeEnv(t)
		budgets := []Budget{{ID: "z", Scope: "provider:zai", Period: PeriodDay, LimitUsd: 1, Action: ActionStop}}
		if err := WriteBudgets(ctx, env.env, budgets); err != nil {
			t.Fatal(err)
		}
		rows := []cost.LedgerRow{row("a", nowMs-1000, "glm-5.3")}
		if err := WriteStatus(ctx, env.env, ComputeStatus(budgets, rows, nowMs)); err != nil {
			t.Fatal(err)
		}
		status := readAsTheRouterDoes(t, BudgetStatusPath(ctx, env.env))
		set := readAsTheRouterDoes(t, BudgetsPath(ctx, env.env))
		stop, ok := domain.BudgetStopFor("zai", status, set, nowMs)
		if !ok || stop.Scope != "provider:zai" || stop.Period == nil || *stop.Period != domain.BudgetDay {
			t.Fatalf("BudgetStopFor(zai) = (%+v, %v), want provider:zai stopped for the day", stop, ok)
		}
		if _, ok := domain.BudgetStopFor("kimi", status, set, nowMs); ok {
			t.Fatal("BudgetStopFor(kimi) stopped the plugin, want nothing")
		}
	})

	t.Run("stops nothing while every budget is under", func(t *testing.T) {
		ctx := context.Background()
		env := makeEnv(t)
		budgets := []Budget{{ID: "z", Scope: "provider:zai", Period: PeriodDay, LimitUsd: 100, Action: ActionStop}}
		if err := WriteBudgets(ctx, env.env, budgets); err != nil {
			t.Fatal(err)
		}
		rows := []cost.LedgerRow{row("a", nowMs-1000, "glm-5.3")}
		if err := WriteStatus(ctx, env.env, ComputeStatus(budgets, rows, nowMs)); err != nil {
			t.Fatal(err)
		}
		status := readAsTheRouterDoes(t, BudgetStatusPath(ctx, env.env))
		set := readAsTheRouterDoes(t, BudgetsPath(ctx, env.env))
		if _, ok := domain.BudgetStopFor("zai", status, set, nowMs); ok {
			t.Fatal("BudgetStopFor(zai) stopped the plugin under its budget, want nothing")
		}
	})
}

// TestBudgetPeriodsAreUTC reads each period at 23:30 in UTC-05:00, which is 04:30 UTC on the
// next date: the day, the week and the month all start on the UTC side of that midnight.
func TestBudgetPeriodsAreUTC(t *testing.T) {
	eastern := time.FixedZone("UTC-5", -5*3600)
	cases := []struct {
		name   string
		period Period
		at     time.Time
		want   time.Time
	}{
		{"day", PeriodDay, time.Date(2026, time.October, 15, 23, 30, 0, 0, eastern),
			time.Date(2026, time.October, 16, 0, 0, 0, 0, time.UTC)},
		{"week from a Sunday evening", PeriodWeek, time.Date(2026, time.October, 18, 23, 30, 0, 0, eastern),
			time.Date(2026, time.October, 19, 0, 0, 0, 0, time.UTC)},
		{"month from its last evening", PeriodMonth, time.Date(2026, time.October, 31, 23, 30, 0, 0, eastern),
			time.Date(2026, time.November, 1, 0, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := PeriodStart(c.period, c.at.UnixMilli())
			if got != c.want.UnixMilli() {
				t.Errorf("PeriodStart(%s, %s) = %s, want %s", c.period, c.at,
					time.UnixMilli(got).UTC(), c.want)
			}
		})
	}
}
