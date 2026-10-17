package usage

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// zone is a fixed UTC+5 offset the day edges prove the aggregate buckets local days in.
var zone = time.FixedZone("test", 5*3600)

// dayMoment is one moment of the test's week, day 0 naming 2026-10-10, as epoch ms.
func dayMoment(day, hour, minute int) int64 {
	return time.Date(2026, time.October, 10+day, hour, minute, 0, 0, zone).UnixMilli()
}

// weekWindow is the seven local days 2026-10-10 through 2026-10-16.
func weekWindow() Window {
	return Window{From: dayMoment(0, 0, 0), To: dayMoment(6, 23, 59)}
}

// rates is one model's rates the fake prices at, per million tokens.
type rates struct {
	in        float64
	out       float64
	cacheRead float64
}

// priceTable prices rows from a rate table the way the server's cost adapter does: cost is
// the tokens at their rates, savings the cache reads against the input rate, and a model
// the table lacks is not priced.
type priceTable map[string]rates

func (t priceTable) PriceOf(row Row) (RowPrice, bool) {
	r, ok := t[row.Model]
	if !ok {
		return RowPrice{}, false
	}
	return RowPrice{
		CostUSD:    (float64(row.Input)*r.in + float64(row.Output)*r.out + float64(row.CacheRead)*r.cacheRead) / 1e6,
		SavingsUSD: float64(row.CacheRead) / 1e6 * (r.in - r.cacheRead),
	}, true
}

// row is one request row, edited in place by edit when there is one.
func row(edit func(*Row)) Row {
	test := Row{
		TS:        dayMoment(0, 12, 0),
		Model:     "glm-5.3",
		Provider:  "zai",
		Project:   "app",
		SessionID: "s1",
		Kind:      KindMain,
		Label:     "Fix the login flow",
		Input:     1_000,
		Output:    100,
	}
	if edit != nil {
		edit(&test)
	}
	return test
}

// close reports whether two dollar figures sit within a tenth of a cent.
func close(a, b shared.USD) bool { return math.Abs(float64(a-b)) < 0.001 }

// wantTotals fails the test when a totals block misses its numbers.
func wantTotals(t *testing.T, name string, got Totals, requests, in, out, cacheRead, cacheWrite int64, cost, savings shared.USD) {
	t.Helper()
	if got.Requests != requests || got.Input != in || got.Output != out ||
		got.CacheRead != cacheRead || got.CacheWrite != cacheWrite {
		t.Errorf("%s totals = %+v, want requests %d input %d output %d cacheRead %d cacheWrite %d",
			name, got, requests, in, out, cacheRead, cacheWrite)
	}
	if !close(got.CostUSD, cost) || !close(got.SavingsUSD, savings) {
		t.Errorf("%s money = cost %.6f savings %.6f, want cost %.6f savings %.6f",
			name, got.CostUSD, got.SavingsUSD, cost, savings)
	}
}

// sumsOf folds a slice list's totals together, the check the grand total reads against.
func sumsOf(list []Slice) Totals {
	var total Totals
	for _, entry := range list {
		total.Requests += entry.Requests
		total.Input += entry.Input
		total.Output += entry.Output
		total.CacheRead += entry.CacheRead
		total.CacheWrite += entry.CacheWrite
		total.CostUSD += entry.CostUSD
		total.SavingsUSD += entry.SavingsUSD
	}
	return total
}

// sliceOf answers one key's slice, failing the test when the axis holds none.
func sliceOf(t *testing.T, list []Slice, key string) Slice {
	t.Helper()
	for _, entry := range list {
		if entry.Key == key {
			return entry
		}
	}
	t.Fatalf("no %q slice in %+v", key, list)
	return Slice{}
}

