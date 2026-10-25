package cost

import (
	"slices"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// pair is a key/label or kind/label pair, the two columns the grouping tests read.
type pair [2]string

// keyLabels reads a flat grouping's key and label columns, in order.
func keyLabels(rows []AttributionRow) []pair {
	pairs := make([]pair, 0, len(rows))
	for _, row := range rows {
		pairs = append(pairs, pair{row.Key, row.Label})
	}
	return pairs
}

// nodeLabels reads a node list's label column, in order.
func nodeLabels(nodes []AttributionNode) []string {
	out := make([]string, 0, len(nodes))
	for _, node := range nodes {
		out = append(out, node.Label)
	}
	return out
}

// child finds a node's child by label.
func child(node AttributionNode, label string) (AttributionNode, bool) {
	for _, c := range node.Children {
		if c.Label == label {
			return c, true
		}
	}
	return AttributionNode{}, false
}

// mixModels reads a mix's model column, in order.
func mixModels(mix []MixEntry) []string {
	models := make([]string, 0, len(mix))
	for _, entry := range mix {
		models = append(models, entry.Model)
	}
	return models
}

func TestAttribution(t *testing.T) {
	now := time.Date(2026, time.October, 8, 15, 0, 0, 0, time.UTC).UnixMilli()
	rows := []LedgerRow{
		{ID: "1", Ts: now - 1000, Model: "glm-5.3", SessionID: "s1", AgentID: "main", Tokens: millionInput},
		{ID: "2", Ts: now - 2000, Model: "glm-5.3", SessionID: "s1", AgentID: "w1", Tokens: millionInput},
		{ID: "3", Ts: now - 3000, Model: "mystery-x", SessionID: "s2", AgentID: "main", Tokens: millionInput},
		{ID: "4", Ts: now - 3*dayMs, Model: "deepseek-flash", SessionID: "s3", AgentID: "w9", Tokens: millionInput},
	}
	names := LedgerNames{
		Sessions: map[string]SessionNames{"s1": {Project: str("app")}},
		Agents:   map[string]AgentNames{"s1:w1": {Name: "explorer"}},
	}

	t.Run("starts today at UTC midnight and the longer ranges 7 and 30 days back", func(t *testing.T) {
		wantDay := time.Date(2026, time.October, 8, 0, 0, 0, 0, time.UTC).UnixMilli()
		if got := RangeStart(RangeDay, now); got != wantDay {
			t.Errorf("rangeStart(day): got %d want %d", got, wantDay)
		}
		if got := RangeStart(RangeWeek, now); got != now-7*dayMs {
			t.Errorf("rangeStart(week): got %d want %d", got, now-7*dayMs)
		}
		if got := RangeStart(RangeMonth, now); got != now-30*dayMs {
			t.Errorf("rangeStart(month): got %d want %d", got, now-30*dayMs)
		}
	})

	t.Run("groups by repo, project, session, agent and model, priciest first", func(t *testing.T) {
		repoNames := names
		repoNames.Sessions = map[string]SessionNames{
			"s1": {Project: str("app"), Repo: str("/Users/raouf/proxbeam")},
		}
		byRepo := Attribute(rows, ByRepo, repoNames, RangeStart(RangeWeek, now))
		if got := keyLabels(byRepo); !slices.Equal(got, []pair{
			{"/Users/raouf/proxbeam", "~/proxbeam"},
			{"(no project)", "(no project)"},
		}) {
			t.Errorf("by repo: got %v", got)
		}

		byProject := Attribute(rows, ByProject, names, RangeStart(RangeWeek, now))
		if len(byProject) != 2 {
			t.Fatalf("by project: got %d groups want 2", len(byProject))
		}
		for i, want := range []struct {
			label    string
			requests int64
			unpriced int64
		}{{"app", 2, 0}, {"(no project)", 2, 1}} {
			if byProject[i].Label != want.label || byProject[i].Requests != want.requests ||
				byProject[i].Unpriced != want.unpriced {
				t.Errorf("by project %d: got %s/%d/%d want %s/%d/%d", i,
					byProject[i].Label, byProject[i].Requests, byProject[i].Unpriced,
					want.label, want.requests, want.unpriced)
			}
		}
		if byProject[0].CostUsd == nil {
			t.Fatal("by project app: no price")
		}
		closeTo(t, "by project app", float64(*byProject[0].CostUsd), 2.8)

		bySession := Attribute(rows, BySession, names, RangeStart(RangeDay, now))
		if got := keyLabels(bySession); !slices.Equal(got, []pair{{"s1", "app · s1"}, {"s2", "s2"}}) {
			t.Errorf("by session: got %v", got)
		}
		if len(bySession) != 2 {
			t.Fatalf("by session: got %d groups want 2", len(bySession))
		}
		if bySession[1].CostUsd != nil {
			t.Errorf("by session s2 cost: got %v want null", *bySession[1].CostUsd)
		}

		byAgent := Attribute(rows, ByAgent, names, 0)
		wantAgents := []string{"main · app · s1", "explorer · app · s1", "w9 · s3", "main · s2"}
		for i, want := range wantAgents {
			if byAgent[i].Label != want {
				t.Errorf("by agent %d: got %s want %s", i, byAgent[i].Label, want)
			}
		}

		byModel := Attribute(rows, ByModel, names, 0)
		wantModels := []string{"glm-5.3", "deepseek-flash", "mystery-x"}
		for i, want := range wantModels {
			if byModel[i].Key != want {
				t.Errorf("by model %d: got %s want %s", i, byModel[i].Key, want)
			}
		}
	})

	t.Run("falls back to the project when a session has no repo", func(t *testing.T) {
		mixed := LedgerNames{
			Sessions: map[string]SessionNames{
				"s1": {Project: str("app")},
				"s2": {Project: str("site"), Repo: str("/w/site")},
			},
		}
		few := []LedgerRow{
			{ID: "1", Ts: now, Model: "glm-5.3", SessionID: "s1", AgentID: "main", Tokens: millionInput},
			{ID: "2", Ts: now, Model: "glm-5.3", SessionID: "s2", AgentID: "main", Tokens: millionInput},
		}
		// equal spend keeps the first-seen group first; the repo-less session rides under
		// its project name
		if got := keyLabels(Attribute(few, ByRepo, mixed, 0)); !slices.Equal(got, []pair{
			{"app", "app"},
			{"/w/site", "/w/site"},
		}) {
			t.Errorf("by repo: got %v", got)
		}
	})

	t.Run("nests the tree repo → session → agent → model, with jobs under the session that ran them", func(t *testing.T) {
		treeNames := LedgerNames{
			Sessions: map[string]SessionNames{
				"parent": {Repo: str("/w/repo"), Name: str("the parent")},
				"job":    {Repo: str("/w/repo"), ParentSessionID: str("parent"), Name: str("the job")},
				"loose":  {Repo: str("/w/repo"), ParentSessionID: str("ghost"), Name: str("loose job")},
				"away":   {Repo: str("/w/other")},
			},
			Agents: map[string]AgentNames{
				"parent:w1": {Name: "explorer", Type: str("Explore"), Description: str("read around")},
			},
		}
		at := now - 1000
		treeRows := []LedgerRow{
			{ID: "1", Ts: at, Model: "glm-5.3", SessionID: "parent", AgentID: "main", Tokens: millionInput},
			{ID: "2", Ts: at, Model: "glm-5.3-flash", SessionID: "parent", AgentID: "w1", Tokens: millionInput},
			{ID: "3", Ts: at, Model: "kimi-k3", SessionID: "job", AgentID: "main", Tokens: millionInput},
			{ID: "4", Ts: at, Model: "deepseek-flash", SessionID: "loose", AgentID: "main", Tokens: millionInput},
			{ID: "5", Ts: at, Model: "mystery-x", SessionID: "away", AgentID: "main", Tokens: millionInput},
		}
		tree := AttributionTree(treeRows, treeNames, 0)
		if len(tree) != 2 {
			t.Fatalf("repos: got %d want 2", len(tree))
		}
		if tree[0].Kind != KindRepo || tree[0].Key != "/w/repo" ||
			tree[1].Kind != KindRepo || tree[1].Key != "/w/other" {
			t.Fatalf("repos: got %s/%s and %s/%s want repo /w/repo and repo /w/other",
				tree[0].Kind, tree[0].Key, tree[1].Kind, tree[1].Key)
		}
		repo := tree[0]
		if repo.Requests != 4 {
			t.Errorf("repo requests: got %d want 4", repo.Requests)
		}
		sessionLabels := make([]string, 0, len(repo.Children))
		for _, session := range repo.Children {
			sessionLabels = append(sessionLabels, session.Label)
		}
		slices.Sort(sessionLabels)
		if !slices.Equal(sessionLabels, []string{"loose job", "the parent"}) {
			t.Errorf("repo children: got %v want [loose job the parent]", sessionLabels)
		}
		parent, ok := child(repo, "the parent")
		if !ok {
			t.Fatal("no the parent session")
		}
		if parent.Requests != 2 {
			t.Errorf("parent requests: got %d want 2 (the job's rows stay the job's own)", parent.Requests)
		}
		wantChildren := []pair{
			{string(KindAgent), "main"},
			{string(KindAgent), "read around"},
			{string(KindSession), "the job"},
		}
		for i, want := range wantChildren {
			got := parent.Children[i]
			if string(got.Kind) != want[0] || got.Label != want[1] {
				t.Errorf("parent child %d: got %s/%s want %s/%s", i, got.Kind, got.Label, want[0], want[1])
			}
		}
		if note := parent.Children[0].Note; note != nil {
			t.Errorf("main note: got %v want null", *note)
		}
		if note := parent.Children[1].Note; note == nil || *note != "Explore" {
			t.Errorf("explorer note: got %v want Explore", parent.Children[1].Note)
		}
		job := parent.Children[2]
		if job.Requests != 1 {
			t.Errorf("job requests: got %d want 1", job.Requests)
		}
		if got := nodeLabels(job.Children[0].Children); !slices.Equal(got, []string{"kimi-k3"}) {
			t.Errorf("job models: got %v want [kimi-k3]", got)
		}
		if !slices.Equal(parent.Mix, []MixEntry{
			{Model: "glm-5.3", Tokens: 1_000_000},
			{Model: "glm-5.3-flash", Tokens: 1_000_000},
		}) {
			t.Errorf("parent mix: got %v", parent.Mix)
		}
		if got := mixModels(repo.Mix); !slices.Equal(got, []string{
			"deepseek-flash", "glm-5.3", "glm-5.3-flash", "kimi-k3",
		}) {
			t.Errorf("repo mix: got %v", got)
		}
		// an unattached job (its parent is gone) hangs directly under its repo, and models
		// sort by spend
		loose, ok := child(repo, "loose job")
		if !ok {
			t.Fatal("no loose job session")
		}
		if got := nodeLabels(loose.Children[0].Children); !slices.Equal(got, []string{"deepseek-flash"}) {
			t.Errorf("loose models: got %v want [deepseek-flash]", got)
		}
		if got := nodeLabels(tree[1].Children[0].Children[0].Children); !slices.Equal(got, []string{"mystery-x"}) {
			t.Errorf("away models: got %v want [mystery-x]", got)
		}
	})

	t.Run("respects the range in the tree and orders agents main-first, everything by spend", func(t *testing.T) {
		treeNames := LedgerNames{
			Sessions: map[string]SessionNames{"s1": {Repo: str("/w/repo")}},
		}
		treeRows := []LedgerRow{
			{ID: "1", Ts: now - 1000, Model: "kimi-k3", SessionID: "s1", AgentID: "w2", Tokens: millionInput},
			{ID: "2", Ts: now - 2000, Model: "glm-5.3", SessionID: "s1", AgentID: "w1", Tokens: millionInput},
			{ID: "3", Ts: now - 3*dayMs, Model: "glm-5.3", SessionID: "s1", AgentID: "main", Tokens: millionInput},
		}
		tree := AttributionTree(treeRows, treeNames, RangeStart(RangeDay, now))
		if len(tree) != 1 || len(tree[0].Children) != 1 {
			t.Fatalf("day tree: got %d repos want 1 with one session", len(tree))
		}
		session := tree[0].Children[0]
		// kimi ($3) out-spends glm ($1.40)
		if got := nodeLabels(session.Children); !slices.Equal(got, []string{"w2", "w1"}) {
			t.Errorf("day agents: got %v want [w2 w1]", got)
		}
		month := AttributionTree(treeRows, treeNames, RangeStart(RangeMonth, now))
		if len(month) != 1 || len(month[0].Children) != 1 {
			t.Fatalf("month tree: got %d repos want 1 with one session", len(month))
		}
		if got := nodeLabels(month[0].Children[0].Children); !slices.Equal(got, []string{"main", "w2", "w1"}) {
			t.Errorf("month agents: got %v want [main w2 w1]", got)
		}
	})

	t.Run("keeps a corrupted parent loop from hanging the tree: every member becomes a top row", func(t *testing.T) {
		looped := LedgerNames{
			Sessions: map[string]SessionNames{
				"s1": {Repo: str("/w/repo"), ParentSessionID: str("s2"), Name: str("a")},
				"s2": {Repo: str("/w/repo"), ParentSessionID: str("s1"), Name: str("b")},
			},
		}
		one := []LedgerRow{
			{ID: "1", Ts: now, Model: "glm-5.3", SessionID: "s1", AgentID: "main", Tokens: millionInput},
			{ID: "2", Ts: now, Model: "glm-5.3", SessionID: "s2", AgentID: "main", Tokens: millionInput},
		}
		tree := AttributionTree(one, looped, 0)
		if len(tree) != 1 {
			t.Fatalf("loop tree: got %d repos want 1", len(tree))
		}
		if got := nodeLabels(tree[0].Children); !slices.Equal(got, []string{"a", "b"}) {
			t.Errorf("sessions: got %v want [a b]", got)
		}
	})

	t.Run("folds a session's model mix to one entry per model across its agents", func(t *testing.T) {
		foldNames := LedgerNames{
			Sessions: map[string]SessionNames{"s1": {Repo: str("/w/repo")}},
		}
		at := now - 1000
		foldRows := []LedgerRow{
			{ID: "1", Ts: at, Model: "glm-5.3", SessionID: "s1", AgentID: "main", Tokens: millionInput},
			{ID: "2", Ts: at, Model: "glm-5.3", SessionID: "s1", AgentID: "w1", Tokens: millionInput},
			{ID: "3", Ts: at, Model: "kimi-k3", SessionID: "s1", AgentID: "w2", Tokens: millionInput},
		}
		fold := AttributionTree(foldRows, foldNames, 0)
		if len(fold) != 1 || len(fold[0].Children) != 1 {
			t.Fatalf("fold tree: got %d repos want 1 with one session", len(fold))
		}
		session := fold[0].Children[0]
		// two agents behind glm-5.3 are one bar segment carrying both agents' tokens, not
		// two entries
		if !slices.Equal(session.Mix, []MixEntry{
			{Model: "glm-5.3", Tokens: 2_000_000},
			{Model: "kimi-k3", Tokens: 1_000_000},
		}) {
			t.Errorf("mix: got %v", session.Mix)
		}
		if session.Requests != 3 {
			t.Errorf("requests: got %d want 3", session.Requests)
		}
		if session.Tokens != (shared.Tokens{Input: 3_000_000}) {
			t.Errorf("tokens: got %+v want 3M input", session.Tokens)
		}
	})

	t.Run("totals what was spent since a moment", func(t *testing.T) {
		total := SpendSince(rows, RangeStart(RangeDay, now))
		if total.USD == nil {
			t.Fatal("spendSince: no price")
		}
		closeTo(t, "spendSince", float64(*total.USD), 2.8)
		if got := SpendSince(nil, 0); got.USD != nil || got.Unpriced != 0 {
			t.Errorf("spendSince([]): got %+v want null and nothing unpriced", got)
		}
	})

	t.Run("orders an equal-token model mix the way localeCompare does, not by bytes", func(t *testing.T) {
		mixNames := LedgerNames{
			Sessions: map[string]SessionNames{"s1": {Repo: str("/w/repo")}},
		}
		at := now - 1000
		mixRows := []LedgerRow{
			{ID: "1", Ts: at, Model: "MiniMax-M2", SessionID: "s1", AgentID: "main", Tokens: millionInput},
			{ID: "2", Ts: at, Model: "minimax-m2", SessionID: "s1", AgentID: "main", Tokens: millionInput},
		}
		tree := AttributionTree(mixRows, mixNames, 0)
		if len(tree) != 1 || len(tree[0].Children) != 1 {
			t.Fatalf("tree: got %d repos want 1 with one session", len(tree))
		}
		main, ok := child(tree[0].Children[0], "main")
		if !ok {
			t.Fatal("no main agent")
		}
		// equal tokens: localeCompare weighs case behind the letters, so the lowercase id
		// leads where byte order would put the capital first
		if got := mixModels(main.Mix); !slices.Equal(got, []string{"minimax-m2", "MiniMax-M2"}) {
			t.Errorf("agent mix: got %v, want [minimax-m2 MiniMax-M2]", got)
		}
	})

	t.Run("folds session mixes the way localeCompare does on equal tokens", func(t *testing.T) {
		foldNames := LedgerNames{
			Sessions: map[string]SessionNames{
				"s1": {Repo: str("/w/repo"), Name: str("first")},
				"s2": {Repo: str("/w/repo"), Name: str("second")},
			},
		}
		at := now - 1000
		foldRows := []LedgerRow{
			{ID: "1", Ts: at, Model: "MiniMax-M2", SessionID: "s1", AgentID: "main", Tokens: millionInput},
			{ID: "2", Ts: at, Model: "minimax-m2", SessionID: "s2", AgentID: "main", Tokens: millionInput},
		}
		tree := AttributionTree(foldRows, foldNames, 0)
		if len(tree) != 1 {
			t.Fatalf("tree: got %d repos want 1", len(tree))
		}
		// one model per session, equal tokens: the repo bar folds them with the lowercase
		// id first, where byte order would lead with the capital
		if got := mixModels(tree[0].Mix); !slices.Equal(got, []string{"minimax-m2", "MiniMax-M2"}) {
			t.Errorf("repo mix: got %v, want [minimax-m2 MiniMax-M2]", got)
		}
	})
}
