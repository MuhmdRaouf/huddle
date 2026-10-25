package server

import (
	"context"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

// The process sampler: every 10 s, one process table for every live session the hub names,
// each session's CPU and memory summed over its pid and every process under it.

// fakePids answers a fixed set of live sessions and counts the asks.
type fakePids struct {
	pids []SessionPid
	asks int
}

func (f *fakePids) LivePids(context.Context) ([]SessionPid, error) {
	f.asks++
	return f.pids, nil
}

// fakeTable answers the rows it holds and counts the reads.
type fakeTable struct {
	rows  []adapters.ProcRow
	reads int
}

func (f *fakeTable) read(context.Context) ([]adapters.ProcRow, error) {
	f.reads++
	return f.rows, nil
}

const mb = 1024 * 1024

func TestProcSamplerSumsEachSessionsTree(t *testing.T) {
	now := time.Date(2026, time.October, 25, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	pids := &fakePids{pids: []SessionPid{{SessionID: "s1", Pid: 100}, {SessionID: "s-reused", Pid: 300}}}
	table := &fakeTable{rows: []adapters.ProcRow{
		{Pid: 100, Ppid: 1, RSSBytes: 100 * mb, CPUSeconds: 10, Command: "claude --resume"},
		{Pid: 101, Ppid: 100, RSSBytes: 20 * mb, CPUSeconds: 2, Command: "node mcp.js"},
		{Pid: 102, Ppid: 101, RSSBytes: 5 * mb, CPUSeconds: 1, Command: "sleep 30"},
		{Pid: 200, Ppid: 1, RSSBytes: 999 * mb, CPUSeconds: 50, Command: "claude"},
		// the pid the registry names now runs something else: never sampled
		{Pid: 300, Ppid: 1, RSSBytes: 50 * mb, CPUSeconds: 3, Command: "postgres"},
	}}
	sampler := NewProcSampler(pids, table.read, clock)

	sampler.Sample(context.Background())
	first, ok := sampler.Stats("s1")
	if !ok || first.RSSMB != 125 || first.CPUPct != nil {
		t.Fatalf("the first sample = %+v (%v), want 125 MB and no CPU yet", first, ok)
	}
	if _, ok := sampler.Stats("s-reused"); ok {
		t.Errorf("a pid no longer running claude was sampled")
	}

	// 10 s later the tree used 1.5 s more CPU: 15% of one core
	now = now.Add(10 * time.Second)
	table.rows[0].CPUSeconds = 11
	table.rows[2].CPUSeconds = 1.5
	sampler.Sample(context.Background())
	second, _ := sampler.Stats("s1")
	if second.CPUPct == nil || *second.CPUPct != 15 {
		t.Fatalf("the second sample's CPU = %v, want 15", second.CPUPct)
	}

	// the session ends: its last sample stays for the day, and the next day drops it
	pids.pids = nil
	reads := table.reads
	sampler.Sample(context.Background())
	if table.reads != reads {
		t.Errorf("the table was read with no session live")
	}
	if kept, ok := sampler.Stats("s1"); !ok || kept.RSSMB != 125 {
		t.Errorf("the ended session's sample = %+v (%v), want it kept for the day", kept, ok)
	}
	now = now.Add(12 * time.Hour)
	sampler.Sample(context.Background())
	if _, ok := sampler.Stats("s1"); ok {
		t.Errorf("yesterday's sample is still there")
	}
}

func TestSessionsRowsCarryCPUAndMemory(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	st := store.CreateStore(clock)
	cwd := "/w/app"
	for _, id := range []string{"s1", "s2"} {
		st.AddSpoolLine(shared.SpoolLine{Ts: isoOf(now.UnixMilli()), Event: "SessionStart", SessionID: strPtr(id), Cwd: &cwd})
	}
	table := &fakeTable{rows: []adapters.ProcRow{
		{Pid: 100, Ppid: 1, RSSBytes: 64 * mb, CPUSeconds: 4, Command: "claude"},
	}}
	sampler := NewProcSampler(&fakePids{pids: []SessionPid{{SessionID: "s1", Pid: 100}}}, table.read, clock)
	sampler.Sample(context.Background())
	now = now.Add(10 * time.Second)
	table.rows[0].CPUSeconds = 5
	sampler.Sample(context.Background())

	_, port := serve(t, HttpOptions{Store: st, Version: "test", Procs: sampler})
	var answer struct {
		Sessions []struct {
			ID     string   `json:"id"`
			CPUPct *float64 `json:"cpuPct"`
			RSSMB  *float64 `json:"rssMB"`
		} `json:"sessions"`
	}
	decode(t, fetch(t, port, "GET", "/api/sessions", "", nil).body, &answer)
	byID := map[string]int{}
	for i, row := range answer.Sessions {
		byID[row.ID] = i
	}
	s1 := answer.Sessions[byID["s1"]]
	if s1.CPUPct == nil || *s1.CPUPct != 10 || s1.RSSMB == nil || *s1.RSSMB != 64 {
		t.Errorf("s1 = cpu %v rss %v, want 10%% and 64 MB", s1.CPUPct, s1.RSSMB)
	}
	s2 := answer.Sessions[byID["s2"]]
	if s2.CPUPct != nil || s2.RSSMB != nil {
		t.Errorf("s2 = cpu %v rss %v, want neither: no pid was sampled", s2.CPUPct, s2.RSSMB)
	}
}
