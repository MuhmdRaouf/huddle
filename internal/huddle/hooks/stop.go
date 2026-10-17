// The Stop hook: a session in a channel must not go quiet while someone waits for its answer.
// If its inbox holds fresh asks (from the owner or another session), block the stop once with
// the list. Never twice for the same ask: the asks it blocked for are remembered
// (<Huddle home>/stop-raised.json), and an ask older than HUDDLE_STOP_MAX_AGE seconds (default
// an hour) never blocks — its asker may be long gone. stop_hook_active guards against a loop;
// an unreachable Huddle, a session that has not joined (401), a state file that cannot be
// written, or any failure never blocks (quiet.go).
package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/cli"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// stopKeep caps the asks remembered per session identity.
const stopKeep = 500

// stopRaised is the file the asks it blocked for are remembered in, keyed by channel/session.
func stopRaised(home string) string { return filepath.Join(home, "stop-raised.json") }

// stopItem is one message of the inbox's answer.
type stopItem struct {
	Seq  int64  `json:"seq"`
	From string `json:"from"`
	Msg  string `json:"msg"`
	Ts   string `json:"ts"`
}

// blockOutput is the one answer this hook gives: a blocked stop with the asks that wait.
type blockOutput struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

// Stop blocks the stop once while fresh asks wait for a reply, and stays silent every other way.
func Stop(ctx context.Context, w *cli.World, in io.Reader, out io.Writer) {
	Run(ctx, w, "stop", func() error {
		var input hookInput
		readInput(in, stopStdinMS, &input)
		id := cli.Identify(ctx, w, input.SessionID)
		if id.Channel == "" || id.As == "" {
			return nil
		}
		if input.StopHookActive || input.AgentID != "" {
			return nil // subagents answer through their parent
		}
		callURL := fmt.Sprintf("%s/api/c/%s/op/inbox?as=%s", id.URL, url.PathEscape(id.Channel), url.PathEscape(id.As))
		resp, err := cli.HFetch(ctx, w, http.MethodPost, callURL,
			map[string]string{"content-type": "application/json"}, []byte("{}"),
			time.Duration(stopInboxMS)*time.Millisecond, input.SessionID)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		var answer struct {
			Result json.RawMessage `json:"result"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
			return err
		}
		var inbox []stopItem
		if len(answer.Result) > 0 {
			_ = json.Unmarshal(answer.Result, &inbox)
		}
		maxAgeMS := stopMaxAgeMS(w) // NaN never blocks: nothing is fresh beside it
		fresh := func(item stopItem) bool {
			ts, ok := jsfmt.ParseISO(item.Ts)
			return ok && float64(nowOf(w).UnixMilli()-ts.UnixMilli()) <= maxAgeMS
		}
		home := cli.Home(ctx, w)
		key := id.Channel + "/" + id.As
		raised := readRaised(stopRaised(home))
		seen := raisedSeqs(raised, key)
		open := make([]stopItem, 0, len(inbox))
		for _, item := range inbox {
			if fresh(item) && !seenHas(seen, item.Seq) {
				open = append(open, item)
			}
		}
		if len(open) == 0 {
			return nil
		}
		// remember first: a stop that cannot record what it raised does not block at all
		remembered := append([]int64{}, seen...)
		for _, item := range open {
			remembered = append(remembered, item.Seq)
		}
		if len(remembered) > stopKeep {
			remembered = remembered[len(remembered)-stopKeep:]
		}
		if !writeRaised(stopRaised(home), raised, key, remembered) {
			return nil
		}
		lines := make([]string, 0, len(open))
		for _, item := range open {
			lines = append(lines, fmt.Sprintf("  #%d from %s: %s", item.Seq, item.From, item.Msg))
		}
		reason := fmt.Sprintf("Huddle: %d message(s) wait for your reply before you stop:\n%s\n"+
			"Answer each with reply (seq, msg). If the work continues after you, wait on the channel instead of stopping.",
			len(open), strings.Join(lines, "\n"))
		writeJSON(out, blockOutput{Decision: "block", Reason: reason})
		return nil
	})
}

// stopMaxAgeMS answers HUDDLE_STOP_MAX_AGE in milliseconds, defaulting to an hour. A value that
// is not a number is NaN: no ask can be fresh beside it, so nothing blocks.
func stopMaxAgeMS(w *cli.World) float64 {
	raw := getenvOf(w, "HUDDLE_STOP_MAX_AGE")
	if raw == "" {
		raw = "3600"
	}
	seconds, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return math.NaN()
	}
	return seconds * 1000
}

// readRaised reads the asks already raised, keeping every key and its order for the rewrite.
func readRaised(file string) *jsfmt.Value {
	raw, err := os.ReadFile(file)
	if err != nil {
		return jsfmt.Object(nil, nil)
	}
	value, err := jsfmt.DecodeValue(raw)
	if err != nil || value == nil || value.Kind() != jsfmt.KindObject {
		return jsfmt.Object(nil, nil)
	}
	return value
}

// raisedSeqs answers the seqs remembered for one key.
func raisedSeqs(raised *jsfmt.Value, key string) []int64 {
	field, ok := fieldOf(raised, key)
	if !ok || field.Kind() != jsfmt.KindArray {
		return nil
	}
	var out []int64
	for _, item := range field.Items() {
		if item.Kind() == jsfmt.KindNumber {
			if n, err := strconv.ParseInt(item.EncodeJSON(), 10, 64); err == nil {
				out = append(out, n)
			}
		}
	}
	return out
}

// seenHas answers whether a seq is remembered.
func seenHas(seen []int64, seq int64) bool {
	for _, s := range seen {
		if s == seq {
			return true
		}
	}
	return false
}

// writeRaised records the seqs raised for one key, keeping every other key and the order, by a
// temp file and a rename. It answers whether the record landed.
func writeRaised(file string, raised *jsfmt.Value, key string, seqs []int64) bool {
	items := make([]*jsfmt.Value, 0, len(seqs))
	for _, seq := range seqs {
		items = append(items, jsfmt.Number(float64(seq)))
	}
	keys := append([]string{}, raised.Keys()...)
	fields := append([]*jsfmt.Value{}, raised.Fields()...)
	at := -1
	for i, name := range keys {
		if name == key {
			at = i
			break
		}
	}
	if at >= 0 {
		fields[at] = jsfmt.Array(items)
	} else {
		keys = append(keys, key)
		fields = append(fields, jsfmt.Array(items))
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return false
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, []byte(jsfmt.Object(keys, fields).EncodeJSON()), 0o644); err != nil {
		return false
	}
	return os.Rename(tmp, file) == nil
}

// fieldOf answers one object field's value and whether it is present.
func fieldOf(value *jsfmt.Value, key string) (*jsfmt.Value, bool) {
	if value == nil || value.Kind() != jsfmt.KindObject {
		return nil, false
	}
	for i, name := range value.Keys() {
		if name == key {
			return value.Fields()[i], true
		}
	}
	return nil, false
}
