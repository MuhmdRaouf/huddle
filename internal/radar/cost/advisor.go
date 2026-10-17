// The model advisor: a cautious look for subagent runs that used a main model for work a
// flash model would likely have done as well. A run counts only when everything about it
// looks small — few requests, little output, nothing but read-only tools, no failures — and
// the saving is what the same tokens cost on the model's own flash sibling from the price
// table. It is a hint, never a verdict: runs it cannot price (models no offering names) are
// counted without a figure.
package cost

import (
	"cmp"
	"slices"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// AdvisorModelRow is one model's line in the advisor report: how many of its subagent runs
// looked flash-sized, and what they cost as run and on the flash sibling.
type AdvisorModelRow struct {
	Model        string      `json:"model"`
	Flash        *string     `json:"flash"`
	Runs         int64       `json:"runs"`
	CostUsd      *shared.USD `json:"costUsd"`
	FlashCostUsd *shared.USD `json:"flashCostUsd"`
}

// AdvisorReport is the model advisor's findings.
type AdvisorReport struct {
	// RunsChecked is the subagent runs on a main model that were looked at.
	RunsChecked int64 `json:"runsChecked"`
	// Candidates is, of those, the ones that look flash-sized.
	Candidates int64 `json:"candidates"`
	// SavingUsd is the estimated saving had the candidates run on their flash sibling; null
	// when none of them is priced.
	SavingUsd *shared.USD       `json:"savingUsd"`
	ByModel   []AdvisorModelRow `json:"byModel"`
	Examples  []AdvisorExample  `json:"examples"`
}

// AdvisorExample is one candidate run, named so the dashboard can show it.
type AdvisorExample struct {
	SessionID    string `json:"sessionId"`
	AgentID      string `json:"agentId"`
	Model        string `json:"model"`
	Requests     int64  `json:"requests"`
	OutputTokens int64  `json:"outputTokens"`
}

// ReadOnlyTools are the tools that only look: a run that used nothing else is a candidate.
var ReadOnlyTools = map[string]struct{}{
	"Read":         {},
	"Grep":         {},
	"Glob":         {},
	"LS":           {},
	"WebFetch":     {},
	"WebSearch":    {},
	"NotebookRead": {},
	"TodoWrite":    {},
}

// AdvisorLimits is how big a run may be and still look flash-sized.
var AdvisorLimits = struct {
	MaxRequests     int
	MaxOutputTokens int64
}{MaxRequests: 12, MaxOutputTokens: 8000}

// advisorRun is one subagent's requests and tool calls.
type advisorRun struct {
	sessionID string
	agentID   string
	requests  []shared.RequestRecord
	tools     []shared.ToolCallRecord
}

// Advise is the advisor's report over some requests and the tool calls their runs made.
func Advise(requests []shared.RequestRecord, tools []shared.ToolCallRecord) AdvisorReport {
	byModel := make(map[string]*AdvisorModelRow)
	examples := make([]AdvisorExample, 0, 5)
	var runsChecked int64
	var saving *shared.USD
	for _, run := range runsOf(requests, tools) {
		model := dominantModel(run)
		if IsSmallModel(model) {
			continue
		}
		runsChecked++
		if !looksSmall(run) {
			continue
		}
		tokens, flash, cost, flashCost := pricedRun(model, run)
		if cost != nil && flashCost != nil {
			difference := *cost - *flashCost
			saving = AddCost(saving, &difference)
		}
		row, ok := byModel[model]
		if !ok {
			row = &AdvisorModelRow{Model: model, Flash: flash}
			byModel[model] = row
		}
		row.Runs++
		row.CostUsd = AddCost(row.CostUsd, cost)
		row.FlashCostUsd = AddCost(row.FlashCostUsd, flashCost)
		examples = append(examples, AdvisorExample{
			SessionID:    run.sessionID,
			AgentID:      run.agentID,
			Model:        model,
			Requests:     int64(len(run.requests)),
			OutputTokens: tokens.Output,
		})
	}
	rows := make([]AdvisorModelRow, 0, len(byModel))
	for _, row := range byModel {
		rows = append(rows, *row)
	}
	slices.SortStableFunc(rows, func(a, b AdvisorModelRow) int {
		if c := cmp.Compare(b.Runs, a.Runs); c != 0 {
			return c
		}
		return jsfmt.LocaleCompare(a.Model, b.Model)
	})
	candidates := int64(len(examples))
	if len(examples) > 5 {
		examples = examples[:5]
	}
	return AdvisorReport{
		RunsChecked: runsChecked,
		Candidates:  candidates,
		SavingUsd:   saving,
		ByModel:     rows,
		Examples:    examples,
	}
}

// runsOf splits the requests into per-agent runs, in first-seen order; the main agent's own
// requests are not a run, and a run needs at least one request to count.
func runsOf(requests []shared.RequestRecord, tools []shared.ToolCallRecord) []*advisorRun {
	var runs []*advisorRun
	byKey := make(map[string]int)
	run := func(sessionID, agentID string) *advisorRun {
		key := sessionID + ":" + agentID
		if at, ok := byKey[key]; ok {
			return runs[at]
		}
		runs = append(runs, &advisorRun{sessionID: sessionID, agentID: agentID})
		byKey[key] = len(runs) - 1
		return runs[len(runs)-1]
	}
	for _, r := range requests {
		if r.AgentID != "main" {
			own := run(r.SessionID, r.AgentID)
			own.requests = append(own.requests, r)
		}
	}
	for _, t := range tools {
		agentID := "main"
		if t.AgentID != nil {
			agentID = *t.AgentID
		}
		if agentID == "main" {
			continue
		}
		if at, ok := byKey[t.SessionID+":"+agentID]; ok {
			runs[at].tools = append(runs[at].tools, t)
		}
	}
	withRequests := make([]*advisorRun, 0, len(runs))
	for _, run := range runs {
		if len(run.requests) > 0 {
			withRequests = append(withRequests, run)
		}
	}
	return withRequests
}

// dominantModel is the model a run mostly used, by request count, ties by name.
func dominantModel(run *advisorRun) string {
	counts := make(map[string]int)
	for _, r := range run.requests {
		counts[r.Model]++
	}
	pairs := make([]struct {
		model string
		count int
	}, 0, len(counts))
	for model, count := range counts {
		pairs = append(pairs, struct {
			model string
			count int
		}{model, count})
	}
	slices.SortStableFunc(pairs, func(a, b struct {
		model string
		count int
	}) int {
		if c := cmp.Compare(b.count, a.count); c != 0 {
			return c
		}
		return jsfmt.LocaleCompare(a.model, b.model)
	})
	if len(pairs) == 0 {
		return ""
	}
	return pairs[0].model
}

// looksSmall says whether everything about the run looks flash-sized: few requests, little
// output, and nothing but tool calls that only looked and worked.
func looksSmall(run *advisorRun) bool {
	var output int64
	for _, r := range run.requests {
		output += r.Tokens.Output
	}
	if len(run.requests) > AdvisorLimits.MaxRequests || output > AdvisorLimits.MaxOutputTokens {
		return false
	}
	for _, t := range run.tools {
		if !t.Ok {
			return false
		}
		if _, ok := ReadOnlyTools[t.Name]; !ok {
			return false
		}
	}
	return true
}

// pricedRun is one candidate's contribution: its tokens priced on its own model and on its
// flash sibling.
func pricedRun(model string, run *advisorRun) (tokens shared.Tokens, flash *string, cost, flashCost *shared.USD) {
	for _, r := range run.requests {
		tokens = shared.AddTokens(tokens, r.Tokens)
	}
	if sibling, ok := FlashOf(model); ok {
		flash = &sibling
	}
	cost = priceRunAs(model, run)
	if flash != nil {
		flashCost = priceRunAs(*flash, run)
	}
	return tokens, flash, cost, flashCost
}

// priceRunAs prices the run's requests as if each had run on model, on that request's own
// conditions; nil when none of them was priced (one request at a time, so a tiering model
// prices each call at its own prompt size).
func priceRunAs(model string, run *advisorRun) *shared.USD {
	var total *shared.USD
	for _, request := range run.requests {
		priced := pricedOfRecord(request)
		priced.Model = model
		if cost := RequestCost(priced); cost != nil {
			total = AddCost(total, &cost.USD)
		}
	}
	return total
}

// pricedOfRecord is the parts of a live record the price reads, a null stop reason marking a
// request still streaming.
func pricedOfRecord(r shared.RequestRecord) PricedRequest {
	priced := PricedRequest{
		Model:        r.Model,
		Ts:           r.Ts,
		Tokens:       r.Tokens,
		Upstream:     r.Upstream,
		CacheWrite1h: int64Copy(r.CacheWrite1h),
		Speed:        textCopy(r.Speed),
		Geo:          textCopy(r.Geo),
		ServiceTier:  textCopy(r.ServiceTier),
	}
	if r.StopReason == nil {
		priced.StopReasonNull = true
	} else {
		priced.StopReason = textCopy(r.StopReason)
	}
	return priced
}
