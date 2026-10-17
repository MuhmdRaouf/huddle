package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/process"
)

// The budgets the lifecycle commands keep: health decides within a second, a
// fresh server has three to listen.
const (
	startWaitMs = 3_000
	startPollMs = 100
)

// Effects is every effect the server commands have; the cli builds the real
// ones, the tests record them.
type Effects struct {
	// Fetch answers one loopback request; ok false is network trouble, never
	// an error.
	Fetch func(ctx context.Context, method, url string, header map[string]string) (int, []byte, bool)
	// Sleep waits between polls, in milliseconds.
	Sleep func(ms int64)
	// SpawnServe starts the detached serve; 0 when the start failed.
	SpawnServe func(command string, args []string, logFile string) int64
	// OpenBrowser hands one url to the platform's opener.
	OpenBrowser func(url string)
}

// DefaultEffects builds the real ones: a bounded loopback client, the detached
// self-spawn with the server log as its output, the platform browser opener
// and a real wait.
func DefaultEffects() Effects {
	return Effects{
		Fetch:       fetch,
		Sleep:       func(ms int64) { time.Sleep(time.Duration(ms) * time.Millisecond) },
		SpawnServe:  spawnServe,
		OpenBrowser: openBrowser,
	}
}

// fetch answers one loopback request with a one-second budget.
func fetch(ctx context.Context, method, url string, header map[string]string) (int, []byte, bool) {
	requestCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, method, url, nil)
	if err != nil {
		return 0, nil, false
	}
	for key, value := range header {
		request.Header.Set(key, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return 0, nil, false
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return response.StatusCode, nil, false
	}
	return response.StatusCode, body, true
}

// spawnServe starts the detached serve with the caller's environment and the
// server log as its output, so a failed start leaves its trace.
func spawnServe(command string, args []string, logFile string) int64 {
	return process.StartDetached(command, args, process.DetachedOptions{
		Env: os.Environ(), LogFile: logFile,
	}).Pid
}

// openBrowser hands the url to the platform's opener, detached and best
// effort: the url is already printed.
func openBrowser(url string) {
	opener := browserOpener()
	if opener == "" {
		return
	}
	process.StartDetached(opener, []string{url}, process.DetachedOptions{})
}

// browserOpener answers the platform's browser opener.
func browserOpener() string {
	if runtime.GOOS == "darwin" {
		return "open"
	}
	return "xdg-open"
}

// ErrNoServer names a board whose server is not answering.
type ErrNoServer struct{ Board string }

func (e *ErrNoServer) Error() string { return "no server for " + e.Board }

// serverURL answers the address a board's server answers on.
func serverURL(port int) string { return fmt.Sprintf("http://127.0.0.1:%d", port) }

// Looking answers the board's recorded state when its server answers health
// for this board; a record whose server is gone is removed, so the next start
// begins clean. A pid alone never decides: health does.
func Looking(ctx context.Context, boardPath, home string, fx Effects) *State {
	abs := absPath(boardPath)
	key := KeyOf(abs)
	recorded := ReadState(home, key)
	if recorded == nil {
		return nil
	}
	if !servesBoard(ctx, fx, recorded, abs) {
		RemoveState(home, key)
		return nil
	}
	return recorded
}

// servesBoard answers whether the recorded server answers health for this
// board.
func servesBoard(ctx context.Context, fx Effects, recorded *State, abs string) bool {
	status, body, ok := fx.Fetch(ctx, http.MethodGet, fmt.Sprintf("%s/api/health", serverURL(recorded.Port)), nil)
	if !ok || status != http.StatusOK {
		return false
	}
	var health struct {
		OK    bool   `json:"ok"`
		Board string `json:"board"`
	}
	if err := json.Unmarshal(body, &health); err != nil {
		return false
	}
	return health.OK && health.Board == abs
}

// Start ensures the board's server and answers its URL: a recorded server
// that still answers health is reused, anything else is replaced by a
// detached serve whose own state file tells the caller when it listens.
func Start(ctx context.Context, boardPath, home string, port int, fx Effects) (string, error) {
	abs := absPath(boardPath)
	key := KeyOf(abs)
	if recorded := Looking(ctx, boardPath, home, fx); recorded != nil {
		return serverURL(recorded.Port), nil
	}
	args := []string{"serve", "--board", abs}
	switch {
	case port > 0:
		args = append(args, "--port", strconv.Itoa(port))
	case port < 0:
		args = append(args, "--port", "0")
	}
	if self, err := os.Executable(); err == nil {
		fx.SpawnServe(self, args, LogPath(home, key))
	}
	for range startWaitMs / startPollMs {
		fx.Sleep(startPollMs)
		recorded := ReadState(home, key)
		if recorded == nil {
			continue // the child writes the state once it listens
		}
		if servesBoard(ctx, fx, recorded, abs) {
			return serverURL(recorded.Port), nil
		}
	}
	return "", fmt.Errorf("server for %s did not become healthy within %d s", abs, startWaitMs/1000)
}

// Stop ends the board's server with its token. A record whose server is gone,
// or one that answers another board, is removed and answered as no server.
func Stop(ctx context.Context, boardPath, home string, fx Effects) error {
	abs := absPath(boardPath)
	key := KeyOf(abs)
	recorded := ReadState(home, key)
	if recorded == nil {
		return &ErrNoServer{Board: abs}
	}
	status, _, ok := fx.Fetch(ctx, http.MethodPost,
		fmt.Sprintf("%s/api/shutdown", serverURL(recorded.Port)),
		map[string]string{"X-Kanban-Token": recorded.Token})
	if !ok || status != http.StatusOK {
		RemoveState(home, key)
		return &ErrNoServer{Board: abs}
	}
	return nil
}

// Refresh rescans the board through its server and answers the new rev.
func Refresh(ctx context.Context, boardPath, home string, fx Effects) (int64, error) {
	abs := absPath(boardPath)
	key := KeyOf(abs)
	recorded := ReadState(home, key)
	if recorded == nil {
		return 0, &ErrNoServer{Board: abs}
	}
	status, body, ok := fx.Fetch(ctx, http.MethodPost,
		fmt.Sprintf("%s/api/refresh", serverURL(recorded.Port)), nil)
	if !ok || status != http.StatusOK {
		RemoveState(home, key)
		return 0, &ErrNoServer{Board: abs}
	}
	var answer struct {
		Rev int64 `json:"rev"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return 0, &ErrNoServer{Board: abs}
	}
	return answer.Rev, nil
}
