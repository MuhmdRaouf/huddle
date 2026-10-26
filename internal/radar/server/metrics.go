// GET /metrics: the Prometheus text exposition format (0.0.4), built from what radar
// already holds. Usage counters (requests, tokens, estimated cost) come from the 35-day
// usage ledger, so they survive restarts; the rest (latency histogram, tool calls, errors,
// sessions, alerts, budgets, router events) describe the dashboard's current window. Pure:
// the caller hands it plain data.

package server

import (
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
	"github.com/MuhmdRaouf/huddle/internal/radar/budget"
	"github.com/MuhmdRaouf/huddle/internal/radar/cost"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

// MetricsInput is everything one exposition is built from.
type MetricsInput struct {
	Version      string
	Ledger       []cost.LedgerRow
	Names        cost.LedgerNames
	Requests     []shared.RequestRecord
	Tools        []shared.ToolCallRecord
	APIErrors    []shared.APIErrorRecord
	RouterEvents []shared.RouterEventRecord
	Alerts       []shared.Alert
	Sessions     []store.SessionListItem
	Budgets      []budget.BudgetSpend
}

// labelEscaper escapes a label value's backslashes, quotes and newlines in one pass; the
// text format reads them as two-character sequences.
var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

// EscapeLabel escapes a label value the way the text format wants: backslash, quote and
// newline.
func EscapeLabel(value string) string { return labelEscaper.Replace(value) }

// labelPair is one label of one label set, in the order the sample's literal names it.
type labelPair struct {
	key   string
	value string
}

// labels renders one label set: each pair as key="value", comma-separated, braces around
// them; no pairs, no braces.
func labels(pairs ...labelPair) string {
	if len(pairs) == 0 {
		return ""
	}
	parts := make([]string, len(pairs))
	for i, pair := range pairs {
		parts[i] = pair.key + `="` + EscapeLabel(pair.value) + `"`
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// ProviderSlug answers "zai", "kimi", … for the provider plugins' models, "anthropic" for
// claude-*, else "other".
func ProviderSlug(model string) string {
	if plugin, ok := cost.PluginOf(model); ok {
		return plugin
	}
	if strings.HasPrefix(strings.ToLower(model), "claude-") {
		return "anthropic"
	}
	return "other"
}

// metricFamily is one named group of samples with its HELP and TYPE headers.
type metricFamily struct {
	name  string
	kind  string
	help  string
	lines []string
}

// family starts one empty family.
func family(name, kind, help string) metricFamily {
	return metricFamily{name: name, kind: kind, help: help}
}

// sumEntry is one sample's labels and value before the summing.
type sumEntry struct {
	pairs []labelPair
	value float64
}

// summed sums the values per label set, then emits one sample each, in label order.
func summed(target *metricFamily, entries []sumEntry, suffix string) {
	totals := make(map[string]float64, len(entries))
	keys := make([]string, 0, len(entries))
	for _, entry := range entries {
		key := labels(entry.pairs...)
		if _, seen := totals[key]; !seen {
			keys = append(keys, key)
		}
		totals[key] += entry.value
	}
	slices.Sort(keys)
	for _, key := range keys {
		target.lines = append(target.lines, target.name+suffix+key+" "+num(totals[key]))
	}
}

// num prints a sample's number the way the text format's consumers read it: whole numbers
// without a decimal point, anything else rounded to six decimals.
func num(value float64) string {
	figure := value
	if value != math.Trunc(value) || math.IsInf(value, 0) || math.IsNaN(value) {
		figure = math.Round(value*1e6) / 1e6
	}
	return jsscalar.JSNumber(figure)
}

// tokenKinds maps the ledger's four counters to their label values, in the order the
// tokens family names them.
var tokenKinds = []struct {
	counter func(shared.Tokens) int64
	label   string
}{
	{func(t shared.Tokens) int64 { return t.Input }, "input"},
	{func(t shared.Tokens) int64 { return t.Output }, "output"},
	{func(t shared.Tokens) int64 { return t.CacheRead }, "cache_read"},
	{func(t shared.Tokens) int64 { return t.CacheWrite }, "cache_write"},
}

// pricedRow is the parts of a stored row the price reads: it prices as it stands, with no
// upstream of its own to name the offering.
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

// usageFamilies builds the request, token and cost counters off the ledger.
func usageFamilies(input MetricsInput) []metricFamily {
	requests := family("radar_requests_total", "counter", "Model requests seen, by model and project.")
	tokens := family("radar_tokens_total", "counter", "Tokens by kind, model and project.")
	costs := family(
		"radar_cost_usd_total",
		"counter",
		"Estimated cost in USD at list price (priced models only), by model and project.",
	)
	var r, t, c []sumEntry
	for _, row := range input.Ledger {
		project := ""
		if known, ok := input.Names.Sessions[row.SessionID]; ok && known.Project != nil {
			project = *known.Project
		}
		base := []labelPair{
			{key: "model", value: row.Model},
			{key: "provider", value: ProviderSlug(row.Model)},
			{key: "project", value: project},
		}
		r = append(r, sumEntry{pairs: base, value: 1})
		for _, kind := range tokenKinds {
			pairs := append(slices.Clone(base), labelPair{key: "kind", value: kind.label})
			t = append(t, sumEntry{pairs: pairs, value: float64(kind.counter(row.Tokens))})
		}
		if price := cost.RequestCost(pricedRow(row)); price != nil {
			c = append(c, sumEntry{pairs: base, value: float64(price.USD)})
		}
	}
	summed(&requests, r, "")
	summed(&tokens, t, "")
	summed(&costs, c, "")
	return []metricFamily{requests, tokens, costs}
}

// latencyBuckets are the latency histogram's upper bounds, in seconds.
var latencyBuckets = []float64{0.5, 1, 2, 5, 10, 30, 60, 120, 300}

// latencyFamily builds the request latency histogram off the current request window.
func latencyFamily(input MetricsInput) metricFamily {
	histogram := family(
		"radar_request_latency_seconds",
		"histogram",
		"Time from the prompt or tool result to the model's answer, by provider.",
	)
	byProvider := make(map[string][]float64)
	providers := make([]string, 0, 4)
	for _, request := range input.Requests {
		if request.LatencyMs == nil {
			continue
		}
		slug := ProviderSlug(request.Model)
		if _, seen := byProvider[slug]; !seen {
			providers = append(providers, slug)
		}
		byProvider[slug] = append(byProvider[slug], float64(*request.LatencyMs)/1000)
	}
	slices.Sort(providers)
	for _, provider := range providers {
		values := byProvider[provider]
		for _, le := range latencyBuckets {
			count := 0
			for _, value := range values {
				if value <= le {
					count++
				}
			}
			histogram.lines = append(histogram.lines, histogram.name+"_bucket"+
				labels(labelPair{key: "provider", value: provider}, labelPair{key: "le", value: jsscalar.JSNumber(le)})+
				" "+strconv.Itoa(count))
		}
		histogram.lines = append(histogram.lines, histogram.name+"_bucket"+
			labels(labelPair{key: "provider", value: provider}, labelPair{key: "le", value: "+Inf"})+
			" "+strconv.Itoa(len(values)))
		sum := 0.0
		for _, value := range values {
			sum += value
		}
		histogram.lines = append(histogram.lines,
			histogram.name+"_sum"+labels(labelPair{key: "provider", value: provider})+" "+num(sum))
		histogram.lines = append(histogram.lines,
			histogram.name+"_count"+labels(labelPair{key: "provider", value: provider})+" "+strconv.Itoa(len(values)))
	}
	return histogram
}

// activityFamilies builds the tool call, API error, router event and session families off
// the current window.
func activityFamilies(input MetricsInput) []metricFamily {
	tools := family("radar_tool_calls_total", "counter", "Tool calls by tool and result.")
	toolEntries := make([]sumEntry, 0, len(input.Tools))
	for _, call := range input.Tools {
		result := "ok"
		if !call.Ok {
			result = "error"
		}
		toolEntries = append(toolEntries, sumEntry{
			pairs: []labelPair{{key: "tool", value: call.Name}, {key: "result", value: result}},
			value: 1,
		})
	}
	summed(&tools, toolEntries, "")

	errors := family(
		"radar_api_errors_total",
		"counter",
		"Rate limits and server errors (429, 5xx), by status and where they were seen.",
	)
	errorEntries := make([]sumEntry, 0, len(input.APIErrors))
	for _, record := range input.APIErrors {
		errorEntries = append(errorEntries, sumEntry{
			pairs: []labelPair{
				{key: "status", value: strconv.FormatInt(record.Status, 10)},
				{key: "source", value: string(record.Source)},
			},
			value: 1,
		})
	}
	summed(&errors, errorEntries, "")

	router := family("radar_router_events_total", "counter", "Router health events by provider and kind.")
	routerEntries := make([]sumEntry, 0, len(input.RouterEvents))
	for _, event := range input.RouterEvents {
		routerEntries = append(routerEntries, sumEntry{
			pairs: []labelPair{
				{key: "provider", value: event.Plugin},
				{key: "event", value: string(event.Event)},
			},
			value: 1,
		})
	}
	summed(&router, routerEntries, "")

	sessions := family("radar_sessions", "gauge", "Sessions tracked, live or ended.")
	live := 0
	for _, session := range input.Sessions {
		if session.Live {
			live++
		}
	}
	sessions.lines = append(sessions.lines,
		"radar_sessions"+labels(labelPair{key: "state", value: "live"})+" "+strconv.Itoa(live),
		"radar_sessions"+labels(labelPair{key: "state", value: "ended"})+" "+strconv.Itoa(len(input.Sessions)-live),
	)
	return []metricFamily{tools, errors, router, sessions}
}

// alertFamilies builds the alert gauges and the per-budget spend and limit gauges.
func alertFamilies(input MetricsInput) []metricFamily {
	alerts := family("radar_alerts", "gauge", "Active alerts by kind.")
	for _, kind := range []shared.AlertKind{
		shared.AlertStuck, shared.AlertLoop, shared.AlertRetryStorm, shared.AlertContext, shared.AlertBudget,
		shared.AlertQuotaNearlyUsed,
	} {
		count := 0
		for _, alert := range input.Alerts {
			if alert.Kind == kind {
				count++
			}
		}
		alerts.lines = append(alerts.lines,
			"radar_alerts"+labels(labelPair{key: "kind", value: string(kind)})+" "+strconv.Itoa(count))
	}
	spent := family("radar_budget_spent_usd", "gauge", "Estimated spend this period per budget.")
	limit := family("radar_budget_limit_usd", "gauge", "Each budget's limit for its period.")
	for _, entry := range input.Budgets {
		pairs := labels(
			labelPair{key: "budget", value: entry.ID},
			labelPair{key: "scope", value: entry.Scope},
			labelPair{key: "period", value: string(entry.Period)},
		)
		spent.lines = append(spent.lines, "radar_budget_spent_usd"+pairs+" "+num(float64(entry.SpentUsd)))
		limit.lines = append(limit.lines, "radar_budget_limit_usd"+pairs+" "+num(float64(entry.LimitUsd)))
	}
	return []metricFamily{alerts, spent, limit}
}

// RenderMetrics renders the whole exposition, ending in a newline as the format requires.
func RenderMetrics(input MetricsInput) string {
	info := family("radar_info", "gauge", "Always 1; the version label says which radar answered.")
	info.lines = append(info.lines, "radar_info"+labels(labelPair{key: "version", value: input.Version})+" 1")
	families := append([]metricFamily{info}, usageFamilies(input)...)
	families = append(families, latencyFamily(input))
	families = append(families, activityFamilies(input)...)
	families = append(families, alertFamilies(input)...)
	out := make([]string, 0, 32)
	for _, f := range families {
		out = append(out, "# HELP "+f.name+" "+f.help, "# TYPE "+f.name+" "+f.kind)
		out = append(out, f.lines...)
	}
	return strings.Join(out, "\n") + "\n"
}
