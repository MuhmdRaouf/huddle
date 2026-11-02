// The change feed: the watcher asks the operating system (kqueue, inotify) to report creates,
// writes and renames in the folders it reads, and reads only the file that changed. It watches
// the spool, the projects dir, and every folder holding a transcript, or itself modified, within
// the watch window. A watch costs file descriptors, so the feed holds at most a quarter of the
// process's soft open-files limit, and each fallback scan unwatches the folders that fell out
// of the window. The fallback scan reads what the feed cannot report: a folder made before the watch
// armed, an event the system dropped, and every folder outside the window or past the budget.
package ingest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// FolderWatch is the operating system's change feed for folders: Add arms one folder,
// Events carries the creates, writes and renames inside the armed ones, Errors what the
// feed could not deliver.
type FolderWatch interface {
	Add(dir string) error
	Remove(dir string) error
	// Cost is how many file descriptors watching dir takes.
	Cost(dir string) int
	// WatchFile holds a watch on a file just created in an armed folder, and answers once it
	// holds: a write after that raises an event, a write before it is for the reader to find.
	WatchFile(path string) error
	Events() <-chan fsnotify.Event
	Errors() <-chan error
	Close() error
}

// StatFunc is os.Stat's shape: the one call the passes' stat-ing goes through.
type StatFunc func(path string) (os.FileInfo, error)

// watchWindow is how recently a transcript must have been modified for its folder to be
// watched: the same 24 hours the Sessions archive and the store's age window keep.
const watchWindow = 24 * time.Hour

// fallbackOpenFilesLimit is the soft open-files limit assumed when the system will not say.
const fallbackOpenFilesLimit = 256

// maxChangesPerRead bounds how many queued events one read takes before it reads the files.
const maxChangesPerRead = 1_024

// osFolderWatch is the feed fsnotify gives: kqueue on macOS, inotify on Linux.
type osFolderWatch struct{ inner *fsnotify.Watcher }

func openOSFolderWatch() (FolderWatch, error) {
	inner, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return osFolderWatch{inner: inner}, nil
}

func (o osFolderWatch) Add(dir string) error          { return o.inner.Add(dir) }
func (o osFolderWatch) Cost(dir string) int           { return folderCost(dir, runtime.GOOS != "linux") }
func (o osFolderWatch) Remove(dir string) error       { return o.inner.Remove(dir) }
func (o osFolderWatch) Events() <-chan fsnotify.Event { return o.inner.Events }
func (o osFolderWatch) Errors() <-chan error          { return o.inner.Errors }
func (o osFolderWatch) Close() error                  { return o.inner.Close() }

// WatchFile is a kqueue watch of its own: kqueue reports a new file's creation before it watches
// the file, so a write in between raises no event. inotify's folder watch reports every file's
// writes from the moment the file exists.
func (o osFolderWatch) WatchFile(path string) error {
	if runtime.GOOS == "linux" {
		return nil
	}
	return o.inner.Add(path)
}

// openFeed opens the change feed and answers its two channels; a feed that cannot open leaves
// the fallback scan to read every folder, and the nil channels never fire.
func (w *Watcher) openFeed() (<-chan fsnotify.Event, <-chan error) {
	open := w.newFeed
	if open == nil {
		open = openOSFolderWatch
	}
	feed, err := open()
	if err != nil {
		w.log(fmt.Sprintf("radar: the folder watch did not open (%v); the %d s scan reads every folder",
			err, w.intervalMs/1_000))
		return nil, nil
	}
	w.feed = feed
	w.fdBudget = openFilesBudget(w.openFilesLimit)
	w.mu.Lock()
	w.watching = true
	w.mu.Unlock()
	return feed.Events(), feed.Errors()
}

func (w *Watcher) closeFeed() {
	if w.feed != nil {
		_ = w.feed.Close()
	}
}

// wantWatch asks for dir to be armed after the running pass; the lock is held.
func (w *Watcher) wantWatch(dir string) {
	if !w.watching {
		return
	}
	if _, armed := w.armed[dir]; armed {
		return
	}
	w.wantArm.add(dir)
}

