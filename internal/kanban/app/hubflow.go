package app

// The flow client the kanban word carries: one flow command goes to the local
// huddle hub first, so a board shared through huddle takes every clone's write
// on the one board the hub serves. No hub answering — the common case for a
// worker in a temp home — costs one stat of the port file, and the command runs
// on the board the working directory resolves.

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	huddlecli "github.com/MuhmdRaouf/huddle/internal/huddle/cli"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hubaddr"
	"github.com/MuhmdRaouf/huddle/internal/huddle/kanbantab"
	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
)

// hubFlow sends one flow command to the local huddle hub and answers its
// verdict: the command's lines, or the board's refusal message. Not handled —
// no hub, no answer, or a board the hub does not serve — leaves the caller the
// direct path.
func hubFlow(ctx context.Context, getenv func(string) (string, bool), key string,
	req board.FlowRequest) ([]string, string, bool) {
	base := hubaddr.Base(getenv)
	if base == "" {
		return nil, "", false
	}
	if !hubaddr.Healthy(ctx, base) {
		return nil, "", false
	}
	body, _ := json.Marshal(req) // a struct of strings and bools always marshals
	resp, err := huddlecli.HFetch(ctx, &huddlecli.World{Getenv: envOf(getenv)}, http.MethodPost,
		base+kanbantab.Prefix+"/b/"+key+"/flow",
		map[string]string{"content-type": "application/json"}, body, 10*time.Second, "")
	if err != nil {
		return nil, "", false
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
		var answer struct {
			Lines []string `json:"lines"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
			return nil, "", false
		}
		return answer.Lines, "", true
	case http.StatusConflict, http.StatusServiceUnavailable:
		// 409 is the board's refusal; 503 is a satellite whose hub cannot answer a board
		// the hub hosts — a claim needs the hub's word, so the command is refused, never
		// run on this machine's copy
		var refusal struct {
			Error string `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&refusal); err != nil {
			return nil, "", false
		}
		return nil, refusal.Error, true
	default:
		return nil, "", false
	}
}

// hubBoard answers the hub page's address for one board key: the base url the
// port file names, its health probed, with the key as the page's hash. No
// credential or token travels in the link — the page's own sign-in is the
// hub's business. False when no hub answers.
func hubBoard(ctx context.Context, getenv func(string) (string, bool), key string) (string, bool) {
	base := hubaddr.Base(getenv)
	if base == "" {
		return "", false
	}
	if !hubaddr.Healthy(ctx, base) {
		return "", false
	}
	return base + "/#/kanban/" + key, true
}

// envOf adapts the kanban world's lookup to the one-value read the huddle
// world takes.
func envOf(getenv func(string) (string, bool)) func(string) string {
	return func(key string) string { v, _ := getenv(key); return v }
}
