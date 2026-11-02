package hub

// The shell_tails table: the last lines of an ended shell, kept once, and pruned with age.

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
)

func TestAShellTailIsKeptOncePerShell(t *testing.T) {
	ctx := context.Background()
	db := openForTest(t)
	at := jsscalar.ISOTime(time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC))

	if _, ok, err := db.ShellTail(ctx, "s-1", "main", "b1"); err != nil || ok {
		t.Fatalf("ShellTail before any save = (%v, %v), want none", ok, err)
	}
	if err := db.SaveShellTail(ctx, "s-1", "main", "b1", "one\ntwo\n", at); err != nil {
		t.Fatalf("save: %v", err)
	}
	text, ok, err := db.ShellTail(ctx, "s-1", "main", "b1")
	if err != nil || !ok || text != "one\ntwo\n" {
		t.Fatalf("ShellTail = (%q, %v, %v), want the saved tail", text, ok, err)
	}
	// a second save leaves the first: a shell's tail is taken once
	if err := db.SaveShellTail(ctx, "s-1", "main", "b1", "other\n", at); err != nil {
		t.Fatalf("second save: %v", err)
	}
	if text, _, _ = db.ShellTail(ctx, "s-1", "main", "b1"); text != "one\ntwo\n" {
		t.Errorf("ShellTail after a second save = %q, want the first kept", text)
	}
	// the key is session, agent and shell
	for _, key := range [][3]string{{"s-2", "main", "b1"}, {"s-1", "a1", "b1"}, {"s-1", "main", "b2"}} {
		if _, ok, _ = db.ShellTail(ctx, key[0], key[1], key[2]); ok {
			t.Errorf("ShellTail%v answers a tail nobody saved", key)
		}
	}
	if err := db.SaveShellTail(ctx, "s-1", "a1", "b1", "agent\n", at); err != nil {
		t.Fatal(err)
	}
	ids, err := db.ShellTailIDs(ctx, "s-1", "main")
	if err != nil || len(ids) != 1 || !ids["b1"] {
		t.Errorf("ShellTailIDs = (%v, %v), want only the main agent's b1", ids, err)
	}
}

func TestPruneEventsDropsShellTailsOlderThanNinetyDays(t *testing.T) {
	ctx := context.Background()
	db := openForTest(t)
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	for shell, age := range map[string]time.Duration{
		"old":   ShellTailTTL + time.Hour,
		"edge":  ShellTailTTL,
		"young": 24 * time.Hour,
	} {
		if err := db.SaveShellTail(ctx, "s-1", "main", shell, "x\n", jsscalar.ISOTime(now.Add(-age))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.PruneEvents(ctx, now); err != nil {
		t.Fatalf("prune: %v", err)
	}
	ids, err := db.ShellTailIDs(ctx, "s-1", "main")
	if err != nil || len(ids) != 2 || !ids["edge"] || !ids["young"] {
		t.Errorf("tails after the prune = (%v, %v), want edge and young", ids, err)
	}
	if ShellTailTTL != 90*24*time.Hour {
		t.Errorf("ShellTailTTL = %v (%s), want 90 days", ShellTailTTL, strconv.Itoa(int(ShellTailTTL/day)))
	}
}