// trackTranscript starts tailing a transcript; the lock is held.
func (w *Watcher) trackTranscript(path string) {
	if w.transcriptTails.has(path) {
		return
	}
	w.transcriptTails.set(path, w.transcriptTail(path))
}

// tailFile reads a tail's new bytes through the watcher's stat call.
func (w *Watcher) tailFile(state *TailState) TailResult {
	return tailFileUsing(w.stat, state, TailChunkBytes)
}

// settleFeed follows a scan: it unwatches the folders that fell out of the window, asks for
// the ones that hold a recent transcript, and arms them.
func (w *Watcher) settleFeed() {
	if w.feed == nil {
		return
	}
	w.reconcileWatches()
	w.catchUpArmed()
}

// reconcileWatches makes the watched folders the recent ones: a folder holding a transcript
// modified within the window, or itself modified within it (a session folder waiting for its
// first subagent). A watched folder that is neither is removed and its cost released; a folder
// holding a recent transcript that is not watched is asked for. A scan also lifts the block a
// spent budget set, and re-counts the folders that stay, which grow as files land in them.
func (w *Watcher) reconcileWatches() {
	w.armBlocked = false
	w.mu.Lock()
	cutoff := w.now() - watchWindow.Milliseconds()
	recent := make(map[string]struct{})
	var recentOrder []string
	for _, tail := range w.transcriptTails.snapshot() {
		dir := filepath.Dir(tail.path)
		if _, seen := recent[dir]; !seen && tail.modMs >= cutoff {
			recent[dir] = struct{}{}
			recentOrder = append(recentOrder, dir)
		}
	}
	var kept, withoutTranscript []string
	for dir := range w.armed {
		switch _, holdsRecent := recent[dir]; {
		case dir == w.projectsRoot || dir == w.spoolDir:
		case holdsRecent:
			kept = append(kept, dir)
		default:
			withoutTranscript = append(withoutTranscript, dir)
		}
	}
	for _, dir := range recentOrder {
		w.wantWatch(dir)
	}
	w.mu.Unlock()
	var quiet []string
	for _, dir := range withoutTranscript {
		if modMs, ok := w.mtimeOf(dir); ok && modMs >= cutoff {
			kept = append(kept, dir)
		} else {
			quiet = append(quiet, dir)
		}
	}
	for _, dir := range quiet {
		_ = w.feed.Remove(dir)
		w.mu.Lock()
		w.heldCost -= w.armed[dir]
		delete(w.armed, dir)
		w.mu.Unlock()
	}
	for _, dir := range kept {
		cost := w.feed.Cost(dir)
		w.mu.Lock()
		w.heldCost += cost - w.armed[dir]
		w.armed[dir] = cost
		w.mu.Unlock()
	}
}

// catchUpArmed arms the folders passes asked for; when it armed any, one full poll reads what
// was written between the pass that found them and the arming.
func (w *Watcher) catchUpArmed() {
	w.mu.Lock()
	dirs := w.wantArm.snapshot()
	w.wantArm = pathSet{}
	w.mu.Unlock()
	armed := false
	for _, dir := range dirs {
		armed = w.arm(dir) || armed
	}
	if armed {
		w.PollOnce()
	}
}

// arm watches one folder; false when the feed refused it. Two limits stop it. The descriptor
// budget stops arming until the next scan; the system's own limit (open files, inotify
// watches) stops it for good, the second guard. Either leaves one log line, and the folders
// left over are the scan's.
func (w *Watcher) arm(dir string) bool {
	if w.feedFull || w.armBlocked {
		return false
	}
	cost := w.feed.Cost(dir)
	w.mu.Lock()
	over := w.heldCost+cost > w.fdBudget
	w.mu.Unlock()
	if over {
		w.armBlocked = true
		w.noteLimit(fmt.Sprintf("its budget of %d file descriptors", w.fdBudget))
		return false
	}
	err := w.feed.Add(dir)
	if err == nil {
		w.mu.Lock()
		w.armed[dir] = cost
		w.heldCost += cost
		w.mu.Unlock()
		return true
	}
	if limit, met := limitMet(err); met {
		_ = w.feed.Remove(dir)
		w.feedFull = true
		w.mu.Lock()
		w.watching = false
		w.wantArm = pathSet{}
		w.mu.Unlock()
		w.noteLimit(limit)
	}
	return false
}

