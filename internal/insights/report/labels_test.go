package report

import "testing"

func TestLabelsNumberRepoPathsInFirstUseOrder(t *testing.T) {
	l := NewLabels()
	if got := l.Repo("/work/a"); got != "repo-1" {
		t.Fatalf("first repo = %q, want repo-1", got)
	}
	if got := l.Repo("/work/b"); got != "repo-2" {
		t.Fatalf("second repo = %q, want repo-2", got)
	}
	if got := l.Repo("/work/a"); got != "repo-1" {
		t.Fatalf("same repo again = %q, want repo-1", got)
	}
}

func TestLabelsCleanThePathBeforeNumberingIt(t *testing.T) {
	l := NewLabels()
	first := l.Repo("/work/a")
	if got := l.Repo("/work/a/"); got != first {
		t.Fatalf("trailing slash = %q, want %q", got, first)
	}
	if got := l.Repo("/work/x/../a"); got != first {
		t.Fatalf("dot-dot path = %q, want %q", got, first)
	}
}

func TestLabelsLeaveAnUnknownRepoUnlabelled(t *testing.T) {
	l := NewLabels()
	if got := l.Repo(""); got != "" {
		t.Fatalf("empty path = %q, want empty", got)
	}
	if n := len(l.Repos()); n != 0 {
		t.Fatalf("an empty path made %d entries, want 0", n)
	}
}

func TestLabelsListTheMapInLabelOrder(t *testing.T) {
	l := NewLabels()
	l.Repo("/work/b")
	l.Repo("/work/a")
	l.Session("s9")
	want := []LabelEntry{{Label: "repo-1", Value: "/work/b"}, {Label: "repo-2", Value: "/work/a"}}
	got := l.Repos()
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Repos() = %v, want %v", got, want)
	}
	sessions := l.Sessions()
	if len(sessions) != 1 || sessions[0] != (LabelEntry{Label: "session-1", Value: "s9"}) {
		t.Fatalf("Sessions() = %v", sessions)
	}
}
