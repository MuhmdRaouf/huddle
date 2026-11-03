package app

// A session's board pin. The hub keeps it on the session's row, where the owner can change it, and
// a file in the sessions directory keeps it too, so a worker with no hub keeps its pin. A read
// asks the hub first and falls back to the file; a write goes to both.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"time"

	huddlecli "github.com/MuhmdRaouf/huddle/internal/huddle/cli"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hubaddr"
)

// pinWait bounds one call to the hub for a pin.
const pinWait = 5 * time.Second

// pinKeyNoise is everything a pin file's name may not carry.
var pinKeyNoise = regexp.MustCompile(`[^\w.-]+`)

// readPin answers the board the session is pinned to, "" for none.
func readPin(ctx context.Context, getenv func(string) (string, bool), session string) string {
	if board, ok := hubPin(ctx, getenv, http.MethodGet, session, ""); ok && board != "" {
		return board
	}
	raw, err := os.ReadFile(pinFile(getenv, session))
	if err != nil {
		return ""
	}
	return string(raw)
}

// writePin pins the session. It fails only when neither the hub nor the file took the pin.
func writePin(ctx context.Context, getenv func(string) (string, bool), session, board string) error {
	file := pinFile(getenv, session)
	fileErr := os.MkdirAll(filepath.Dir(file), 0o700)
	if fileErr == nil {
		fileErr = os.WriteFile(file, []byte(board), 0o600)
	}
	if _, ok := hubPin(ctx, getenv, http.MethodPost, session, board); ok {
		return nil
	}
	return fileErr
}

// pinFile is where a hub-less session keeps its pin.
func pinFile(getenv func(string) (string, bool), session string) string {
	dir := huddlecli.SessionsDir(&huddlecli.World{Getenv: envOf(getenv)})
	return filepath.Join(dir, pinKeyNoise.ReplaceAllString(session, "")+".board")
}

// hubPin reads (GET) or sets (POST) the session's pin on the local hub and answers the board it
// holds. False when no hub answers or the hub knows no such session.
func hubPin(ctx context.Context, getenv func(string) (string, bool), method, session, board string) (string, bool) {
	base := hubaddr.Base(getenv)
	if base == "" || !hubaddr.Healthy(ctx, base) {
		return "", false
	}
	var body []byte
	headers := map[string]string{}
	if method == http.MethodPost {
		body, _ = json.Marshal(struct {
			Board string `json:"board"`
		}{board})
		headers["content-type"] = "application/json"
	}
	resp, err := huddlecli.HFetch(ctx, &huddlecli.World{Getenv: envOf(getenv)}, method,
		base+"/api/registry/sessions/"+url.PathEscape(session)+"/board", headers, body, pinWait, session)
	if err != nil {
		return "", false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	var answer struct {
		Board string `json:"board"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		return "", false
	}
	return answer.Board, true
}