// noteLimit logs the one line a met limit leaves: what the feed holds and the limit it met.
func (w *Watcher) noteLimit(limit string) {
	if w.limitLogged {
		return
	}
	w.limitLogged = true
	w.mu.Lock()
	held := len(w.armed)
	w.mu.Unlock()
	w.log(fmt.Sprintf("radar: the folder watch holds %d watches and met %s; the folders it does not watch are read by the %d s scan",
		held, limit, w.intervalMs/1_000))
}

// folderCost is how many file descriptors watching dir takes: on kqueue the folder and each
// entry in it, on inotify one watch.
func folderCost(dir string, perEntry bool) int {
	if !perEntry {
		return 1
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 1
	}
	return 1 + len(entries)
}

// openFilesBudget is a quarter of the soft open-files limit, read once: the share of the
// process's descriptors the feed may take, the rest staying with the hub's database and sockets.
func openFilesBudget(read func() (uint64, error)) int {
	if read == nil {
		read = softOpenFilesLimit
	}
	limit, err := read()
	if err != nil {
		limit = fallbackOpenFilesLimit
	}
	return int(limit / 4)
}

func softOpenFilesLimit() (uint64, error) {
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
		return 0, err
	}
	return uint64(limit.Cur), nil
}

// limitMet names the limit an Add error ran into: the process's open files for kqueue, the
// user's inotify watches for inotify.
func limitMet(err error) (string, bool) {
	switch {
	case errors.Is(err, syscall.EMFILE):
		var limit syscall.Rlimit
		if syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit) != nil {
			return "the open files limit", true
		}
		return fmt.Sprintf("the open files limit of %d", limit.Cur), true
	case errors.Is(err, syscall.ENFILE):
		return "the system open files limit", true
	case errors.Is(err, syscall.ENOSPC):
		limit, err := os.ReadFile("/proc/sys/fs/inotify/max_user_watches")
		if err != nil {
			return "the inotify watch limit", true
		}
		return "the inotify watch limit of " + strings.TrimSpace(string(limit)), true
	}
	return "", false
}

// readChanges notes first and the events queued behind it, arms what they found, and reads
// the files they named.
func (w *Watcher) readChanges(first fsnotify.Event, events <-chan fsnotify.Event) {
	w.noteChange(first)
drain:
	for range maxChangesPerRead {
		select {
		case event, open := <-events:
			if !open {
				break drain
			}
			w.noteChange(event)
		default:
			break drain
		}
	}
	w.armAsked()
	w.pollChanged()
	// the pass itself asks for the folders of the transcripts a spool line named
	w.armAsked()
}

// armAsked arms the folders asked for, when there are any.
func (w *Watcher) armAsked() {
	w.mu.Lock()
	wantsArming := len(w.wantArm.order) > 0
	w.mu.Unlock()
	if wantsArming {
		w.catchUpArmed()
	}
}

// noteFeedFailure answers an error the feed reports, an overflow of its event queue among
// them: events may be lost, so one full poll reads everything; the first failure leaves a line.
func (w *Watcher) noteFeedFailure(err error) {
	if !w.failureLogged {
		w.failureLogged = true
		w.log(fmt.Sprintf("radar: the folder watch reported an error (%v); a full read follows", err))
	}
	w.PollOnce()
}

// noteChange files one event: a spool or transcript file that changed is read by the next
// pass, a transcript seen for the first time is tailed, a folder made under the projects dir
// is armed and read. A file created is watched before anything reads it, so a write that lands
// while the watch is made is in what the read finds.
func (w *Watcher) noteChange(event fsnotify.Event) {
	if !event.Has(fsnotify.Create) && !event.Has(fsnotify.Write) && !event.Has(fsnotify.Rename) {
		return
	}
	path := event.Name
	if event.Has(fsnotify.Create) && (w.isSpoolFile(path) || strings.HasSuffix(path, ".jsonl")) {
		_ = w.feed.WatchFile(path)
	}
	switch {
	case w.isSpoolFile(path):
		w.markChanged(path)
	case strings.HasSuffix(path, ".jsonl"):
		w.noteTranscript(path)
	case strings.HasSuffix(path, ".meta.json"):
		w.noteTranscript(strings.TrimSuffix(path, ".meta.json") + ".jsonl")
	case event.Has(fsnotify.Create) && strings.HasPrefix(path, w.projectsRoot+string(filepath.Separator)):
		if info, err := w.stat(path); err == nil && info.IsDir() {
			w.discoverFolder(path, depthUnder(w.projectsRoot, path))
		}
	}
}

