package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/process"
)

// The server's own lifecycle, what is left of it: nothing starts a server any
// more — the huddle hub serves every board in process — so the commands here
// are the ones a board someone serves by hand still needs. Refresh rescans
// through the running server; Serve is the foreground server `kanban serve`
// runs for tests and debugging.

// Effects is every effect the server commands have; the cli builds the real
// ones, the tests record them.
type Effects struct {
	// Fetch answers one loopback request; ok false is network trouble, never
	// an error.
	Fetch func(ctx context.Context, method, url string, header map[string]string) (int, []byte, bool)
	// OpenBrowser hands one url to the platform's opener.
	OpenBrowser func(url string)
}

// DefaultEffects builds the real ones: a bounded loopback client and the
// platform browser opener.
func DefaultEffects() Effects {
	return Effects{
		Fetch:       fetch,
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