func TestAggregateZeroFillsDays(t *testing.T) {
	rows := []Row{
		row(func(r *Row) { r.TS = dayMoment(1, 10, 0) }),
		row(func(r *Row) { r.TS = dayMoment(5, 12, 0) }),
	}
	report := Aggregate(rows, weekWindow(), priceTable{"glm-5.3": {in: 1, out: 2, cacheRead: 0.1}}, zone)
	if report.From != "2026-10-10" || report.To != "2026-10-16" {
		t.Errorf("window = %s..%s, want 2026-10-10..2026-10-16", report.From, report.To)
	}
	if len(report.Days) != 7 {
		t.Fatalf("days = %d, want 7", len(report.Days))
	}
	for i, day := range report.Days {
		want := time.UnixMilli(dayMoment(i, 0, 0)).In(zone).Format("2006-01-02")
		if day.Date != want {
			t.Errorf("day %d = %s, want %s", i, day.Date, want)
		}
	}
	// exactly the two seeded days carry anything; the other five read as zero
	for i, day := range report.Days {
		if day.Requests == 0 && (day.Input != 0 || day.Output != 0 || day.CacheRead != 0 ||
			day.CacheWrite != 0 || day.CostUSD != 0 || day.SavingsUSD != 0) {
			t.Errorf("day %d is zero in requests but not in totals: %+v", i, day.Totals)
		}
	}
	if report.Days[1].Requests != 1 || report.Days[5].Requests != 1 {
		t.Errorf("seeded days' requests = %d and %d, want 1 and 1",
			report.Days[1].Requests, report.Days[5].Requests)
	}
	zero := 0
	for _, day := range report.Days {
		if day.Requests == 0 {
			zero++
		}
	}
	if zero != 5 {
		t.Errorf("empty days = %d, want 5", zero)
	}
}

func TestAggregateBucketsByLocalDay(t *testing.T) {
	// 02:00 local (UTC+5) on the 11th is 21:00 UTC on the 10th: only the local reading puts
	// the row on the 11th
	rows := []Row{row(func(r *Row) { r.TS = dayMoment(1, 2, 0) })}
	report := Aggregate(rows, weekWindow(), priceTable{"glm-5.3": {in: 1, out: 2}}, zone)
	if report.Days[1].Requests != 1 {
		t.Errorf("day 1 requests = %d, want the row's local day", report.Days[1].Requests)
	}
	if report.Days[0].Requests != 0 {
		t.Errorf("day 0 requests = %d, want 0: the row is the 11th in zone, not UTC", report.Days[0].Requests)
	}
}

func TestAggregateSlicesSumToTotals(t *testing.T) {
	prices := priceTable{
		"glm-5.3": {in: 1, out: 2, cacheRead: 0.1},
		"kimi-k3": {in: 3, out: 4, cacheRead: 0.2},
	}
	rows := []Row{
		row(func(r *Row) { r.Input, r.Output = 1_000_000, 500_000 }),
		row(func(r *Row) {
			r.Model, r.Kind, r.Label = "kimi-k3", KindSubagent, "scout"
			r.Input, r.CacheRead = 1_000_000, 2_000_000
			r.Output = 0
		}),
		row(func(r *Row) {
			r.TS, r.Project, r.SessionID, r.Kind, r.Label = dayMoment(2, 9, 30), "web", "s2", KindJob, ""
			r.Input, r.Output, r.CacheRead = 3_000_000, 1_000_000, 1_000_000
		}),
	}
	report := Aggregate(rows, weekWindow(), prices, zone)
	// r1: 1*1 + 0.5*2 = 2.0; r2: 1*3 + 2*0.2 = 3.4; r3: 3*1 + 1*2 + 1*0.1 = 5.1
	wantTotals(t, "grand", report.Totals, 3, 5_000_000, 1_500_000, 3_000_000, 0, 10.5, 6.5)
	for _, axis := range []struct {
		name string
		list []Slice
	}{
		{"byModel", report.ByModel},
		{"byProject", report.ByProject},
		{"byKind", report.ByKind},
	} {
		wantTotals(t, axis.name+" sums", sumsOf(axis.list), 3, 5_000_000, 1_500_000, 3_000_000, 0, 10.5, 6.5)
	}
	glm := sliceOf(t, report.ByModel, "glm-5.3")
	wantTotals(t, "glm-5.3", glm.Totals, 2, 4_000_000, 1_500_000, 1_000_000, 0, 7.1, 0.9)
	if len(glm.Days) != 7 {
		t.Errorf("glm-5.3 days = %d, want 7 zero-filled", len(glm.Days))
	}
	kimi := sliceOf(t, report.ByModel, "kimi-k3")
	wantTotals(t, "kimi-k3", kimi.Totals, 1, 1_000_000, 0, 2_000_000, 0, 3.4, 5.6)
	app := sliceOf(t, report.ByProject, "app")
	wantTotals(t, "app", app.Totals, 2, 2_000_000, 500_000, 2_000_000, 0, 5.4, 5.6)
	web := sliceOf(t, report.ByProject, "web")
	wantTotals(t, "web", web.Totals, 1, 3_000_000, 1_000_000, 1_000_000, 0, 5.1, 0.9)
	wantTotals(t, "main", sliceOf(t, report.ByKind, KindMain).Totals, 1, 1_000_000, 500_000, 0, 0, 2.0, 0)
	wantTotals(t, KindSubagent, sliceOf(t, report.ByKind, KindSubagent).Totals, 1, 1_000_000, 0, 2_000_000, 0, 3.4, 5.6)
	wantTotals(t, KindJob, sliceOf(t, report.ByKind, KindJob).Totals, 1, 3_000_000, 1_000_000, 1_000_000, 0, 5.1, 0.9)
	// the costliest key first, ties by key
	if len(report.ByModel) != 2 || report.ByModel[0].Key != "glm-5.3" || report.ByModel[1].Key != "kimi-k3" {
		t.Errorf("byModel order = %q, %q; want glm-5.3 then kimi-k3 by cost",
			report.ByModel[0].Key, report.ByModel[1].Key)
	}
}

