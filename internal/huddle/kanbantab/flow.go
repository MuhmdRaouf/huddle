package kanbantab

// The tab's write face: one flow command per request, run on the same
// board.Board the cli's own commands run on, so every session and satellite
// huddle serves shares one board without a port of its own.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
)

// flowBodyMax is the most bytes one flow command's body may carry.
const flowBodyMax = 64 << 10

// flowAnswer is one command's 200 body; the list is never null.
type flowAnswer struct {
	Lines []string `json:"lines"`
}

// flowCommands answers the command table, one entry per command the route
// accepts, each running the board call the cli's command of the same name runs
// with the same fields.
func flowCommands() map[string]func(ctx context.Context, b board.Board, req board.FlowRequest) ([]string, error) {
	return map[string]func(context.Context, board.Board, board.FlowRequest) ([]string, error){
		"promote": func(ctx context.Context, b board.Board, req board.FlowRequest) ([]string, error) {
			return b.Promote(ctx, board.Args{Actor: req.Actor, ID: req.ID, Force: req.Force})
		},
		"pull": func(ctx context.Context, b board.Board, req board.FlowRequest) ([]string, error) {
			return b.Pull(ctx, board.Args{
				Actor: req.Actor, ID: req.ID, Branch: req.Branch,
				Worktree: req.Worktree, Force: req.Force})
		},
		"review": func(ctx context.Context, b board.Board, req board.FlowRequest) ([]string, error) {
			return b.Review(ctx, board.Args{Actor: req.Actor, ID: req.ID, Note: req.Note})
		},
		"reject": func(ctx context.Context, b board.Board, req board.FlowRequest) ([]string, error) {
			return b.Reject(ctx, board.Args{Actor: req.Actor, ID: req.ID, Note: req.Note})
		},
		"unblock": func(ctx context.Context, b board.Board, req board.FlowRequest) ([]string, error) {
			return b.Unblock(ctx, board.Args{Actor: req.Actor, ID: req.ID, Note: req.Note})
		},
		"test": func(ctx context.Context, b board.Board, req board.FlowRequest) ([]string, error) {
			return b.Test(ctx, board.Args{Actor: req.Actor, ID: req.ID, Note: req.Note})
		},
		"done": func(ctx context.Context, b board.Board, req board.FlowRequest) ([]string, error) {
			return b.Done(ctx, board.Args{
				Actor: req.Actor, ID: req.ID, Note: req.Note,
				Checks: req.Checks, Merge: req.Merge})
		},
		"block": func(ctx context.Context, b board.Board, req board.FlowRequest) ([]string, error) {
			return b.Block(ctx, board.Args{
				Actor: req.Actor, ID: req.ID, Note: req.Note, On: req.On, Until: req.Until})
		},
		"move": func(ctx context.Context, b board.Board, req board.FlowRequest) ([]string, error) {
			return b.Move(ctx, board.Args{Actor: req.Actor, ID: req.ID, Note: req.Note, Column: req.Column})
		},
		"log": func(ctx context.Context, b board.Board, req board.FlowRequest) ([]string, error) {
			return b.Log(ctx, board.Args{Actor: req.Actor, ID: req.ID, Note: req.Note})
		},
		"set": func(ctx context.Context, b board.Board, req board.FlowRequest) ([]string, error) {
			return b.Set(ctx, req.Actor, req.ID, req.Pairs)
		},
	}
}

// flowRoute answers one request on /b/<key>/flow: a POST runs one flow command
// on the board the key names, any other method is refused.
func (t *Tab) flowRoute(w http.ResponseWriter, r *http.Request, key string) {
	t.oneMethod(w, r, http.MethodPost, func() { t.serveFlow(w, r, key) })
}

// serveFlow runs one flow command. A Relay set on the tab sees the command
// first: a board another huddle hosts is that huddle's to answer, its verdict
// written through unchanged. The local answer carries the command's lines; the
// board's own refusal is 409 with its message, any other failure 500.
func (t *Tab) serveFlow(w http.ResponseWriter, r *http.Request, key string) {
	r.Body = http.MaxBytesReader(w, r.Body, flowBodyMax)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errorAnswer{Error: "the flow body is over 64 KiB"})
			return
		}
		writeJSON(w, http.StatusBadRequest, errorAnswer{Error: "the flow body is not one command: " + err.Error()})
		return
	}
	var req board.FlowRequest
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorAnswer{Error: "the flow body is not one command: " + err.Error()})
		return
	}
	run, known := flowCommands()[req.Command]
	if !known {
		writeJSON(w, http.StatusBadRequest, errorAnswer{Error: req.Command + " is not a flow command"})
		return
	}
	if t.relay != nil {
		if status, answer, relayed := t.relay(r.Context(), key, raw); relayed {
			writeRelayed(w, status, answer)
			return
		}
	}
	b, found := t.boardFor(r.Context(), key)
	if !found {
		writeJSON(w, http.StatusNotFound, errorAnswer{Error: "no board " + key})
		return
	}
	// a root holding kanban.db writes through its store, exactly as the cli opens it
	b, closeBoard, err := board.Open(r.Context(), b)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorAnswer{Error: err.Error()})
		return
	}
	defer func() { _ = closeBoard() }()
	// a folder board has no store transaction to serialise two writers, so the
	// tab runs one flow command at a time; a database board's own transaction
	// sits inside the same hold
	t.flowMu.Lock()
	lines, err := run(r.Context(), b, req)
	t.flowMu.Unlock()
	if t.audit != nil {
		t.audit(r.Context(), r, req.Command, key, req.ID, err == nil)
	}
	if err != nil {
		var refusal *board.Refused
		if errors.As(err, &refusal) {
			writeJSON(w, http.StatusConflict, errorAnswer{Error: refusal.Message})
			return
		}
		writeJSON(w, http.StatusInternalServerError, errorAnswer{Error: err.Error()})
		return
	}
	if lines == nil {
		lines = []string{}
	}
	writeJSON(w, http.StatusOK, flowAnswer{Lines: lines})
}

// writeRelayed writes one relayed verdict whole: the huddle that ran the
// command already encoded its answer, so its status and body pass through
// unchanged under the answer headers the tab's own writes carry.
func writeRelayed(w http.ResponseWriter, status int, answer []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(answer)
}

// boardFor answers the board a key names for a flow command to run on — the
// same row lookup appFor mounts an app from, answering the Board itself.
func (t *Tab) boardFor(ctx context.Context, key string) (board.Board, bool) {
	for _, row := range t.boardsOf(ctx) {
		if row.Key == key {
			return board.Board{Root: row.Root, Now: t.now}, true
		}
	}
	return board.Board{}, false
}
