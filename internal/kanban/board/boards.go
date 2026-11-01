package board

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// BoardEntry is one board of the repo's store: a registered feature. Created
// by and at are empty for a board that exists only because cards carry its
// name as their cluster.
type BoardEntry struct {
	Name      string `json:"name"`
	CreatedBy string `json:"created_by,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

// boardsFile is the BOARDS record: the registered boards, sorted by name.
type boardsFile struct {
	Boards []BoardEntry `json:"boards"`
}

func (b Board) boardsPath() string { return filepath.Join(b.Root, "BOARDS") }

// recordedBoards answers the BOARDS record sorted by name. A missing or
// unreadable BOARDS reads as none, the way ClosedFeatures treats FEATURES.
func (b Board) recordedBoards() []BoardEntry {
	data, err := b.fs().readFile(b.boardsPath())
	if err != nil {
		return nil
	}
	var rec boardsFile
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil
	}
	sortBoards(rec.Boards)
	return rec.Boards
}

// Boards answers the repo's boards sorted by name: the BOARDS record plus every
// cluster a card carries. A board is a registered feature of the one store, so
// a card's cluster names a board even when no BOARDS entry exists for it. A
// .agents/tickets/<feature>/ folder names no board.
func (b Board) Boards() []BoardEntry {
	boards := b.recordedBoards()
	known := map[string]bool{}
	for _, e := range boards {
		known[e.Name] = true
	}
	for _, col := range Columns {
		for _, fname := range b.names(col) {
			t, _ := b.loadCard(col, strings.TrimSuffix(fname, ".md"))
			if t.Cluster != "" && !known[t.Cluster] {
				known[t.Cluster] = true
				boards = append(boards, BoardEntry{Name: t.Cluster})
			}
		}
	}
	sortBoards(boards)
	return boards
}

// RequireBoard answers nil when the name is a board of the store, and the
// refusal naming kanban board create when it is not.
func (b Board) RequireBoard(name string) error {
	name = boardName(name)
	for _, e := range b.Boards() {
		if e.Name == name {
			return nil
		}
	}
	return refused("board %s does not exist; create it with kanban board create %s", name, name)
}

// CreateBoard registers a board in the store. A second create of a recorded
// board answers the already-exists line and writes nothing.
func (b Board) CreateBoard(ctx context.Context, a Args) ([]string, error) {
	return b.atomic(ctx, func(b Board) ([]string, error) { return b.createBoard(ctx, a) })
}

func (b Board) createBoard(ctx context.Context, a Args) ([]string, error) {
	if err := actorRefusal(a.Actor); err != nil {
		return nil, err
	}
	name := boardName(a.ID)
	if !clusterRe.MatchString(name) {
		return nil, refused("board name '%s': one short lowercase word or two (letters and digits)", strings.TrimSpace(a.ID))
	}
	recorded := b.recordedBoards()
	if slices.ContainsFunc(recorded, func(e BoardEntry) bool { return e.Name == name }) {
		return []string{fmt.Sprintf("board %s already exists", name)}, nil
	}
	recorded = append(recorded, BoardEntry{Name: name, CreatedBy: a.Actor, CreatedAt: b.stamp()})
	if err := b.writeBoards(ctx, recorded); err != nil {
		return nil, err
	}
	if err := b.appendLog("board-create", logStr("board", name), logStr("actor", a.Actor)); err != nil {
		return nil, err
	}
	return []string{fmt.Sprintf("board %s created", name)}, nil
}

// writeBoards lands the BOARDS record whole: JSON, sorted by name, one trailing
// newline.
func (b Board) writeBoards(ctx context.Context, boards []BoardEntry) error {
	sortBoards(boards)
	data, err := json.Marshal(boardsFile{Boards: boards})
	if err != nil {
		return err
	}
	return b.fs().writeFile(ctx, b.boardsPath(), append(data, '\n'))
}

// boardName is a board's stored form: a card's cluster, lowercased with its
// whitespace collapsed.
func boardName(name string) string { return strings.ToLower(collapse(name)) }

func sortBoards(boards []BoardEntry) {
	slices.SortFunc(boards, func(x, y BoardEntry) int { return strings.Compare(x.Name, y.Name) })
}
