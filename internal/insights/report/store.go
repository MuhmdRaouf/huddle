package report

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// RunStatus is where a run stands.
type RunStatus string

const (
	RunStarting RunStatus = "starting" // the hub named the run and handed it to a process
	RunRunning  RunStatus = "running"
	RunDone     RunStatus = "done"
	RunFailed   RunStatus = "failed"
)

// Step is what a running run is doing, for the progress the hub shows.
type Step string

const (
	StepReading Step = "reading" // building the payload from the stores
	StepAsking  Step = "asking"  // waiting on the lane's answer
)

// Run is one insights run as its file keeps it: who it asked, what it sent, how it ended, and
// the model's answer. It stays on this machine.
type Run struct {
	ID           string     `json:"id"`
	Status       RunStatus  `json:"status"`
	Step         Step       `json:"step,omitempty"`
	Provider     string     `json:"provider,omitempty"`
	Model        string     `json:"model,omitempty"`
	Window       WindowView `json:"window"`
	StartedAt    time.Time  `json:"startedAt"`
	FinishedAt   time.Time  `json:"finishedAt,omitzero"`
	InputTokens  int64      `json:"inputTokens,omitempty"`
	OutputTokens int64      `json:"outputTokens,omitempty"`
	Answer       string     `json:"answer,omitempty"`
	Error        string     `json:"error,omitempty"`
}

// InvalidRunIDError says an id could not name a file inside the runs directory.
type InvalidRunIDError struct{ ID string }

func (e *InvalidRunIDError) Error() string { return fmt.Sprintf("%q is not a run id", e.ID) }

var runIDPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z-]*$`)

// RunStore keeps each run as one JSON file, <id>.json, in a private directory under the huddle
// home. Ids start with their start time, so the file names sort by age.
type RunStore struct{ dir string }

// NewRunStore keeps runs in dir, which is created on the first save.
func NewRunStore(dir string) *RunStore { return &RunStore{dir: dir} }

func (s *RunStore) file(id string) (string, error) {
	if !runIDPattern.MatchString(id) {
		return "", &InvalidRunIDError{ID: id}
	}
	return filepath.Join(s.dir, id+".json"), nil
}

// Save writes the run's file whole, mode 0600, by renaming a finished temp file over the old
// one, so a reader never sees half a run.
func (s *RunStore) Save(r Run) error {
	file, err := s.file(r.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(s.dir, ".run-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(raw); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), file)
}

// Load reads one run.
func (s *RunStore) Load(id string) (Run, error) {
	file, err := s.file(id)
	if err != nil {
		return Run{}, err
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return Run{}, err
	}
	var r Run
	if err := json.Unmarshal(raw, &r); err != nil {
		return Run{}, fmt.Errorf("read run %s: %w", id, err)
	}
	return r, nil
}

// List reads the newest runs first, at most limit of them; a directory that does not exist yet
// holds none. A file that does not parse is skipped.
func (s *RunStore) List(limit int) ([]Run, error) {
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if id, ok := strings.CutSuffix(e.Name(), ".json"); ok && runIDPattern.MatchString(id) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	slices.Reverse(ids)
	var runs []Run
	for _, id := range ids {
		if len(runs) == limit {
			break
		}
		if r, err := s.Load(id); err == nil {
			runs = append(runs, r)
		}
	}
	return runs, nil
}

// NewRunID names a run: the start time to the second in UTC, then six random hex digits, so ids
// sort by age and two runs in one second differ.
func NewRunID(now time.Time) string {
	var suffix [3]byte
	_, _ = rand.Read(suffix[:])
	return now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(suffix[:])
}
