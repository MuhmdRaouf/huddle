// Package sources reads the stores the insights report builds its payload from: the request
// ledger and history.db, both opened read-only, and the hub's needs-you history. The hub's
// preview and the separate process that runs a report build the payload from these same readers.
package sources

import (
	"context"
	"errors"
	"io/fs"
	"math"
	"os"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/insights/report"
	"github.com/MuhmdRaouf/huddle/internal/radar/ledger"
)

// AskHistory is the hub's needs-you history; hub.DB answers it.
type AskHistory interface {
	ItemHistory(ctx context.Context, since time.Time) ([]hub.ItemRecord, error)
}

// Stores is the report's four sources over the opened stores. A store whose file does not exist
// reads as empty.
type Stores struct {
	requests *ledger.Reader
	history  *ledger.HistoryReader
	asks     AskHistory
}

// Open opens the ledger and history.db read-only; asks may be nil. It never creates a file.
func Open(ledgerFile, historyFile string, asks AskHistory) (*Stores, error) {
	stores := &Stores{asks: asks}
	if present(ledgerFile) {
		reader, err := ledger.OpenReader(ledgerFile)
		if err != nil {
			return nil, err
		}
		stores.requests = reader
	}
	if present(historyFile) {
		reader, err := ledger.OpenHistoryReader(historyFile)
		if err != nil {
			_ = stores.Close()
			return nil, err
		}
		stores.history = reader
	}
	return stores, nil
}

func present(file string) bool {
	_, err := os.Stat(file)
	return !errors.Is(err, fs.ErrNotExist)
}

// Close closes the readers.
func (s *Stores) Close() error {
	var err error
	if s.requests != nil {
		err = s.requests.Close()
	}
	if s.history != nil {
		err = errors.Join(err, s.history.Close())
	}
	return err
}

// Sources is the report's four sources, all read from these stores.
func (s *Stores) Sources() report.Sources {
	return report.Sources{Requests: s, Tools: s, Transcripts: s, Asks: s}
}

// bounds is the window in ms since the epoch; a one-session window spans all time.
func bounds(w report.Window) (from, to int64) {
	if w.Session != "" {
		return 0, math.MaxInt64
	}
	return w.From.UnixMilli(), w.To.UnixMilli()
}

func (s *Stores) Requests(ctx context.Context, w report.Window) ([]report.Request, error) {
	if s.requests == nil {
		return nil, nil
	}
	from, to := bounds(w)
	facts, err := s.requests.RequestFacts(ctx, from, to, w.Session)
	if err != nil {
		return nil, err
	}
	out := make([]report.Request, len(facts))
	for i, f := range facts {
		out[i] = report.Request{
			Session: f.Session, Model: f.Model, Provider: f.Provider, At: time.UnixMilli(f.StartedAt),
			DurationMs: f.DurationMs, Status: int(f.Status), CostUSD: f.CostUSD,
			Tokens: report.Tokens{Input: f.Input, Output: f.Output, CacheRead: f.CacheRead, CacheWrite: f.CacheWrite},
		}
	}
	return out, nil
}

// Sessions reads the plain sessions' main nodes; a job is its own node kind and stays out.
func (s *Stores) Sessions(ctx context.Context, w report.Window) ([]report.Session, error) {
	nodes, err := s.nodes(ctx, w, "main")
	if err != nil {
		return nil, err
	}
	out := make([]report.Session, len(nodes))
	for i, n := range nodes {
		out[i] = report.Session{ID: n.SessionID, Repo: n.Repo, Start: time.UnixMilli(n.StartedAt), End: endOf(n.EndedAt)}
	}
	return out, nil
}

func (s *Stores) Subagents(ctx context.Context, w report.Window) ([]report.SubagentRun, error) {
	nodes, err := s.nodes(ctx, w, "subagent")
	if err != nil {
		return nil, err
	}
	out := make([]report.SubagentRun, len(nodes))
	for i, n := range nodes {
		out[i] = report.SubagentRun{Session: n.SessionID, Started: time.UnixMilli(n.StartedAt), Stopped: endOf(n.EndedAt)}
	}
	return out, nil
}

func (s *Stores) nodes(ctx context.Context, w report.Window, kind string) ([]ledger.NodeFact, error) {
	if s.history == nil {
		return nil, nil
	}
	from, to := bounds(w)
	all, err := s.history.SessionNodes(ctx, from, to, w.Session)
	if err != nil {
		return nil, err
	}
	var out []ledger.NodeFact
	for _, n := range all {
		if n.Kind == kind {
			out = append(out, n)
		}
	}
	return out, nil
}

// endOf is the time a node ended; zero while it has not.
func endOf(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

func (s *Stores) ToolRuns(ctx context.Context, w report.Window) ([]report.ToolRun, error) {
	if s.history == nil {
		return nil, nil
	}
	from, to := bounds(w)
	facts, err := s.history.ToolFacts(ctx, from, to, w.Session)
	if err != nil {
		return nil, err
	}
	out := make([]report.ToolRun, len(facts))
	for i, f := range facts {
		out[i] = report.ToolRun{
			Session: f.Session, Tool: f.Name, InputKey: f.InputKey, At: time.UnixMilli(f.StartedAt),
			DurationMs: f.DurationMs, OK: f.OK,
		}
	}
	return out, nil
}

func (s *Stores) Compactions(ctx context.Context, w report.Window) ([]report.Compaction, error) {
	if s.history == nil {
		return nil, nil
	}
	from, to := bounds(w)
	facts, err := s.history.Compactions(ctx, from, to, w.Session)
	if err != nil {
		return nil, err
	}
	out := make([]report.Compaction, len(facts))
	for i, f := range facts {
		out[i] = report.Compaction{Session: f.Session, At: time.UnixMilli(f.At)}
	}
	return out, nil
}

// Asks reads the needs-you items that stopped a session: permission prompts and questions.
func (s *Stores) Asks(ctx context.Context, w report.Window) ([]report.Ask, error) {
	if s.asks == nil {
		return nil, nil
	}
	since := w.From
	if w.Session != "" {
		since = time.UnixMilli(0)
	}
	records, err := s.asks.ItemHistory(ctx, since)
	if err != nil {
		return nil, err
	}
	var out []report.Ask
	for _, r := range records {
		if r.Kind != hub.KindPermission && r.Kind != hub.KindQuestion {
			continue
		}
		outside := r.CreatedAt.After(w.To)
		if w.Session != "" {
			outside = r.SessionID != w.Session
		}
		if outside {
			continue
		}
		out = append(out, report.Ask{Session: r.SessionID, Decision: r.Decision, CreatedAt: r.CreatedAt, DecidedAt: r.DecidedAt})
	}
	return out, nil
}
