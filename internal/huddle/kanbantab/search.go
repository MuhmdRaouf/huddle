package kanbantab

// The tab's search route: one GET reads every card file of the board a key
// names, an archived board's included, and answers the cards whose text holds
// the query. Only this route reads card files; the state and boards answers
// decide from the snapshot the board's app already holds.

import (
	"net/http"
	"slices"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
	"github.com/MuhmdRaouf/huddle/internal/kanban/ticket"
)

// cardText is one card file as a search reads it: its column and its parse.
type cardText struct {
	column string
	card   ticket.Ticket
}

// searchHit is one card a search matched: its board is the card's cluster.
type searchHit struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Column   string `json:"column"`
	Board    string `json:"board"`
	Archived bool   `json:"archived"`
}

// searchAnswer is the /search body; the list is never null.
type searchAnswer struct {
	Hits []searchHit `json:"hits"`
}

// readCards reads every card file of the board, column by column. A file that
// vanishes between the walk and the read is left out.
func readCards(b board.Board) []cardText {
	var cards []cardText
	for _, col := range board.Columns {
		for _, entry := range b.ColumnEntries(col) {
			data, err := b.ReadPath(entry.Path)
			if err != nil {
				continue
			}
			card, _ := ticket.Parse(entry.Name, data)
			cards = append(cards, cardText{column: col, card: card})
		}
	}
	return cards
}

// searchRoute answers one GET on /b/<key>/search?q=<text>: the cards of every
// board of the repo whose id, title, summary, board name, tags or body hold the
// text, without regard to case, sorted by id. A blank query is refused.
func (t *Tab) searchRoute(w http.ResponseWriter, r *http.Request, key string) {
	t.oneMethod(w, r, http.MethodGet, func() {
		e := t.entryFor(r.Context(), key)
		if e == nil {
			writeJSON(w, http.StatusNotFound, errorAnswer{Error: "no board " + key})
			return
		}
		query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
		if query == "" {
			writeJSON(w, http.StatusBadRequest, errorAnswer{Error: "q: search text required"})
			return
		}
		archived := map[string]bool{}
		for _, f := range e.board.ClosedFeatures() {
			archived[f.Name] = true
		}
		hits := []searchHit{}
		for _, c := range t.readCards(e.board) {
			if !strings.Contains(strings.ToLower(searchable(c.card)), query) {
				continue
			}
			hits = append(hits, searchHit{
				ID: c.card.ID, Title: c.card.Title, Column: c.column,
				Board: c.card.Cluster, Archived: archived[c.card.Cluster],
			})
		}
		slices.SortFunc(hits, func(x, y searchHit) int { return strings.Compare(x.ID, y.ID) })
		writeJSON(w, http.StatusOK, searchAnswer{Hits: hits})
	})
}

// searchable joins the text of one card a search looks in.
func searchable(c ticket.Ticket) string {
	parts := append([]string{c.ID, c.Title, c.Summary, c.Cluster, c.Body}, c.Tags...)
	return strings.Join(parts, "\n")
}
