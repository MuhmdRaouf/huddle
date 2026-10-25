// Attribution: who spent what. Ledger rows grouped by repo, project, session, agent or model
// over today, the last 7 days or the last 30 days, each group with its request count, tokens
// by kind and estimated cost. The tree nests the same rows repo → session (jobs under the
// session that submitted them) → agent → model. Pure.
package cost

import (
	"cmp"
	"slices"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/radar/clock"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// AttributionRange is a costs-view window: today, the last 7 days or the last 30 days.
type AttributionRange string

const (
	RangeDay   AttributionRange = "day"
	RangeWeek  AttributionRange = "week"
	RangeMonth AttributionRange = "month"
)

// AttributionRanges is the ranges the costs view offers.
var AttributionRanges = []AttributionRange{RangeDay, RangeWeek, RangeMonth}

// AttributionBy is the axis a costs grouping groups on.
type AttributionBy string

const (
	ByRepo    AttributionBy = "repo"
	ByProject AttributionBy = "project"
	BySession AttributionBy = "session"
	ByAgent   AttributionBy = "agent"
	ByModel   AttributionBy = "model"
)

// AttributionByKinds is the axes the costs view groups on.
var AttributionByKinds = []AttributionBy{ByRepo, ByProject, BySession, ByAgent, ByModel}

// AttributionRow is one group's spend: its request count, tokens by kind and estimated cost.
type AttributionRow struct {
	Key      string        `json:"key"`
	Label    string        `json:"label"`
	Requests int64         `json:"requests"`
	Tokens   shared.Tokens `json:"tokens"`
	// CostUsd is estimated USD at list price; null when no request in the group has a price.
	CostUsd *shared.USD `json:"costUsd"`
	// Unpriced is the requests in the group whose model the price table does not know.
	Unpriced int64 `json:"unpriced"`
}

// NodeKind is which level of the costs tree a node sits at.
type NodeKind string

const (
	KindRepo    NodeKind = "repo"
	KindSession NodeKind = "session"
	KindAgent   NodeKind = "agent"
	KindModel   NodeKind = "model"
)

// MixEntry is one bar segment behind a node: one model and its token total.
type MixEntry struct {
	Model  string `json:"model"`
	Tokens int64  `json:"tokens"`
}

// AttributionNode is one node of the costs drill-down, carrying the same sums a flat
// grouping shows over the rows behind it.
type AttributionNode struct {
	Kind  NodeKind `json:"kind"`
	Key   string   `json:"key"`
	Label string   `json:"label"`
	// Note is a second word when there is one: the agent type for an agent row.
	Note     *string       `json:"note"`
	Requests int64         `json:"requests"`
	Tokens   shared.Tokens `json:"tokens"`
	CostUsd  *shared.USD   `json:"costUsd"`
	Unpriced int64         `json:"unpriced"`
	// Approximate is true when a request behind the node priced at its family's rate.
	Approximate bool `json:"approximate"`
	// Mix is the tokens per model behind this node, heaviest first: the stacked model-mix
	// bar.
	Mix      []MixEntry        `json:"mix"`
	Children []AttributionNode `json:"children"`
}

// RangeStart is the start of the range: today's midnight in zone (nil is UTC), or 7 / 30 days
// back from now.
func RangeStart(span AttributionRange, now int64, zone *time.Location) int64 {
	switch span {
	case RangeWeek:
		return now - 7*dayMs
	case RangeMonth:
		return now - 30*dayMs
	}
	return clock.DayOf(time.UnixMilli(now), zone).UnixMilli()
}

// pricedOfRow is the parts of a stored row the price reads: no stop reason (it is not a
// request in flight) and no upstream (it prices through its model id).
func pricedOfRow(row LedgerRow) PricedRequest {
	return PricedRequest{
		Model:        row.Model,
		Ts:           row.Ts,
		Tokens:       row.Tokens,
		CacheWrite1h: int64Copy(row.CacheWrite1h),
		Speed:        textCopy(row.Speed),
		Geo:          textCopy(row.Geo),
		ServiceTier:  textCopy(row.ServiceTier),
	}
}

// shortId is the head of an id, the way a short label shows it.
func shortId(id string) string {
	return jsfmt.UTF16Slice(id, 0, 8)
}

// sessionLabel is what a session is called in a flat grouping: its project and short id, or
// the id alone.
func sessionLabel(names LedgerNames, sessionID string) string {
	if known, ok := names.Sessions[sessionID]; ok && known.Project != nil {
		return *known.Project + " · " + shortId(sessionID)
	}
	return shortId(sessionID)
}

// repoKeyAndLabel is a session's repo group: the repo root when known, else the project
// folder, else no project at all.
func repoKeyAndLabel(names LedgerNames, sessionID string) (key string, label string) {
	if known, ok := names.Sessions[sessionID]; ok {
		if known.Repo != nil {
			return *known.Repo, shared.HomeShort(*known.Repo)
		}
		if known.Project != nil {
			return *known.Project, *known.Project
		}
	}
	return "(no project)", "(no project)"
}

// keyAndLabel is the group a row lands in on one axis.
func keyAndLabel(row LedgerRow, by AttributionBy, names LedgerNames) (key string, label string) {
	switch by {
	case ByModel:
		return row.Model, row.Model
	case BySession:
		return row.SessionID, sessionLabel(names, row.SessionID)
	case ByRepo:
		return repoKeyAndLabel(names, row.SessionID)
	case ByProject:
		if known, ok := names.Sessions[row.SessionID]; ok && known.Project != nil {
			return *known.Project, *known.Project
		}
		return "(no project)", "(no project)"
	}
	key = row.SessionID + ":" + row.AgentID
	agent := "main"
	if row.AgentID != "main" {
		if named, ok := names.Agents[key]; ok {
			agent = named.Name
		} else {
			agent = shortId(row.AgentID)
		}
	}
	return key, agent + " · " + sessionLabel(names, row.SessionID)
}

// Attribute groups and sums the rows on one axis, most expensive first, then most tokens;
// equal footing keeps the first-seen group first.
func Attribute(rows []LedgerRow, by AttributionBy, names LedgerNames, since int64) []AttributionRow {
	var groups orderedMap[*AttributionRow]
	for _, row := range rows {
		if row.Ts < since {
			continue
		}
		key, label := keyAndLabel(row, by, names)
		group, ok := groups.get(key)
		if !ok {
			group = &AttributionRow{Key: key, Label: label}
			groups.set(key, group)
		}
		cost := RequestCost(pricedOfRow(row))
		group.Requests++
		group.Tokens = shared.AddTokens(group.Tokens, row.Tokens)
		group.CostUsd = AddCost(group.CostUsd, usdOf(cost))
		if cost == nil {
			group.Unpriced++
		}
	}
	out := make([]AttributionRow, 0, groups.len())
	for _, key := range groups.keys() {
		group, _ := groups.get(key)
		out = append(out, *group)
	}
	slices.SortStableFunc(out, bySpendRow)
	return out
}

// Spend is the estimated spend over a window and how many of its calls carry no price.
type Spend struct {
	// USD is nil when nothing in the window is priced.
	USD *shared.USD
	// Unpriced counts the calls left out of USD: a model the price table does not know, or a
	// call the router saw that no usage ever reached.
	Unpriced int64
}

// SpendSince is the estimated spend across every row at or after since.
func SpendSince(rows []LedgerRow, since int64) Spend {
	var total Spend
	for _, row := range rows {
		if row.Ts < since {
			continue
		}
		cost := RequestCost(pricedOfRow(row))
		if cost == nil || shared.TotalTokens(row.Tokens) == 0 {
			total.Unpriced++
		}
		total.USD = AddCost(total.USD, usdOf(cost))
	}
	return total
}

// bySpendRow orders groups most expensive first, then most tokens; nothing priced sinks.
func bySpendRow(a, b AttributionRow) int {
	if c := cmp.Compare(spendOf(b.CostUsd), spendOf(a.CostUsd)); c != 0 {
		return c
	}
	return cmp.Compare(shared.TotalTokens(b.Tokens), shared.TotalTokens(a.Tokens))
}

// bySpendNode is bySpendRow over tree nodes.
func bySpendNode(a, b AttributionNode) int {
	if c := cmp.Compare(spendOf(b.CostUsd), spendOf(a.CostUsd)); c != 0 {
		return c
	}
	return cmp.Compare(shared.TotalTokens(b.Tokens), shared.TotalTokens(a.Tokens))
}

// spendOf is a cost for ordering, nothing priced counting below every figure.
func spendOf(cost *shared.USD) float64 {
	if cost == nil {
		return -1
	}
	return float64(*cost)
}

// usdOf is a price's figure, nil when there is no price.
func usdOf(cost *PriceResult) *shared.USD {
	if cost == nil {
		return nil
	}
	return &cost.USD
}

// tally is the sums every grouping and node carries.
type tally struct {
	Requests int64
	Tokens   shared.Tokens
	CostUsd  *shared.USD
	Unpriced int64
	// Approximate is true once a request in the sums priced at its family's rate.
	Approximate bool
}

// tallyRow folds one row into the sums.
func tallyRow(sums *tally, row LedgerRow) {
	cost := RequestCost(pricedOfRow(row))
	sums.Requests++
	sums.Tokens = shared.AddTokens(sums.Tokens, row.Tokens)
	sums.CostUsd = AddCost(sums.CostUsd, usdOf(cost))
	if cost == nil {
		sums.Unpriced++
	} else if cost.Approximate {
		sums.Approximate = true
	}
}

// addTally folds one tally into another.
func addTally(into *tally, from tally) {
	into.Requests += from.Requests
	into.Tokens = shared.AddTokens(into.Tokens, from.Tokens)
	into.CostUsd = AddCost(into.CostUsd, from.CostUsd)
	into.Unpriced += from.Unpriced
	into.Approximate = into.Approximate || from.Approximate
}

// mixOf is the models behind a bucket, heaviest first, then by name: one bar segment per
// model behind a node.
func mixOf(models []modelBucket) []MixEntry {
	mix := make([]MixEntry, 0, len(models))
	for _, model := range models {
		mix = append(mix, MixEntry{Model: model.model, Tokens: shared.TotalTokens(model.tally.Tokens)})
	}
	slices.SortStableFunc(mix, func(a, b MixEntry) int {
		if c := cmp.Compare(b.Tokens, a.Tokens); c != 0 {
			return c
		}
		return jsfmt.LocaleCompare(a.Model, b.Model)
	})
	return mix
}

// mergeMix folds the mixes of several nodes into one, heaviest first: what a repo row's bar
// shows.
func mergeMix(parts [][]MixEntry) []MixEntry {
	totals := make(map[string]int64)
	var order []string
	for _, part := range parts {
		for _, entry := range part {
			if _, ok := totals[entry.Model]; !ok {
				order = append(order, entry.Model)
			}
			totals[entry.Model] += entry.Tokens
		}
	}
	mix := make([]MixEntry, 0, len(order))
	for _, model := range order {
		mix = append(mix, MixEntry{Model: model, Tokens: totals[model]})
	}
	slices.SortStableFunc(mix, func(a, b MixEntry) int {
		if c := cmp.Compare(b.Tokens, a.Tokens); c != 0 {
			return c
		}
		return jsfmt.LocaleCompare(a.Model, b.Model)
	})
	return mix
}

// modelBucket is one model's rows behind an agent.
type modelBucket struct {
	model string
	tally tally
}

// agentBucket is one agent's rows behind a session: its models, each with its own sums.
type agentBucket struct {
	key   string
	label string
	note  *string
	// modelIdx finds an agent's model without walking the list.
	models   []modelBucket
	modelIdx map[string]int
}

// sessionBucket is one session's rows: its agents, and the jobs it submitted.
type sessionBucket struct {
	key    string
	names  SessionNames
	agents []agentBucket
	// agentIdx finds a session's agent without walking the list.
	agentIdx map[string]int
	jobs     []*sessionBucket
}

// emptyNames is the names a session the ledger never heard of carries.
func emptyNames() SessionNames { return SessionNames{} }

// modelNode is the tree node for one model.
func modelNode(bucket modelBucket) AttributionNode {
	return AttributionNode{
		Kind:        KindModel,
		Key:         bucket.model,
		Label:       bucket.model,
		Note:        nil,
		Requests:    bucket.tally.Requests,
		Tokens:      bucket.tally.Tokens,
		CostUsd:     bucket.tally.CostUsd,
		Unpriced:    bucket.tally.Unpriced,
		Approximate: bucket.tally.Approximate,
		Mix:         []MixEntry{{Model: bucket.model, Tokens: shared.TotalTokens(bucket.tally.Tokens)}},
		Children:    []AttributionNode{},
	}
}

// agentNode is the tree node for one agent, one child per model behind it.
func agentNode(bucket agentBucket) AttributionNode {
	sums := tally{}
	for i := range bucket.models {
		addTally(&sums, bucket.models[i].tally)
	}
	children := make([]AttributionNode, 0, len(bucket.models))
	for _, model := range bucket.models {
		children = append(children, modelNode(model))
	}
	slices.SortStableFunc(children, bySpendNode)
	return AttributionNode{
		Kind:        KindAgent,
		Key:         bucket.key,
		Label:       bucket.label,
		Note:        bucket.note,
		Requests:    sums.Requests,
		Tokens:      sums.Tokens,
		CostUsd:     sums.CostUsd,
		Unpriced:    sums.Unpriced,
		Approximate: sums.Approximate,
		Mix:         mixOf(bucket.models),
		Children:    children,
	}
}

// sessionLabelOf is what a session is called in the tree: its own name, else the title an
// agent carried, else its short id.
func sessionLabelOf(session *sessionBucket) string {
	if session.names.Name != nil {
		return *session.names.Name
	}
	if session.names.Title != nil {
		return *session.names.Title
	}
	return shortId(session.key)
}

// sessionNode is the tree node for one session, its jobs nested under it.
func sessionNode(bucket *sessionBucket) AttributionNode {
	// The session's models, folded one bucket per model: an agent and its subagents behind
	// one model are one bar segment and one token total, not one per agent — the mix the
	// type promises, one entry per model behind the node.
	var folded []modelBucket
	foldedIdx := make(map[string]int)
	for i := range bucket.agents {
		for _, model := range bucket.agents[i].models {
			at, ok := foldedIdx[model.model]
			if !ok {
				folded = append(folded, modelBucket{model: model.model})
				at = len(folded) - 1
				foldedIdx[model.model] = at
			}
			addTally(&folded[at].tally, model.tally)
		}
	}
	sums := tally{}
	for i := range folded {
		addTally(&sums, folded[i].tally)
	}
	// the session's own agent leads, then the subagents by spend
	ordered := make([]struct {
		main bool
		node AttributionNode
	}, len(bucket.agents))
	for i, agent := range bucket.agents {
		ordered[i].main = agent.key == bucket.key+":main"
		ordered[i].node = agentNode(agent)
	}
	slices.SortStableFunc(ordered, func(a, b struct {
		main bool
		node AttributionNode
	}) int {
		if a.main != b.main {
			if b.main {
				return 1
			}
			return -1
		}
		return bySpendNode(a.node, b.node)
	})
	children := make([]AttributionNode, 0, len(ordered)+len(bucket.jobs))
	for i := range ordered {
		children = append(children, ordered[i].node)
	}
	jobs := make([]AttributionNode, 0, len(bucket.jobs))
	for _, job := range bucket.jobs {
		jobs = append(jobs, sessionNode(job))
	}
	slices.SortStableFunc(jobs, bySpendNode)
	children = append(children, jobs...)
	return AttributionNode{
		Kind:        KindSession,
		Key:         bucket.key,
		Label:       sessionLabelOf(bucket),
		Note:        nil,
		Requests:    sums.Requests,
		Tokens:      sums.Tokens,
		CostUsd:     sums.CostUsd,
		Unpriced:    sums.Unpriced,
		Approximate: sums.Approximate,
		Mix:         mixOf(folded),
		Children:    children,
	}
}

// hasParentLoop says whether the session's parent chain circles back on itself; such a
// session stays a top row, since a corrupted parent record must not hang the tree (or the
// build) in a loop.
func hasParentLoop(session *sessionBucket, byID map[string]*sessionBucket) bool {
	seen := map[string]struct{}{session.key: {}}
	at := session.names.ParentSessionID
	for at != nil {
		if _, ok := seen[*at]; ok {
			return true
		}
		seen[*at] = struct{}{}
		parent, ok := byID[*at]
		if !ok {
			return false
		}
		at = parent.names.ParentSessionID
	}
	return false
}

// repoNode is the tree node for one repo, its sessions sorted by spend. Every session's own
// node feeds the repo's sums, so a job's rows count once, under the job.
func repoNode(key string, label string, group []*sessionBucket) AttributionNode {
	byID := make(map[string]*sessionBucket, len(group))
	for _, session := range group {
		byID[session.key] = session
	}
	var top []*sessionBucket
	for _, session := range group {
		parent := (*sessionBucket)(nil)
		if id := session.names.ParentSessionID; id != nil {
			if p, ok := byID[*id]; ok {
				parent = p
			}
		}
		if parent == nil || hasParentLoop(session, byID) {
			top = append(top, session)
		} else {
			parent.jobs = append(parent.jobs, session)
		}
	}
	sessions := make([]AttributionNode, 0, len(group))
	mixes := make([][]MixEntry, 0, len(group))
	for _, session := range group {
		node := sessionNode(session)
		sessions = append(sessions, node)
		mixes = append(mixes, node.Mix)
	}
	sums := tally{}
	for _, node := range sessions {
		sums.Requests += node.Requests
		sums.Tokens = shared.AddTokens(sums.Tokens, node.Tokens)
		sums.CostUsd = AddCost(sums.CostUsd, node.CostUsd)
		sums.Unpriced += node.Unpriced
		sums.Approximate = sums.Approximate || node.Approximate
	}
	children := make([]AttributionNode, 0, len(top))
	for _, session := range top {
		children = append(children, sessionNode(session))
	}
	slices.SortStableFunc(children, bySpendNode)
	return AttributionNode{
		Kind:        KindRepo,
		Key:         key,
		Label:       label,
		Note:        nil,
		Requests:    sums.Requests,
		Tokens:      sums.Tokens,
		CostUsd:     sums.CostUsd,
		Unpriced:    sums.Unpriced,
		Approximate: sums.Approximate,
		Mix:         mergeMix(mixes),
		Children:    children,
	}
}

// sessionBuckets is one pass over the rows: a bucket per session, per agent, per model, each
// carrying the names on record, in first-seen order.
func sessionBuckets(rows []LedgerRow, names LedgerNames, since int64) []*sessionBucket {
	var sessions []*sessionBucket
	sessionIdx := make(map[string]int)
	for _, row := range rows {
		if row.Ts < since {
			continue
		}
		at, ok := sessionIdx[row.SessionID]
		if !ok {
			known, knownOK := names.Sessions[row.SessionID]
			if !knownOK {
				known = emptyNames()
			}
			sessions = append(sessions, &sessionBucket{
				key:      row.SessionID,
				names:    known,
				agentIdx: make(map[string]int),
			})
			at = len(sessions) - 1
			sessionIdx[row.SessionID] = at
		}
		session := sessions[at]
		agentAt, ok := session.agentIdx[row.AgentID]
		if !ok {
			named, namedOK := names.Agents[row.SessionID+":"+row.AgentID]
			label := "main"
			if row.AgentID != "main" {
				if namedOK {
					label = named.Name
				} else {
					label = shortId(row.AgentID)
				}
			}
			if namedOK && named.Description != nil {
				label = *named.Description
			}
			var note *string
			if namedOK {
				note = textCopy(named.Type)
			}
			session.agents = append(session.agents, agentBucket{
				key:      row.SessionID + ":" + row.AgentID,
				label:    label,
				note:     note,
				modelIdx: make(map[string]int),
			})
			agentAt = len(session.agents) - 1
			session.agentIdx[row.AgentID] = agentAt
		}
		agent := &session.agents[agentAt]
		modelAt, ok := agent.modelIdx[row.Model]
		if !ok {
			agent.models = append(agent.models, modelBucket{model: row.Model})
			modelAt = len(agent.models) - 1
			agent.modelIdx[row.Model] = modelAt
		}
		tallyRow(&agent.models[modelAt].tally, row)
	}
	return sessions
}

// AttributionTree is the costs drill-down over the same rows the flat groupings sum: repos →
// sessions (jobs nested under the session that submitted them, an unattached job directly
// under its repo) → agents → models.
func AttributionTree(rows []LedgerRow, names LedgerNames, since int64) []AttributionNode {
	type repoGroup struct {
		label    string
		sessions []*sessionBucket
	}
	var groups orderedMap[*repoGroup]
	for _, session := range sessionBuckets(rows, names, since) {
		key, label := repoKeyAndLabel(names, session.key)
		group, ok := groups.get(key)
		if !ok {
			group = &repoGroup{label: label}
			groups.set(key, group)
		}
		group.sessions = append(group.sessions, session)
	}
	nodes := make([]AttributionNode, 0, groups.len())
	for _, key := range groups.keys() {
		group, _ := groups.get(key)
		nodes = append(nodes, repoNode(key, group.label, group.sessions))
	}
	slices.SortStableFunc(nodes, bySpendNode)
	return nodes
}
