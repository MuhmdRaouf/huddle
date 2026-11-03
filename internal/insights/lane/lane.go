// Package lane asks a model through huddle-router: one Anthropic-shaped messages request to the
// router's loopback address. The router picks the provider's lane by the model id and adds that
// provider's key, so the request carries no credential. It lives outside insights/report, which
// never imports net/http.
package lane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/insights/report"
)

// MaxOutputTokens caps the report the model writes.
const MaxOutputTokens = 4000

// askTimeout bounds one request: a report over a full window takes a minute or two.
const askTimeout = 10 * time.Minute

// Lane is huddle-router at a base address.
type Lane struct {
	baseURL string
	client  *http.Client
}

var _ report.Lane = Lane{}

// New returns a lane for the router at baseURL, like http://127.0.0.1:18786.
func New(baseURL string) Lane {
	// the router is on loopback: no proxy variable of the environment applies
	return Lane{baseURL: strings.TrimSuffix(baseURL, "/"), client: &http.Client{Transport: &http.Transport{Proxy: nil}}}
}

type request struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system"`
	Messages  []message `json:"messages"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type response struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
}

// Ask sends the system text and one user message and answers the model's text with its usage. An
// error names the status and never the answer's body, which may echo the request.
func (l Lane) Ask(ctx context.Context, model, system, user string) (report.Reply, error) {
	body, err := json.Marshal(request{Model: model, MaxTokens: MaxOutputTokens, System: system, Messages: []message{{Role: "user", Content: user}}})
	if err != nil {
		return report.Reply{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, askTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return report.Reply{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	res, err := l.client.Do(req)
	if err != nil {
		return report.Reply{}, fmt.Errorf("huddle-router is not reachable at %s", l.baseURL)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, res.Body)
		return report.Reply{}, fmt.Errorf("huddle-router answered %d", res.StatusCode)
	}
	var answer response
	if err := json.NewDecoder(res.Body).Decode(&answer); err != nil {
		return report.Reply{}, fmt.Errorf("huddle-router's answer is not a message: %w", err)
	}
	var text strings.Builder
	for _, block := range answer.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	return report.Reply{Text: text.String(), InputTokens: answer.Usage.InputTokens, OutputTokens: answer.Usage.OutputTokens}, nil
}
