package outside

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
)

// Radar answers in 800 ms or not at all, and one answer stands for 5 seconds.
const (
	radarTimeout = 800 * time.Millisecond
	radarTTL     = 5 * time.Second
)

// Alert is one of radar's alerts (stuck, loop, retry storm, budget, context) as the dashboard
// shows it. A radar that sends no agent or no cost leaves them null.
type Alert struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	SessionID string   `json:"sessionId"`
	AgentID   *string  `json:"agentId"`
	Project   string   `json:"project"`
	Since     float64  `json:"since"`
	Detail    string   `json:"detail"`
	CostUSD   *float64 `json:"costUsd"`
}

// Range is the window radar sums attribution over.
type Range string

const (
	RangeDay   Range = "day"
	RangeWeek  Range = "week"
	RangeMonth Range = "month"
)

// radarAnswer is one cached answer, ok or not.
type radarAnswer struct {
	at   time.Time
	body []byte
	ok   bool
}

// RadarSource answers one radar API path in-process — the bytes the loopback API would answer —
// so a huddle that runs radar's tab itself reads it without a socket. False when the path has
// no answer.
type RadarSource interface {
	RadarAnswer(ctx context.Context, path string) ([]byte, bool)
}

// Radar reads the radar plugin's state when it runs on this machine: through Source when a
// huddle serves radar in-process, else its port from $RADAR_HOME/port (else ~/.agents/radar)
// and its answers over the loopback API. Radar absent, dead, slow or answering garbage reads as
// a quiet empty result: the parts of the dashboard that show its data are simply absent.
type Radar struct {
	Env    func(string) string
	Now    func() time.Time
	Client *http.Client
	// Source answers in-process when huddle runs radar's tab; nil reads radar over HTTP.
	Source RadarSource

	mu    sync.Mutex
	cache map[string]radarAnswer
}

// NewRadar reads the environment, the clock and the HTTP budget from the process.
func NewRadar() *Radar {
	return &Radar{
		Env:    os.Getenv,
		Now:    time.Now,
		Client: &http.Client{Timeout: radarTimeout},
		cache:  map[string]radarAnswer{},
	}
}

func (r *Radar) env(name string) string {
	if r.Env != nil {
		return r.Env(name)
	}
	return os.Getenv(name)
}

func (r *Radar) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// StateDir is radar's state dir: $RADAR_HOME, else ~/.agents/radar.
func (r *Radar) StateDir() string {
	if home := r.env("RADAR_HOME"); home != "" {
		return home
	}
	home := r.env("HOME")
	if home == "" {
		if fallback, err := os.UserHomeDir(); err == nil {
			home = fallback
		}
	}
	return path.Join(home, ".agents", "radar")
}

// Port is the port radar's state dir names, 0 when radar is not there or the file is not a port.
func (r *Radar) Port() int {
	data, err := os.ReadFile(filepath.Join(r.StateDir(), "port"))
	if err != nil {
		return 0
	}
	port, err := strconv.ParseFloat(jsscalar.Trim(string(data)), 64)
	if err != nil || port <= 0 || port >= 65536 || port != math.Trunc(port) {
		return 0
	}
	return int(port)
}

// get answers one path's JSON body: from the cache while it stands, else from the in-process
// source when huddle runs radar's tab, else over HTTP from the port radar's state dir names. A
// failure is cached too, so a dead radar is not asked again and again.
func (r *Radar) get(ctx context.Context, path string) ([]byte, bool) {
	port := 0
	key := "tab " + path
	if r.Source == nil {
		port = r.Port()
		if port == 0 {
			return nil, false
		}
		key = strconv.Itoa(port) + path
	}
	now := r.now()
	r.mu.Lock()
	answer, cached := r.cache[key]
	r.mu.Unlock()
	if cached && now.Sub(answer.at) < radarTTL {
		return answer.body, answer.ok
	}
	var body []byte
	var ok bool
	if r.Source != nil {
		body, ok = r.Source.RadarAnswer(ctx, path)
	} else {
		body, ok = r.fetch(ctx, port, path)
	}
	r.mu.Lock()
	r.cache[key] = radarAnswer{at: now, body: body, ok: ok}
	r.mu.Unlock()
	return body, ok
}

// fetch makes the one request: the host is exactly 127.0.0.1:<port>, the only one radar's
// DNS-rebinding guard answers.
func (r *Radar) fetch(ctx context.Context, port int, path string) ([]byte, bool) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("http://127.0.0.1:%d%s", port, path), nil)
	if err != nil {
		return nil, false
	}
	request.Header.Set("Accept", "application/json")
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: radarTimeout}
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, false
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, response.Body)
		return nil, false
	}
	body, err := io.ReadAll(response.Body)
	if err != nil || !json.Valid(body) {
		return nil, false
	}
	return body, true
}

