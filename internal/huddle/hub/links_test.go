package hub

// The links table: one row per session per channel with the level it hears the channel
// at, written and read through the link methods alone.

import (
	"context"
	"testing"
)

func TestLinkSessionsStoresOneRowPerSessionAtItsLevel(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	if err := db.LinkSessions(ctx, "kanban-release", []string{"s-1", "s-2"}, LevelOverhear, "2026-10-16T09:00:00.000Z"); err != nil {
		t.Fatalf("link: %v", err)
	}
	rows, err := db.st.All(ctx, "SELECT channel, session_id, level, linked_at FROM links ORDER BY session_id")
	if err != nil {
		t.Fatalf("read links: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("links = %d rows, want 2", len(rows))
	}
	if rows[0]["session_id"] != "s-1" || rows[1]["session_id"] != "s-2" {
		t.Errorf("sessions = %v/%v, want s-1/s-2", rows[0]["session_id"], rows[1]["session_id"])
	}
	for i, row := range rows {
		if row["channel"] != "kanban-release" || row["level"] != LevelOverhear {
			t.Errorf("row %d = %v/%v, want kanban-release/%s", i, row["channel"], row["level"], LevelOverhear)
		}
		if row["linked_at"] != "2026-10-16T09:00:00.000Z" {
			t.Errorf("row %d linked_at = %v, want the link's own time", i, row["linked_at"])
		}
	}
}

func TestLinkingAgainChangesOnlyTheLevel(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	if err := db.LinkSessions(ctx, "kanban-release", []string{"s-1"}, LevelOverhear, "2026-10-16T09:00:00.000Z"); err != nil {
		t.Fatalf("link: %v", err)
	}
	// a second link of the same session moves the level and the time, and never grows a row
	if err := db.LinkSessions(ctx, "kanban-release", []string{"s-1"}, LevelDigest, "2026-10-16T09:01:00.000Z"); err != nil {
		t.Fatalf("relink: %v", err)
	}
	rows, err := db.st.All(ctx, "SELECT level, linked_at FROM links WHERE channel = 'kanban-release'")
	if err != nil {
		t.Fatalf("read links: %v", err)
	}
	if len(rows) != 1 || rows[0]["level"] != LevelDigest || rows[0]["linked_at"] != "2026-10-16T09:01:00.000Z" {
		t.Fatalf("links after the relink = %v, want one digest row at 09:01", rows)
	}
}

func TestSetLinkLevelAndUnlinkAnswerFalseForNoLink(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	// nothing linked yet: both answer false and change nothing
	moved, err := db.SetLinkLevel(ctx, "kanban-release", "s-1", LevelOnRequest)
	if err != nil || moved {
		t.Fatalf("a level move with no link = (%v, %v), want false", moved, err)
	}
	gone, err := db.Unlink(ctx, "kanban-release", "s-1")
	if err != nil || gone {
		t.Fatalf("an unlink with no link = (%v, %v), want false", gone, err)
	}
	if err := db.LinkSessions(ctx, "kanban-release", []string{"s-1"}, LevelDigest, "2026-10-16T09:00:00.000Z"); err != nil {
		t.Fatalf("link: %v", err)
	}
	if moved, err = db.SetLinkLevel(ctx, "kanban-release", "s-1", LevelOnRequest); err != nil || !moved {
		t.Fatalf("the level move = (%v, %v), want true", moved, err)
	}
	if row, err := db.st.Get(ctx, "SELECT level FROM links WHERE channel = 'kanban-release' AND session_id = 's-1'"); err != nil || row == nil || row["level"] != LevelOnRequest {
		t.Fatalf("the row after the move = %v (%v), want %s", row, err, LevelOnRequest)
	}
	if gone, err = db.Unlink(ctx, "kanban-release", "s-1"); err != nil || !gone {
		t.Fatalf("the unlink = (%v, %v), want true", gone, err)
	}
	if row, err := db.st.Get(ctx, "SELECT session_id FROM links WHERE channel = 'kanban-release'"); err != nil || row != nil {
		t.Fatalf("the row after the unlink = %v (%v), want none", row, err)
	}
}

func TestLinksOfListsOneChannelOnly(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	if err := db.LinkSessions(ctx, "kanban-release", []string{"s-2", "s-1"}, LevelOverhear, "2026-10-16T09:00:00.000Z"); err != nil {
		t.Fatalf("link: %v", err)
	}
	if err := db.LinkSessions(ctx, "other", []string{"s-3"}, LevelDigest, "2026-10-16T09:00:01.000Z"); err != nil {
		t.Fatalf("link: %v", err)
	}
	links, err := db.LinksOf(ctx, "kanban-release")
	if err != nil {
		t.Fatalf("links of: %v", err)
	}
	want := []Link{
		{Channel: "kanban-release", SessionID: "s-1", Level: LevelOverhear, LinkedAt: "2026-10-16T09:00:00.000Z"},
		{Channel: "kanban-release", SessionID: "s-2", Level: LevelOverhear, LinkedAt: "2026-10-16T09:00:00.000Z"},
	}
	if len(links) != len(want) {
		t.Fatalf("links = %+v, want %d of the one channel", links, len(want))
	}
	for i, row := range want {
		if links[i] != row {
			t.Errorf("link %d = %+v, want %+v", i, links[i], row)
		}
	}
	if left, err := db.LinksOf(ctx, "empty"); err != nil || len(left) != 0 {
		t.Errorf("links of an unlinked channel = %+v (%v), want none", left, err)
	}
}

func TestTheLinkRoutesNameAFailedStatement(t *testing.T) {
	ctx := context.Background()
	db := dbWithout(t, "links")
	if err := db.LinkSessions(ctx, "kanban-release", []string{"s-1"}, LevelOverhear, "2026-10-16T09:00:00.000Z"); err == nil {
		t.Fatal("a link write to a broken links table succeeded")
	}
	if _, err := db.SetLinkLevel(ctx, "kanban-release", "s-1", LevelDigest); err == nil {
		t.Fatal("a level move on a broken links table succeeded")
	}
	if _, err := db.Unlink(ctx, "kanban-release", "s-1"); err == nil {
		t.Fatal("an unlink on a broken links table succeeded")
	}
	if _, err := db.LinksOf(ctx, "kanban-release"); err == nil {
		t.Fatal("a links read from a broken links table succeeded")
	}
}

func TestAllLinksJoinsTheNewestDigestTime(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	if err := db.LinkSessions(ctx, "kanban-release", []string{"s-2", "s-1"}, LevelDigest, "2026-10-16T09:00:00.000Z"); err != nil {
		t.Fatalf("link: %v", err)
	}
	if err := db.LinkSessions(ctx, "other", []string{"s-3"}, LevelOverhear, "2026-10-16T09:01:00.000Z"); err != nil {
		t.Fatalf("link: %v", err)
	}
	// s-1 and s-3 have digested; s-2 has not, so its row carries no digest time
	for _, save := range []struct{ session, at string }{
		{"s-1", "2026-10-16T09:05:00.000Z"},
		{"s-3", "2026-10-16T09:06:00.000Z"},
	} {
		if err := db.SaveDigest(ctx, save.session, "the digest", save.at); err != nil {
			t.Fatalf("digest for %s: %v", save.session, err)
		}
	}
	rows, err := db.AllLinks(ctx)
	if err != nil {
		t.Fatalf("all links: %v", err)
	}
	want := []LinkRow{
		{
			Link:     Link{Channel: "kanban-release", SessionID: "s-1", Level: LevelDigest, LinkedAt: "2026-10-16T09:00:00.000Z"},
			DigestAt: "2026-10-16T09:05:00.000Z",
		},
		{Link: Link{Channel: "kanban-release", SessionID: "s-2", Level: LevelDigest, LinkedAt: "2026-10-16T09:00:00.000Z"}},
		{
			Link:     Link{Channel: "other", SessionID: "s-3", Level: LevelOverhear, LinkedAt: "2026-10-16T09:01:00.000Z"},
			DigestAt: "2026-10-16T09:06:00.000Z",
		},
	}
	if len(rows) != len(want) {
		t.Fatalf("all links = %+v, want %d rows", rows, len(want))
	}
	for i, row := range want {
		if rows[i] != row {
			t.Errorf("row %d = %+v, want %+v", i, rows[i], row)
		}
	}
	// the newest digest replaces an older one: the join reads the one stored row
	if err := db.SaveDigest(ctx, "s-1", "newer", "2026-10-16T09:09:00.000Z"); err != nil {
		t.Fatalf("a newer digest: %v", err)
	}
	rows, err = db.AllLinks(ctx)
	if err != nil {
		t.Fatalf("all links again: %v", err)
	}
	if rows[0].DigestAt != "2026-10-16T09:09:00.000Z" {
		t.Errorf("the digest time after the newer save = %q, want the newest", rows[0].DigestAt)
	}
	if left, err := db.AllLinks(ctx); err != nil || len(left) != 3 {
		t.Errorf("all links = %d rows (%v), want the same 3", len(left), err)
	}
}

func TestLinksOfSessionListsEveryChannel(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	at := "2026-10-16T09:00:00.000Z"
	if err := db.LinkSessions(ctx, "kanban-release", []string{"s-1", "s-2"}, LevelDigest, at); err != nil {
		t.Fatalf("link: %v", err)
	}
	if err := db.LinkSessions(ctx, "billing", []string{"s-1"}, LevelOverhear, at); err != nil {
		t.Fatalf("link: %v", err)
	}
	links, err := db.LinksOfSession(ctx, "s-1")
	if err != nil {
		t.Fatalf("links of s-1: %v", err)
	}
	if len(links) != 2 || links[0].Channel != "billing" || links[0].Level != LevelOverhear ||
		links[1].Channel != "kanban-release" || links[1].Level != LevelDigest {
		t.Fatalf("links of s-1 = %v, want billing at overhear then kanban-release at digest", links)
	}
	if links, err = db.LinksOfSession(ctx, "s-3"); err != nil || len(links) != 0 {
		t.Errorf("links of an unlinked session = (%v, %v), want none", links, err)
	}
}
