// The SessionStart and UserPromptSubmit hook's tips line: one tip the hub holds about this
// session, added to its context, while the owner's tips-to-sessions switch is on. A tip reaches
// a session once: the ids the session has seen sit in <huddle home>/seen/<session id>.tips.json.
// Silent when the switch is off (the default: a tip line spends the session's tokens), when the
// hook is a subagent's (the context is the parent's to spend), when the session id is not one
// the hub names sessions by, when the hub holds nothing new, or on any failure (quiet.go).
package hooks

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/MuhmdRaouf/huddle/internal/huddle/cli"
	"github.com/MuhmdRaouf/huddle/internal/insights"
)

// The deadlines the tips part grants itself, in milliseconds: reading its stdin, then the one
// fetch of the insights route. Together they sit inside the 400 ms the plan grants the part on a
// prompt, its smaller slice.
const (
	tipsStdinMS = 100
	tipsFetchMS = 250
)

// sessionIDOK is the shape the hub names sessions by; a session id outside it asks for nothing.
var sessionIDOK = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// Tips brings this session's first unseen tip into its context, once per tip.
func Tips(ctx context.Context, w *cli.World, in io.Reader, out io.Writer) {
	Run(ctx, w, "tips", func() error {
		var input hookInput
		readInput(in, tipsStdinMS, &input)
		if input.AgentID != "" || !sessionIDOK.MatchString(input.SessionID) {
			return nil
		}
		home := cli.Home(ctx, w)
		if home == "" {
			return nil
		}
		// The switch gates everything: off, the session asks the hub for nothing.
		if !insights.ReadSettings(insights.SettingsFile(home)).TipsToSessions {
			return nil
		}
		id := cli.Identify(ctx, w, input.SessionID)
		if id.URL == "" {
			return nil
		}
		tips := cli.SessionTips(ctx, w, id, input.SessionID, tipsFetchMS)
		ledger := filepath.Join(home, "seen", input.SessionID+".tips.json")
		seen := readSeen(ledger)
		for _, tip := range tips {
			if hasID(seen, tip.ID) {
				continue
			}
			writeContext(out, input.HookEventName, "Huddle tip: "+tip.Sentence)
			// A ledger that cannot be written leaves the tip shown: the next prompt may
			// show it again, which is no harm.
			return writeSeen(ledger, append(seen, tip.ID))
		}
		return nil
	})
}

// hasID answers whether the list of seen ids already holds one id.
func hasID(seen []string, id string) bool {
	for _, got := range seen {
		if got == id {
			return true
		}
	}
	return false
}

// readSeen answers the tip ids the ledger holds; a missing or broken file holds none.
func readSeen(path string) []string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var seen []string
	if json.Unmarshal(raw, &seen) != nil {
		return nil
	}
	return seen
}

// writeSeen adds the ids to the ledger through a temp file in the ledger's folder and a rename,
// so a reader never meets a half-written list.
func writeSeen(path string, seen []string) error {
	raw, err := json.Marshal(seen)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".tips-*.json")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name) // nothing left once the rename has moved it
	if _, err := temp.Write(raw); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return err
	}
	return os.Rename(name, path)
}
