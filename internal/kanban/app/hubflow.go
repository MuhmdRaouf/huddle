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
	"os"
	"strconv"
	"strings"
	"time"

	huddlecli "github.com/MuhmdRaouf/huddle/internal/huddle/cli"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/kanbantab"
	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
)

// hubFlow sends one flow command to the local huddle hub and answers its
// verdict: the command's lines, or the board's refusal message. Not handled —
// no hub, no answer, or a board the hub does not serve — leaves the caller the
// direct path.
func hubFlow(ctx context.Context, getenv func(string) (string, bool), key string,
	req board.FlowRequest) ([]string, string, bool) {
	base := hubBase(getenv)
	if base == "" {
		return nil, "", false
	}
	if !hubHealthy(ctx, base) {
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

// hubBase answers the local huddle's base url from its port file, in the home
// the environment resolves. Empty when no home or no readable port is there.
func hubBase(getenv func(string) (string, bool)) string {
	h, err := home.Resolve(envOf(getenv))
	if err != nil {
		return ""
	}
	raw, err := os.ReadFile(h.PortFile())
	if err != nil {
		return ""
	}
	port := strings.TrimSpace(string(raw))
	if _, err := strconv.Atoi(port); err != nil {
		return ""
	}
	return "http://127.0.0.1:" + port
}

// hubHealthy says whether the port's holder answers huddle's health path: the
// port file alone never vouches for a server.
func hubHealthy(ctx context.Context, base string) bool {
	probe, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(probe, http.MethodGet, base+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}

// envOf adapts the kanban world's lookup to the one-value read the huddle home
// and the huddle world take.
func envOf(getenv func(string) (string, bool)) func(string) string {
	return func(key string) string { v, _ := getenv(key); return v }
}
