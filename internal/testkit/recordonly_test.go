package testkit

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRecordOnlyOn(t *testing.T) {
	for _, tt := range []struct {
		name    string
		actions string
		goos    string
		want    bool
	}{
		{name: "GitHub Actions on macOS", actions: "true", goos: "darwin", want: true},
		{name: "GitHub Actions on Linux", actions: "true", goos: "linux", want: false},
		{name: "a laptop on macOS", actions: "", goos: "darwin", want: false},
		{name: "a laptop on Linux", actions: "", goos: "linux", want: false},
		{name: "another CI on macOS", actions: "false", goos: "darwin", want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GITHUB_ACTIONS", tt.actions)
			if got := RecordOnlyOn(tt.goos); got != tt.want {
				t.Errorf("RecordOnlyOn(%q) with GITHUB_ACTIONS=%q = %t, want %t", tt.goos, tt.actions, got, tt.want)
			}
		})
	}
}

// TestRecordOnlyEndsWithTheBaseline fails once HV-PERF-GUARD-BASELINE adds its same-job baseline
// files, so the macOS CI record-only branch cannot outlive the baseline that replaces it.
func TestRecordOnlyEndsWithTheBaseline(t *testing.T) {
	_, here, _, _ := runtime.Caller(0)
	repo := filepath.Join(filepath.Dir(here), "..", "..")
	for _, baseline := range []string{
		"cmd/huddle/router_overhead_baseline_test.go",
		"internal/huddle/server/idle_cpu_baseline_test.go",
	} {
		if _, err := os.Stat(filepath.Join(repo, baseline)); err == nil {
			t.Errorf("%s exists: remove testkit.RecordOnly, the record-only branches of TestHubIdleCPU and TestE2ERouterOverhead, and this test", baseline)
		}
	}
}