// Alerts lists radar's current alerts, false when radar is absent or its answer is not a list.
// Only an alert with a string sessionId is anyone's; every other field falls back.
func (r *Radar) Alerts(ctx context.Context) ([]Alert, bool) {
	body, ok := r.get(ctx, "/api/alerts")
	if !ok {
		return nil, false
	}
	var top struct {
		Alerts *[]json.RawMessage `json:"alerts"`
	}
	if err := json.Unmarshal(body, &top); err != nil || top.Alerts == nil {
		return nil, false
	}
	var alerts []Alert
	for _, raw := range *top.Alerts {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			continue
		}
		var session string
		if err := json.Unmarshal(fields["sessionId"], &session); err != nil {
			continue
		}
		alerts = append(alerts, Alert{
			ID:        jsString(fields, "id", ""),
			Kind:      jsString(fields, "kind", "alert"),
			SessionID: session,
			AgentID:   jsStringPtr(fields["agentId"]),
			Project:   jsString(fields, "project", ""),
			Since:     jsNumber(fields["since"]),
			Detail:    jsString(fields, "detail", ""),
			CostUSD:   jsNumberPtr(fields["costUsd"]),
		})
	}
	return alerts, true
}

// SessionCosts is radar's estimated cost per Claude session over a window, in the order its
// rows name them: a repeated key folds into the cost of its first position, the way a Map
// keyed by session does. The order is the one the answers that quote it keep.
type SessionCosts struct {
	order []string
	usd   map[string]float64
}

// add folds one row's cost into its key, keeping the key's first position.
func (s *SessionCosts) add(key string, cost float64) {
	if _, seen := s.usd[key]; !seen {
		s.order = append(s.order, key)
	}
	s.usd[key] += cost
}

// Keys answers the session keys in radar's row order.
func (s *SessionCosts) Keys() []string { return s.order }

// USD answers one session's cost over the window.
func (s *SessionCosts) USD(key string) float64 { return s.usd[key] }

// Len answers how many sessions carry a cost.
func (s *SessionCosts) Len() int { return len(s.order) }

// CostBySession sums radar's estimated cost per Claude session over the window, nil when
// radar is absent or its answer is not a list. A row counts only with a string key and a
// finite number for its cost; the rest of the row is not read here.
func (r *Radar) CostBySession(ctx context.Context, window Range) (*SessionCosts, bool) {
	if window == "" {
		window = RangeDay
	}
	body, ok := r.get(ctx, "/api/attribution?by=session&range="+string(window))
	if !ok {
		return nil, false
	}
	var top struct {
		Rows *[]json.RawMessage `json:"rows"`
	}
	if err := json.Unmarshal(body, &top); err != nil || top.Rows == nil {
		return nil, false
	}
	costs := &SessionCosts{usd: map[string]float64{}}
	for _, raw := range *top.Rows {
		var row struct {
			Key     *string         `json:"key"`
			CostUSD json.RawMessage `json:"costUsd"`
		}
		if err := json.Unmarshal(raw, &row); err != nil || row.Key == nil || !isNumberToken(row.CostUSD) {
			continue
		}
		cost, err := strconv.ParseFloat(string(row.CostUSD), 64)
		if err != nil { // a number too large to be finite
			continue
		}
		costs.add(*row.Key, cost)
	}
	return costs, true
}

// ResetCache forgets the cached answers.
func (r *Radar) ResetCache() {
	r.mu.Lock()
	r.cache = map[string]radarAnswer{}
	r.mu.Unlock()
}

// jsString reads one member the way the dashboard does: missing and null fall back to the
// default, a scalar becomes its JavaScript string.
func jsString(fields map[string]json.RawMessage, key, fallback string) string {
	raw, present := fields[key]
	if !present || string(raw) == "null" {
		return fallback
	}
	return jsText(raw, fallback)
}

// jsStringPtr reads a nullable member: null and missing are nil, a scalar becomes its string.
func jsStringPtr(raw json.RawMessage) *string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	text := jsText(raw, "")
	return &text
}

func jsText(raw json.RawMessage, fallback string) string {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		if value, err := number.Float64(); err == nil {
			return jsscalar.JSNumber(value)
		}
		return fallback
	}
	switch string(raw) {
	case "true":
		return "true"
	case "false":
		return "false"
	}
	return fallback
}

// jsNumber reads one member as a number, 0 for missing, null and text that is not one.
func jsNumber(raw json.RawMessage) float64 {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		if value, err := number.Float64(); err == nil {
			return value
		}
		return 0
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if value, err := strconv.ParseFloat(text, 64); err == nil {
			return value
		}
	}
	if string(raw) == "true" {
		return 1
	}
	return 0
}

// jsNumberPtr reads one member as a number, nil for anything but one: a cost shows only when
// radar sent a number, and a quoted number is a string.
func jsNumberPtr(raw json.RawMessage) *float64 {
	if !isNumberToken(raw) {
		return nil
	}
	value, err := strconv.ParseFloat(string(raw), 64)
	if err != nil {
		return nil
	}
	return &value
}

// isNumberToken reports whether raw is a JSON number, not a quoted one: json.Number accepts
// both, and typeof draws the line.
func isNumberToken(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	return strings.ContainsRune("-0123456789", rune(raw[0]))
}
