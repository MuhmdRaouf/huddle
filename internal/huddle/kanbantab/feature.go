package kanbantab

// The tab's feature route: one owner write per request, closing or reopening
// the feature the body names, run on the same board.Board the flow commands
// run on. The tab's Owner alone answers who may write; a tab mounted without
// one closes nothing, so a hub that never wired the check refuses every
// feature write.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
)

// featureWrite is one feature route request: the action and the feature it
// names.
type featureWrite struct {
	Action  string `json:"action"`
	Feature string `json:"feature"`
}

// featureRoute answers one request on /b/<key>/feature: a POST from the owner
// closes or reopens the feature the body names, any other method is refused.
func (t *Tab) featureRoute(w http.ResponseWriter, r *http.Request, key string) {
	t.oneMethod(w, r, http.MethodPost, func() { t.serveFeature(w, r, key) })
}

// serveFeature runs one feature write. The owner check comes before anything
// else: a request from anyone else is refused with the body unread and the
// board untouched. The local answer carries the write's lines; the board's own
// refusal is 409 with its message, any other failure 500. No Relay sits here:
// the hub closes the features of the boards it can open, and a board it cannot
// is no board of this key at all.
func (t *Tab) serveFeature(w http.ResponseWriter, r *http.Request, key string) {
	if !t.owner(r) {
		writeJSON(w, http.StatusForbidden, errorAnswer{Error: "only the owner closes or reopens a feature"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, flowBodyMax)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errorAnswer{Error: "the feature body is over 64 KiB"})
			return
		}
		writeJSON(w, http.StatusBadRequest, errorAnswer{Error: "the feature body is not one action: " + err.Error()})
		return
	}
	var req featureWrite
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorAnswer{Error: "the feature body is not one action: " + err.Error()})
		return
	}
	if req.Action != "close" && req.Action != "reopen" {
		writeJSON(w, http.StatusBadRequest, errorAnswer{Error: "action: close or reopen"})
		return
	}
	feature := strings.TrimSpace(req.Feature)
	if feature == "" {
		writeJSON(w, http.StatusBadRequest, errorAnswer{Error: "feature: name required"})
		return
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
	// the same one-at-a-time hold the flow commands keep: a folder board has no
	// store transaction to serialise two writers, and a database board's own
	// transaction sits inside the same hold
	t.flowMu.Lock()
	var lines []string
	if req.Action == "close" {
		lines, err = b.CloseFeature(r.Context(), board.Args{Actor: "owner", ID: feature})
	} else {
		lines, err = b.ReopenFeature(r.Context(), board.Args{Actor: "owner", ID: feature})
	}
	t.flowMu.Unlock()
	if t.audit != nil {
		t.audit(r.Context(), r, "feature."+req.Action, key, feature, err == nil)
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