// isSpoolFile says path is one of the spool's day files.
func (w *Watcher) isSpoolFile(path string) bool {
	return filepath.Dir(path) == w.spoolDir && dateFile.MatchString(filepath.Base(path))
}

// changedSpoolFiles is the reported spool files, in name order.
func (w *Watcher) changedSpoolFiles(changed map[string]struct{}) []string {
	var files []string
	for path := range changed {
		if w.isSpoolFile(path) {
			files = append(files, path)
		}
	}
	slices.Sort(files)
	return files
}

// markChanged records a file for the next pass over the reported changes.
func (w *Watcher) markChanged(path string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.noteChangedLocked(path)
}

// noteChangedLocked is markChanged with the lock held.
func (w *Watcher) noteChangedLocked(path string) {
	if w.changed == nil {
		w.changed = make(map[string]struct{})
	}
	w.changed[path] = struct{}{}
}

// noteTranscript tails a transcript the first time it is reported, asks for its folder to be
// watched, and marks it changed. A subagent's transcript waits for its meta file, which names
// the agent and is read once, when the tail opens: a subagent without one is the scan's.
func (w *Watcher) noteTranscript(path string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.noteTranscriptLocked(path)
}

// noteTranscriptLocked is noteTranscript with the lock held.
func (w *Watcher) noteTranscriptLocked(path string) {
	if !w.transcriptTails.has(path) {
		modMs, ok := w.openable(path)
		if !ok {
			return
		}
		w.trackTranscript(path)
		w.transcriptTails.files[path].modMs = modMs
		w.wantWatch(filepath.Dir(path))
	}
	w.noteChangedLocked(path)
}

// tailNamedTranscript tails the transcript a spool line names, and the subagents its session
// already has, the moment a feed pass reads the line: the hook that named the file wrote it
// first, and the folder holding it may be one the feed does not watch yet. The line's folder is
// asked for; one past the descriptor budget stays with the fallback scan. The lock is held.
func (w *Watcher) tailNamedTranscript(line shared.SpoolLine) {
	if !w.watching || line.TranscriptPath == nil || *line.TranscriptPath == "" {
		return
	}
	path := *line.TranscriptPath
	if !strings.HasSuffix(path, ".jsonl") || w.transcriptTails.has(path) {
		return
	}
	w.noteTranscriptLocked(path)
	if line.SessionID == nil || !w.transcriptTails.has(path) {
		return
	}
	for _, subagent := range ListSubagents(filepath.Join(filepath.Dir(path), *line.SessionID, "subagents")) {
		w.noteTranscriptLocked(subagent)
	}
}

// openable answers a transcript's modified time when it exists and, for a subagent, its meta
// file does too.
func (w *Watcher) openable(path string) (int64, bool) {
	info, err := w.stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return 0, false
	}
	if IdentifyFile(path).Kind == shared.KindSubagent {
		meta, err := w.stat(strings.TrimSuffix(path, ".jsonl") + ".meta.json")
		if err != nil || !meta.Mode().IsRegular() {
			return 0, false
		}
	}
	return info.ModTime().UnixMilli(), true
}

// discoverFolder arms a folder made after the watch armed, then reads it: its transcripts are
// tailed and its subfolders armed and read in turn. Arming comes first, so a file made while
// the folder is read raises its own event.
func (w *Watcher) discoverFolder(dir string, depth int) {
	if depth > maxWalkDepth {
		return
	}
	w.arm(dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		switch {
		case entry.IsDir():
			w.discoverFolder(path, depth+1)
		case strings.HasSuffix(entry.Name(), ".jsonl"):
			w.noteTranscript(path)
		}
	}
}

// depthUnder is how many folders deep path lies under root.
func depthUnder(root, path string) int {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return maxWalkDepth + 1
	}
	return strings.Count(rel, string(filepath.Separator)) + 1
}
