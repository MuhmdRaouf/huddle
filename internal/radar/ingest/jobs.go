// Optional source: jobs recorded by the provider plugins under their state dirs. Each job
// becomes an external agent on its own pseudo session (`<provider>:<job id>`); attempt files
// are `claude --output-format stream-json` events, which are the same shapes transcript
// parsing already understands. A missing directory is normal and silent.
package ingest

import (
	"cmp"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
)

// JobPlugin is one of the provider plugins whose jobs radar reads.
type JobPlugin string

// The provider plugins whose state dirs hold jobs.
const (
	Zai      JobPlugin = "zai"
	Kimi     JobPlugin = "kimi"
	DeepSeek JobPlugin = "deepseek"
	MiniMax  JobPlugin = "minimax"
	Qwen     JobPlugin = "qwen"
)

// JobPlugins lists every provider plugin whose jobs radar reads, in read order.
var JobPlugins = []JobPlugin{Zai, Kimi, DeepSeek, MiniMax, Qwen}

// ZaiJobUpstream is zai's Anthropic-compatible base URL for jobs (the plugin's own provider
// definition names it).
const ZaiJobUpstream = "https://api.z.ai/api/anthropic"

// liveJobStates are the states a job is actually running in. The engines' own active sets
// agree (queued, running, verifying); a job that landed — awaiting review, accepted, returned
// to its queue's floor, discarded, failed — is not running, and an unknown state never poses
// as a running one.
var liveJobStates = map[string]bool{"queued": true, "running": true, "verifying": true}

// JobAttempt is one attempt of a job: its stream file (attempt-<n>.jsonl) and, when job.json
// says one, the session id the job engine recorded for it.
type JobAttempt struct {
	File      string
	N         int64
	SessionID *string
}

// PluginJob is one job a provider plugin recorded under its state dir: an external agent on
// its own pseudo session.
type PluginJob struct {
	ID        string
	Plugin    JobPlugin
	SessionID string
	Title     *string
	State     *string
	Live      bool
	Model     *string
	UpdatedAt *int64
	Dir       string
	Attempts  []JobAttempt
	// Upstream is where the job's requests go: the plugin's Anthropic-compatible API, when
	// radar knows it.
	Upstream string
	// RepoRoot is the checkout the job runs in, Branch the one it pushes to (edit mode only).
	RepoRoot *string
	Branch   *string
	// ParentSessionID is the Claude Code session that submitted the job, when there was one.
	ParentSessionID *string
}

// JobClaudeHome is the config dir every attempt of a plugin's jobs runs `claude` with: its
// transcripts live under here.
func JobClaudeHome(ctx context.Context, env domain.EnvLookup, plugin JobPlugin) string {
	return filepath.Join(paths.ProviderStateDir(ctx, env, string(plugin)), "claude-home")
}

// readJobJSON reads a job.json as a record; nil when it is missing, broken, or not an object.
func readJobJSON(path string) map[string]any {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	value, err := domain.ParseJSON(string(data))
	if err != nil {
		return nil
	}
	record, _ := value.(map[string]any)
	return record
}

// attemptFile is an attempt's stream file name: attempt-<n>.jsonl.
var attemptFile = regexp.MustCompile(`^attempt-([0-9]+)\.jsonl$`)

// attemptNumber is a stream file's attempt number, or -1 when the name is not one.
func attemptNumber(name string) int64 {
	match := attemptFile.FindStringSubmatch(name)
	if match == nil {
		return -1
	}
	n, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return -1 // more digits than an attempt index ever holds: not an attempt file
	}
	return n
}

// listAttemptFiles lists a job dir's attempt stream files in attempt order.
func listAttemptFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	type named struct {
		name string
		n    int64
	}
	var found []named
	for _, entry := range entries {
		if n := attemptNumber(entry.Name()); n >= 0 {
			found = append(found, named{entry.Name(), n})
		}
	}
	slices.SortStableFunc(found, func(a, b named) int { return cmp.Compare(a.n, b.n) })
	files := make([]string, 0, len(found))
	for _, attempt := range found {
		files = append(files, filepath.Join(dir, attempt.name))
	}
	return files
}

// newestMtime is the newest of the files' mtimes and updatedAt; a file it cannot stat
// contributes nothing.
func newestMtime(files []string, updatedAt *int64) *int64 {
	newest := updatedAt
	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil {
			// an unreadable attempt file just does not contribute its mtime
			continue
		}
		mtime := info.ModTime().UnixMilli()
		if newest == nil || mtime > *newest {
			newest = &mtime
		}
	}
	return newest
}

// recordedSessionID is the session id the job engine recorded for attempt n, when its
// attempts array carries one for that number; anything else reads as unknown.
func recordedSessionID(attempts any, n int64) *string {
	entries, ok := attempts.([]any)
	if !ok {
		return nil
	}
	for _, entry := range entries {
		record, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		entryN, isNumber := record["n"].(float64)
		if !isNumber || entryN != float64(n) {
			continue
		}
		return stringPtr(record["sessionId"])
	}
	return nil
}

// attemptsOf reads a job dir's attempts, each with its recorded session id.
func attemptsOf(dir string, raw any) []JobAttempt {
	files := listAttemptFiles(dir)
	attempts := make([]JobAttempt, 0, len(files))
	for _, file := range files {
		n := attemptNumber(filepath.Base(file))
		attempts = append(attempts, JobAttempt{File: file, N: n, SessionID: recordedSessionID(raw, n)})
	}
	return attempts
}