func TestAggregateCacheSavings(t *testing.T) {
	// savings = cache_read/1e6 * (input rate - cache read rate): 2.5M * (1 - 0.28) = 1.8
	rows := []Row{row(func(r *Row) { r.CacheRead = 2_500_000 })}
	report := Aggregate(rows, weekWindow(), priceTable{"glm-5.3": {in: 1, out: 2, cacheRead: 0.28}}, zone)
	if !close(report.Totals.SavingsUSD, 1.8) {
		t.Errorf("savings = %.6f, want 1.8", report.Totals.SavingsUSD)
	}
	if !close(report.Days[0].SavingsUSD, 1.8) {
		t.Errorf("day savings = %.6f, want 1.8", report.Days[0].SavingsUSD)
	}
}

func TestAggregateUnpricedModel(t *testing.T) {
	prices := priceTable{"glm-5.3": {in: 1, out: 2, cacheRead: 0.1}}
	rows := []Row{
		row(nil),
		row(func(r *Row) { r.Model, r.Input = "mystery-model", 4_000 }),
	}
	report := Aggregate(rows, weekWindow(), prices, zone)
	if !report.Approximate {
		t.Error("approximate = false, want true: a model was unpriced")
	}
	if report.Unpriced != 1 {
		t.Errorf("unpriced = %d, want 1", report.Unpriced)
	}
	// the unpriced row still counts its tokens, at no cost
	wantTotals(t, "grand", report.Totals, 2, 5_000, 200, 0, 0, 0.0012, 0)
	mystery := sliceOf(t, report.ByModel, "mystery-model")
	wantTotals(t, "mystery-model", mystery.Totals, 1, 4_000, 100, 0, 0, 0, 0)
	// all priced: the flags stay off
	clean := Aggregate(rows[:1], weekWindow(), prices, zone)
	if clean.Approximate || clean.Unpriced != 0 {
		t.Errorf("priced-only report says approximate=%v unpriced=%d, want false 0",
			clean.Approximate, clean.Unpriced)
	}
}

