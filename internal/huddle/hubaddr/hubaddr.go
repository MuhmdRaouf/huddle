// Package hubaddr answers the local huddle hub's address: the base url the
// huddle home's port file names, probed for health. Kanban's flow client and
// radar's session-start hook both ask it, so one lookup decides for every part
// whether a hub is running here.
package hubaddr

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
)

// ProbeTimeout bounds one health probe: the radar hook runs it on session
// start, so it must fit inside the radar part's session-start budget
// (internal/hookplan) with room for the part's other work.
const ProbeTimeout = 300 * time.Millisecond

// Base answers the local huddle's base url from its port file, in the home the
// environment resolves. Empty when no home or no readable port is there.
func Base(getenv func(string) (string, bool)) string {
	h, err := home.Resolve(func(key string) string { value, _ := getenv(key); return value })
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

// Healthy says whether the port's holder answers huddle's health path: the
// port file alone never vouches for a server.
func Healthy(ctx context.Context, base string) bool {
	probe, cancel := context.WithTimeout(ctx, ProbeTimeout)
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