// objectOf reads one of a job's fields as a record; anything else is the empty record.
func objectOf(job map[string]any, key string) map[string]any {
	if value, ok := job[key].(map[string]any); ok {
		return value
	}
	return map[string]any{}
}

// stillLive says the engine is still working the job; every landed or unknown state reads as
// finished.
func stillLive(state any) bool {
	text, ok := state.(string)
	return ok && liveJobStates[text]
}

// updatedAtOf is the run's last update: its recorded updatedAt, or the newest attempt file's
// mtime.
func updatedAtOf(attempts []JobAttempt, raw any) *int64 {
	var recorded *int64
	if text, ok := raw.(string); ok {
		if parsed, ok := jsscalar.ParseISO(text); ok {
			ms := parsed.UnixMilli()
			recorded = &ms
		}
	}
	files := make([]string, 0, len(attempts))
	for _, attempt := range attempts {
		files = append(files, attempt.File)
	}
	return newestMtime(files, recorded)
}

// jobFromDir reads one job dir; ok is false when it holds no readable job.json.
func jobFromDir(plugin JobPlugin, upstreamOf func(JobPlugin) string, root, id string) (PluginJob, bool) {
	dir := filepath.Join(root, id)
	job := readJobJSON(filepath.Join(dir, "job.json"))
	if job == nil {
		return PluginJob{}, false
	}
	brief := objectOf(job, "brief")
	attempts := attemptsOf(dir, job["attempts"])
	jobID := id
	if text, ok := job["id"].(string); ok {
		jobID = text
	}
	state := (*string)(nil)
	if text, ok := job["state"].(string); ok {
		state = &text // any string counts, an empty one included
	}
	upstream := upstreamOf(plugin)
	if plugin == Zai {
		upstream = ZaiJobUpstream
	}
	return PluginJob{
		ID:              jobID,
		Plugin:          plugin,
		SessionID:       string(plugin) + ":" + id,
		Title:           stringPtr(brief["title"]),
		State:           state,
		Live:            stillLive(job["state"]),
		Model:           qualifiedModel(plugin, stringPtr(brief["model"])),
		UpdatedAt:       updatedAtOf(attempts, job["updatedAt"]),
		Dir:             dir,
		Attempts:        attempts,
		Upstream:        upstream,
		RepoRoot:        stringPtr(objectOf(job, "workspace")["repoRoot"]),
		Branch:          stringPtr(objectOf(job, "workspace")["branch"]),
		ParentSessionID: stringPtr(objectOf(job, "origin")["sessionId"]),
	}, true
}

// qualifiedModel prefixes a known model with its plugin, the id the dashboard groups by.
func qualifiedModel(plugin JobPlugin, model *string) *string {
	if model == nil {
		return nil
	}
	qualified := string(plugin) + ":" + *model
	return &qualified
}

// ReadJobs lists every job under every provider's state dir, newest update first; a provider
// with no jobs dir contributes nothing. A nil upstreamOf answers no upstream.
func ReadJobs(ctx context.Context, env domain.EnvLookup, upstreamOf func(JobPlugin) string) []PluginJob {
	if upstreamOf == nil {
		upstreamOf = func(JobPlugin) string { return "" }
	}
	var jobs []PluginJob
	for _, plugin := range JobPlugins {
		root := filepath.Join(paths.ProviderStateDir(ctx, env, string(plugin)), "jobs")
		ids, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range ids {
			if job, ok := jobFromDir(plugin, upstreamOf, root, entry.Name()); ok {
				jobs = append(jobs, job)
			}
		}
	}
	sort.SliceStable(jobs, func(i, j int) bool { return jobTime(jobs[i]) > jobTime(jobs[j]) })
	return jobs
}

// jobTime is a job's update time, zero when it has none.
func jobTime(job PluginJob) int64 {
	if job.UpdatedAt == nil {
		return 0
	}
	return *job.UpdatedAt
}

// ListSubagents lists the `*.jsonl` transcripts in a session's subagents dir, sorted;
// nothing when there is no such dir.
func ListSubagents(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var files []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".jsonl") {
			files = append(files, filepath.Join(dir, entry.Name()))
		}
	}
	sort.Strings(files)
	return files
}

// AttemptTranscript is where one attempt's transcripts live: its own transcript and the
// subagents it spawned.
type AttemptTranscript struct {
	// File is the attempt's own transcript, `<claudeHome>/projects/<project>/<sessionId>.jsonl`.
	File string
	// Subagents are the transcripts under `<same project dir>/<sessionId>/subagents/`.
	Subagents []string
}

// FindAttemptTranscript finds the transcript one attempt wrote under the plugin's
// claude-home, in any project dir; nil when there is none (older jobs ran before transcripts).
func FindAttemptTranscript(claudeHome, sessionID string) *AttemptTranscript {
	projects := filepath.Join(claudeHome, "projects")
	entries, err := os.ReadDir(projects)
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(projects, entry.Name())
		file := filepath.Join(dir, sessionID+".jsonl")
		if !isFile(file) {
			continue
		}
		return &AttemptTranscript{File: file, Subagents: ListSubagents(filepath.Join(dir, sessionID, "subagents"))}
	}
	return nil
}

// isFile says path is a regular file; a dangling symlink is not one.
func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