func TestAggregateTopSessions(t *testing.T) {
	prices := priceTable{"glm-5.3": {in: 1, out: 2, cacheRead: 0.1}}
	var rows []Row
	// 22 sessions of project app; session i costs i+1, so the cheapest two drop
	for i := range 22 {
		i := i
		rows = append(rows, row(func(r *Row) {
			r.SessionID = fmt.Sprintf("s%02d", i)
			r.Kind, r.Label = KindMain, fmt.Sprintf("prompt %02d", i)
			r.Input = int64(i+1) * 1_000_000
		}))
	}
	// a cost tie decides on last activity: the later one first
	rows = append(rows,
		row(func(r *Row) {
			r.SessionID, r.Project, r.Kind, r.Label = "tie-a", "web", KindMain, "tie"
			r.TS, r.Input = dayMoment(0, 9, 0), 5_000_000
		}),
		row(func(r *Row) {
			r.SessionID, r.Project, r.Kind, r.Label = "tie-b", "web", KindMain, "tie"
			r.TS, r.Input = dayMoment(0, 11, 0), 5_000_000
		}),
	)
	report := Aggregate(rows, weekWindow(), prices, zone)
	app := report.TopSessions["app"]
	if len(app) != 20 {
		t.Fatalf("app sessions = %d, want at most 20", len(app))
	}
	if app[0].SessionID != "s21" || app[19].SessionID != "s02" {
		t.Errorf("app ends = %q..%q, want s21..s02", app[0].SessionID, app[19].SessionID)
	}
	for i := 1; i < len(app); i++ {
		if app[i-1].Totals.CostUSD < app[i].Totals.CostUSD {
			t.Errorf("app session %d costs less than its successor: %.2f < %.2f",
				i, app[i-1].Totals.CostUSD, app[i].Totals.CostUSD)
		}
	}
	web := report.TopSessions["web"]
	if len(web) != 2 || web[0].SessionID != "tie-b" || web[1].SessionID != "tie-a" {
		t.Errorf("web sessions = %+v, want tie-b before tie-a: same cost, later activity first", web)
	}
	if web[0].Project != "web" || web[0].Label != "tie" {
		t.Errorf("web[0] = %+v, want its project and label", web[0])
	}
	if web[0].LastTS != dayMoment(0, 11, 0) {
		t.Errorf("web[0] lastTs = %d, want %d", web[0].LastTS, dayMoment(0, 11, 0))
	}
}

func TestAggregateSessionLabel(t *testing.T) {
	prices := priceTable{"glm-5.3": {in: 1, out: 2}}
	t.Run("the main node's label is the session's, the subagent's only a stand-in", func(t *testing.T) {
		rows := []Row{
			row(func(r *Row) { r.Kind, r.Label = KindSubagent, "scout: dig" }),
			row(func(r *Row) { r.Kind, r.Label = KindMain, "Fix the login flow" }),
		}
		report := Aggregate(rows, weekWindow(), prices, zone)
		if got := report.TopSessions["app"][0].Label; got != "Fix the login flow" {
			t.Errorf("label = %q, want the main node's", got)
		}
	})
	t.Run("a session of subagents alone keeps its first label", func(t *testing.T) {
		rows := []Row{
			row(func(r *Row) { r.SessionID, r.Kind, r.Label = "s9", KindSubagent, "scout: dig" }),
			row(func(r *Row) { r.SessionID, r.Kind, r.Label = "s9", KindSubagent, "later one" }),
		}
		report := Aggregate(rows, weekWindow(), prices, zone)
		if got := report.TopSessions["app"][0].Label; got != "scout: dig" {
			t.Errorf("label = %q, want the first one", got)
		}
	})
	t.Run("a label cut to 80 runes", func(t *testing.T) {
		long := ""
		for range 100 {
			long += "x"
		}
		rows := []Row{row(func(r *Row) { r.Label = long })}
		report := Aggregate(rows, weekWindow(), prices, zone)
		if got := report.TopSessions["app"][0].Label; len(got) != 80 {
			t.Errorf("label length = %d, want 80", len(got))
		}
	})
}

func TestAggregatePricesRetrieved(t *testing.T) {
	report := Aggregate(nil, weekWindow(), priceTable{}, zone)
	if report.PricesRetrieved == "" {
		t.Error("pricesRetrieved is empty, want the price table's day")
	}
	if len(report.ByModel) != 0 || len(report.ByProject) != 0 || len(report.ByKind) != 0 {
		t.Error("an empty report still carries slices, want empty ones")
	}
}
