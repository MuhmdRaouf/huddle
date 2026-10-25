// The process sampler: every live session's CPU and memory, read from its pid and every process
// under it. Radar holds no pids; the embedding hub's registry names them. One process table
// serves every session of a pass, and a pass with no live session reads no table at all.
package server

import (
	"context"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters"
	dayclock "github.com/MuhmdRaouf/huddle/internal/radar/clock"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

// ProcSampleEvery is how often the sampler reads the process table.
const ProcSampleEvery = 10 * time.Second

// SessionPid is one live session's process, as the registry names it.
type SessionPid struct {
	SessionID string
	Pid       int
}

// SessionPids answers the live sessions' pids.
type SessionPids interface {
	LivePids(ctx context.Context) ([]SessionPid, error)
}

// ProcTable reads the machine's process table.
type ProcTable func(ctx context.Context) ([]adapters.ProcRow, error)

// ProcStats is one session's latest sample. CPUPct is the share of one core its tree used since
// the sample before; the first sample of a pid has none.
type ProcStats struct {
	CPUPct *float64
	RSSMB  float64
}

// procSample is a stored sample with what the next one needs to work out the CPU share.
type procSample struct {
	ProcStats
	pid        int
	at         time.Time
	cpuSeconds float64
}

// ProcSampler keeps each session's latest sample for the UTC day.
type ProcSampler struct {
	pids  SessionPids
	table ProcTable
	now   func() time.Time

	mu     sync.Mutex
	day    time.Time
	latest map[string]procSample
}

// NewProcSampler builds a sampler that has sampled nothing yet.
func NewProcSampler(pids SessionPids, table ProcTable, now func() time.Time) *ProcSampler {
	return &ProcSampler{pids: pids, table: table, now: now, latest: map[string]procSample{}}
}

// Run samples every ProcSampleEvery until ctx ends.
func (p *ProcSampler) Run(ctx context.Context) {
	ticker := time.NewTicker(ProcSampleEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.Sample(ctx)
		}
	}
}

// Sample takes one pass. A failed read drops the pass: the samples already held stay.
func (p *ProcSampler) Sample(ctx context.Context) {
	now := p.now()
	p.mu.Lock()
	if day := dayclock.Midnight(now); !day.Equal(p.day) {
		p.day, p.latest = day, map[string]procSample{}
	}
	p.mu.Unlock()

	live, err := p.pids.LivePids(ctx)
	if err != nil || len(live) == 0 {
		return
	}
	rows, err := p.table(ctx)
	if err != nil {
		return
	}
	byPid := make(map[int]adapters.ProcRow, len(rows))
	children := map[int][]int{}
	for _, row := range rows {
		byPid[row.Pid] = row
		children[row.Ppid] = append(children[row.Ppid], row.Pid)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	for _, session := range live {
		root, ok := byPid[session.Pid]
		// the same rule the registry ends a session by: a pid that no longer runs claude is
		// some other program's now, and its tree is not the session's
		if !ok || !strings.Contains(root.Command, "claude") {
			continue
		}
		rss, cpu := treeUsage(session.Pid, byPid, children)
		next := procSample{pid: session.Pid, at: now, cpuSeconds: cpu}
		next.RSSMB = roundTenth(float64(rss) / (1024 * 1024))
		if prev, ok := p.latest[session.SessionID]; ok && prev.pid == session.Pid {
			if elapsed := now.Sub(prev.at).Seconds(); elapsed > 0 {
				// a child that exited takes its CPU time with it, so the sum can fall
				share := roundTenth(max(0, cpu-prev.cpuSeconds) / elapsed * 100)
				next.CPUPct = &share
			}
		}
		p.latest[session.SessionID] = next
	}
}

// Stats answers a session's latest sample today.
func (p *ProcSampler) Stats(sessionID string) (ProcStats, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	sample, ok := p.latest[sessionID]
	return sample.ProcStats, ok
}

// withProcStats sets each top-level session's latest sample on its row.
func (p *ProcSampler) withProcStats(items []store.SessionTreeItem) []store.SessionTreeItem {
	for i := range items {
		stats, ok := p.Stats(items[i].ID)
		if !ok {
			continue
		}
		rss := stats.RSSMB
		items[i].CPUPct, items[i].RSSMB = stats.CPUPct, &rss
	}
	return items
}

// treeUsage sums the resident memory and CPU time of root and every process under it.
func treeUsage(root int, byPid map[int]adapters.ProcRow, children map[int][]int) (int64, float64) {
	var rss int64
	var cpu float64
	seen := map[int]bool{}
	queue := []int{root}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if seen[pid] {
			continue
		}
		seen[pid] = true
		rss += byPid[pid].RSSBytes
		cpu += byPid[pid].CPUSeconds
		queue = append(queue, children[pid]...)
	}
	return rss, cpu
}

func roundTenth(v float64) float64 { return math.Round(v*10) / 10 }
