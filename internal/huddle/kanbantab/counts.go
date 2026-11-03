package kanbantab

// The tab's boards route: the boards of one repo with two counts each. A board
// is a feature of the repo's store; the answer comes from the snapshot the
// board's app already holds plus the small BOARDS and FEATURES records, so it
// reads no card file.

import (
	"net/http"
	"slices"
	"strings"
)

// boardSummary is one board of a repo: its name, whether it is archived, the
// cards in progress and the cards waiting for the owner — those in review plus
// those blocked. An archived board counts nothing.
type boardSummary struct {
	Name       string `json:"name"`
	Archived   bool   `json:"archived"`
	InProgress int    `json:"inProgress"`
	Waiting    int    `json:"waiting"`
}

// repoBoardsAnswer is the /b/<key>/boards body; the list is never null.
type repoBoardsAnswer struct {
	Boards []boardSummary `json:"boards"`
}

// boardsRoute answers one GET on /b/<key>/boards: every board of the repo, open
// and archived, sorted by name. The open boards are the registered ones plus
// every cluster a card in the snapshot carries; the archived ones are the
// closed features the snapshot lists.
func (t *Tab) boardsRoute(w http.ResponseWriter, r *http.Request, key string) {
	t.oneMethod(w, r, http.MethodGet, func() {
		e := t.entryFor(r.Context(), key)
		if e == nil {
			writeJSON(w, http.StatusNotFound, errorAnswer{Error: "no board " + key})
			return
		}
		rows := map[string]*boardSummary{}
		row := func(name string) *boardSummary {
			if rows[name] == nil {
				rows[name] = &boardSummary{Name: name}
			}
			return rows[name]
		}
		for _, entry := range e.board.RecordedBoards() {
			row(entry.Name)
		}
		snapshot := e.app.Snapshot()
		for _, c := range snapshot.Cards {
			if c.Cluster == "" || c.FeatureClosed {
				continue
			}
			summary := row(c.Cluster)
			switch c.Column {
			case "in-progress":
				summary.InProgress++
			case "review", "blocked":
				summary.Waiting++
			}
		}
		for _, f := range snapshot.Features.Closed {
			*row(f.Name) = boardSummary{Name: f.Name, Archived: true}
		}
		answer := repoBoardsAnswer{Boards: make([]boardSummary, 0, len(rows))}
		for _, summary := range rows {
			answer.Boards = append(answer.Boards, *summary)
		}
		slices.SortFunc(answer.Boards, func(x, y boardSummary) int { return strings.Compare(x.Name, y.Name) })
		writeJSON(w, http.StatusOK, answer)
	})
}
