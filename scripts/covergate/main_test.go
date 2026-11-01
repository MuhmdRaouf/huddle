package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

const mp = "github.com/MuhmdRaouf/huddle"

const sampleProfile = "mode: atomic\n" +
	mp + "/internal/domain/brief/brief.go:10.20,12.3 2 1\n" +
	mp + "/internal/domain/brief/brief.go:15.20,17.3 3 0\n" +
	mp + "/internal/domain/job/job.go:1.1,2.2 4 1\n" +
	mp + "/internal/adapters/fsfiles/fsfiles.go:5.10,6.20 4 1\n" +
	mp + "/internal/testkit/tk.go:1.1,2.2 5 0\n" +
	mp + "/cmd/huddle/main.go:1.1,2.2 5 1\n" +
	mp + "/scripts/covergate/main.go:1.1,2.2 5 0\n"

func sampleCoverage(t *testing.T) map[string]coverage {
	t.Helper()
	blocks, err := parseProfile(strings.NewReader(sampleProfile))
	if err != nil {
		t.Fatal(err)
	}
	return aggregate(blocks)
}

func TestParseProfile(t *testing.T) {
	blocks, err := parseProfile(strings.NewReader(sampleProfile))
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 7 {
		t.Fatalf("parsed %d blocks, want 7", len(blocks))
	}
	first := blocks[0]
	if first.File != mp+"/internal/domain/brief/brief.go" || first.Statements != 2 || first.Count != 1 {
		t.Errorf("first block = %+v, want {%s 2 1}", first, mp+"/internal/domain/brief/brief.go")
	}
	uncovered := blocks[1]
	if uncovered.File != mp+"/internal/domain/brief/brief.go" || uncovered.Statements != 3 || uncovered.Count != 0 {
		t.Errorf("second block = %+v, want {%s 3 0}", uncovered, mp+"/internal/domain/brief/brief.go")
	}
}

func TestParseProfileRejectsBadInput(t *testing.T) {
	tests := []struct {
		name    string
		profile string
	}{
		{"missing mode header", mp + "/internal/radar/shared/repo.go:1.1,2.2 1 1\n"},
		{"wrong mode header", "mode: set"},
		{"truncated line", "mode: atomic\n" + mp + "/internal/radar/shared/repo.go:1.1,2.2 1\n"},
		{"non-numeric statements", "mode: atomic\n" + mp + "/internal/radar/shared/repo.go:1.1,2.2 x 1\n"},
		{"non-numeric count", "mode: atomic\n" + mp + "/internal/radar/shared/repo.go:1.1,2.2 1 y\n"},
		{"no position", "mode: atomic\n" + mp + "/internal/radar/shared/repo.go 1 1\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseProfile(strings.NewReader(tt.profile)); err == nil {
				t.Fatal("parseProfile succeeded, want an error")
			}
		})
	}
}

func TestParseProfileSkipsBlankLines(t *testing.T) {
	profile := "mode: atomic\n\n" + mp + "/internal/radar/shared/repo.go:1.1,2.2 1 1\n\n"
	blocks, err := parseProfile(strings.NewReader(profile))
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 {
		t.Fatalf("parsed %d blocks, want 1", len(blocks))
	}
}

func TestAggregate(t *testing.T) {
	cov := sampleCoverage(t)
	want := map[string]coverage{
		mp + "/internal/domain/brief":     {Statements: 5, Covered: 2},
		mp + "/internal/domain/job":       {Statements: 4, Covered: 4},
		mp + "/internal/adapters/fsfiles": {Statements: 4, Covered: 4},
		mp + "/internal/testkit":          {Statements: 5, Covered: 0},
		mp + "/cmd/huddle":                {Statements: 5, Covered: 5},
		mp + "/scripts/covergate":         {Statements: 5, Covered: 0},
	}
	if len(cov) != len(want) {
		t.Fatalf("aggregated %d packages, want %d", len(cov), len(want))
	}
	for pkg, c := range want {
		if got := cov[pkg]; got != c {
			t.Errorf("%s = %+v, want %+v", pkg, got, c)
		}
	}
}

func TestPercent(t *testing.T) {
	tests := []struct {
		c    coverage
		want float64
	}{
		{coverage{Statements: 2, Covered: 1}, 50},
		{coverage{Statements: 20, Covered: 19}, 95},
		{coverage{Statements: 5, Covered: 0}, 0},
		{coverage{}, 100},
	}
	for _, tt := range tests {
		if got := percent(tt.c); got != tt.want {
			t.Errorf("percent(%+v) = %v, want %v", tt.c, got, tt.want)
		}
	}
}

func TestThreshold(t *testing.T) {
	tests := []struct {
		pkg       string
		wantMin   float64
		wantGated bool
	}{
		{mp + "/internal/domain/brief", 95, true},
		{mp + "/internal/domain/job/area", 95, true},
		{mp + "/internal/adapters/fsfiles", 90, true},
		{mp + "/internal/adapters/keystore", 90, true},
		{mp + "/internal/adapters/process", 90, true},
		{mp + "/internal/ports", 90, true},
		{mp + "/internal/jsfmt", 90, true},
		{mp + "/internal/jsscalar", 90, true},
		{mp + "/internal/radar/shared", 90, true},
		{mp + "/internal/radar/store", 90, true},
		{mp + "/internal/radar/history", 90, true},
		{mp + "/internal/huddle/server", 90, true},
		{mp + "/internal/huddle/hooks", 90, true},
		{mp + "/internal/huddle/store", 90, true},
		{mp + "/internal/kanban/board", 90, true},
		{mp + "/internal/kanban/ticket", 90, true},
		{mp + "/internal/adapters/spool", 90, true},
		{mp + "/internal/adapters/fsutil", 90, true},
		{mp + "/internal/core/app", 90, true},
		{mp + "/internal/core/cli", 90, true},
		{mp + "/internal/core/archtest", 0, false},
		{mp + "/internal/core/testkit/fakes", 0, false},
		{mp + "/internal/core/entry", 0, false},
		{mp + "/internal/testkit", 0, false},
		{mp + "/internal/testkit/cmd/fakeclaude", 0, false},
		{mp + "/cmd/huddle", 0, false},
		{mp + "/cmd/radar", 0, false},
		{mp + "/cmd/kanban", 0, false},
		{mp + "/scripts/covergate", 0, false},
		{mp, 0, false},
		{"example.com/other/pkg", 0, false},
	}
	for _, tt := range tests {
		min, gated := threshold(tt.pkg)
		if min != tt.wantMin || gated != tt.wantGated {
			t.Errorf("threshold(%s) = (%v, %v), want (%v, %v)", tt.pkg, min, gated, tt.wantMin, tt.wantGated)
		}
	}
}

func TestGateLine(t *testing.T) {
	cov := sampleCoverage(t)
	tests := []struct {
		pkg    string
		want   string
		wantOK bool
	}{
		{mp + "/internal/domain/brief", "FAIL " + mp + "/internal/domain/brief 40.0% (min 95%)", false},
		{mp + "/internal/domain/job", "ok   " + mp + "/internal/domain/job 100.0% (min 95%)", true},
		{mp + "/internal/adapters/fsfiles", "ok   " + mp + "/internal/adapters/fsfiles 100.0% (min 90%)", true},
		{mp + "/internal/testkit", "", false},
		{mp + "/cmd/huddle", "", false},
		{mp + "/scripts/covergate", "", false},
	}
	for _, tt := range tests {
		got, ok := gateLine(tt.pkg, cov[tt.pkg])
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("gateLine(%s) = (%q, %v), want (%q, %v)", tt.pkg, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestGateLineBoundary(t *testing.T) {
	atMin := coverage{Statements: 20, Covered: 19}
	if _, ok := gateLine(mp+"/internal/domain/job", atMin); !ok {
		t.Error("exactly 95.0% must pass the 95% gate")
	}
	justUnder := coverage{Statements: 1000, Covered: 949}
	if _, ok := gateLine(mp+"/internal/domain/job", justUnder); ok {
		t.Error("94.9% must fail the 95% gate")
	}
	at90 := coverage{Statements: 10, Covered: 9}
	if _, ok := gateLine(mp+"/internal/adapters/fsfiles", at90); !ok {
		t.Error("exactly 90.0% must pass the 90% gate")
	}
}

// writeProfile puts a coverprofile named coverage.out in a fresh dir and returns the dir.
func writeProfile(t *testing.T, profile string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "coverage.out"), []byte(profile), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// listing is a packageLister that always returns pkgs.
func listing(pkgs ...string) packageLister {
	return func(string) ([]string, error) {
		return pkgs, nil
	}
}

// samplePackages lists every package sampleProfile has blocks for.
func samplePackages() []string {
	return []string{
		mp + "/internal/domain/brief",
		mp + "/internal/domain/job",
		mp + "/internal/adapters/fsfiles",
		mp + "/internal/testkit",
		mp + "/cmd/huddle",
		mp + "/scripts/covergate",
	}
}

// noGoOnPath puts a fake go first on PATH that prints a marker and fails: any
// go invocation must show up as that marker on stderr.
func noGoOnPath(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	name, content := filepath.Join(dir, "go"), "#!/bin/sh\necho covergate ran go test >&2\nexit 1\n"
	if err := os.WriteFile(name, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestRunJudgesGivenProfile(t *testing.T) {
	dir := writeProfile(t, "mode: atomic\n"+mp+"/internal/domain/job/job.go:1.1,2.2 4 1\n")
	var stdout, stderr bytes.Buffer
	code := judge(dir, "coverage.out", false, listing(mp+"/internal/domain/job"), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("judge exited %d, want 0; stderr:\n%s", code, stderr.String())
	}
	want := "ok   " + mp + "/internal/domain/job 100.0% (min 95%)\n"
	if stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunGivenProfileFailsBelowMinimum(t *testing.T) {
	// sampleProfile has internal/domain/brief at 40%, under its 95% minimum.
	dir := writeProfile(t, sampleProfile)
	var stdout, stderr bytes.Buffer
	code := judge(dir, "coverage.out", false, listing(samplePackages()...), &stdout, &stderr)
	if code != 1 {
		t.Fatalf("judge exited %d, want 1; stderr:\n%s", code, stderr.String())
	}
	want := "FAIL " + mp + "/internal/domain/brief 40.0% (min 95%)\n"
	if !strings.Contains(stdout.String(), want) {
		t.Errorf("stdout = %q, want it to contain %q", stdout.String(), want)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunGivenProfileFailsWhenGatedPackageMissing(t *testing.T) {
	// The lister knows internal/domain/brief; the profile has no block for it.
	dir := writeProfile(t, "mode: atomic\n"+mp+"/internal/domain/job/job.go:1.1,2.2 4 1\n")
	var stdout, stderr bytes.Buffer
	code := judge(dir, "coverage.out", false, listing(mp+"/internal/domain/job", mp+"/internal/domain/brief"), &stdout, &stderr)
	if code != 1 {
		t.Fatalf("judge exited %d, want 1; stderr:\n%s", code, stderr.String())
	}
	want := "FAIL " + mp + "/internal/domain/brief missing from the profile (min 95%)\n"
	if !strings.Contains(stdout.String(), want) {
		t.Errorf("stdout = %q, want it to contain %q", stdout.String(), want)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunGivenProfileWithEveryPackagePasses(t *testing.T) {
	profile := "mode: atomic\n" +
		mp + "/internal/domain/job/job.go:1.1,2.2 4 1\n" +
		mp + "/internal/adapters/fsfiles/fsfiles.go:5.10,6.20 4 1\n"
	dir := writeProfile(t, profile)
	var stdout, stderr bytes.Buffer
	pkgs := listing(mp+"/internal/domain/job", mp+"/internal/adapters/fsfiles", mp+"/scripts/build")
	code := judge(dir, "coverage.out", false, pkgs, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("judge exited %d, want 0; stderr:\n%s", code, stderr.String())
	}
	want := "ok   " + mp + "/internal/adapters/fsfiles 100.0% (min 90%)\n" +
		"ok   " + mp + "/internal/domain/job 100.0% (min 95%)\n"
	if stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunGivenProfileAllowsExemptPackagesMissing(t *testing.T) {
	dir := writeProfile(t, "mode: atomic\n"+mp+"/internal/domain/job/job.go:1.1,2.2 4 1\n")
	lists := listing(
		mp+"/internal/domain/job",
		mp+"/internal/testkit", mp+"/internal/testkit/cmd/fakeclaude",
		mp+"/cmd/huddle", mp+"/cmd/radar", mp+"/cmd/kanban",
		mp+"/scripts/build", mp+"/scripts/covergate",
	)
	var stdout, stderr bytes.Buffer
	if code := judge(dir, "coverage.out", false, lists, &stdout, &stderr); code != 0 {
		t.Fatalf("judge exited %d, want 0; stderr:\n%s", code, stderr.String())
	}
	want := "ok   " + mp + "/internal/domain/job 100.0% (min 95%)\n"
	if stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunGivenProfileNeverRunsGoTest(t *testing.T) {
	noGoOnPath(t)
	dir := writeProfile(t, "mode: atomic\n"+mp+"/internal/domain/job/job.go:1.1,2.2 4 1\n")
	var stdout, stderr bytes.Buffer
	code := judge(dir, "coverage.out", false, listing(mp+"/internal/domain/job"), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("judge exited %d, want 0; stderr:\n%s", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "covergate ran go test") {
		t.Error("judge started go test although a profile was given")
	}
}

func TestRunMissingProfileFile(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := judge(t.TempDir(), "nope.out", false, listing(), &stdout, &stderr); code != 1 {
		t.Fatalf("judge exited %d, want 1", code)
	}
	msg := stderr.String()
	if !strings.Contains(msg, "covergate:") || !strings.Contains(msg, "nope.out") {
		t.Errorf("stderr = %q, want a covergate error naming nope.out", msg)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
}

func TestRunUnreadableProfileFile(t *testing.T) {
	// "." resolves to the module dir itself, which is a directory, not a readable profile.
	var stdout, stderr bytes.Buffer
	if code := judge(t.TempDir(), ".", false, listing(), &stdout, &stderr); code != 1 {
		t.Fatalf("judge exited %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "covergate:") {
		t.Errorf("stderr = %q, want a covergate error", stderr.String())
	}
}

func TestRunFailsWhenPackageListingFails(t *testing.T) {
	dir := writeProfile(t, "mode: atomic\n"+mp+"/internal/domain/job/job.go:1.1,2.2 4 1\n")
	lister := func(string) ([]string, error) {
		return nil, errors.New("no go tool")
	}
	var stdout, stderr bytes.Buffer
	if code := judge(dir, "coverage.out", false, lister, &stdout, &stderr); code != 1 {
		t.Fatalf("judge exited %d, want 1", code)
	}
	msg := stderr.String()
	if !strings.Contains(msg, "covergate:") || !strings.Contains(msg, "no go tool") {
		t.Errorf("stderr = %q, want a covergate error naming the listing failure", msg)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
}

func TestRunPresentOnlySkipsGatedPackageMissingFromProfile(t *testing.T) {
	// A shard's profile holds only that shard's packages: with -present-only a
	// gated package another shard runs does not fail this one.
	dir := writeProfile(t, "mode: atomic\n"+mp+"/internal/domain/job/job.go:1.1,2.2 4 1\n")
	var stdout, stderr bytes.Buffer
	code := judge(dir, "coverage.out", true, listing(mp+"/internal/domain/job", mp+"/internal/domain/brief"), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("judge exited %d, want 0; stderr:\n%s", code, stderr.String())
	}
	want := "ok   " + mp + "/internal/domain/job 100.0% (min 95%)\n"
	if stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunPresentOnlyStillFailsLowCoverage(t *testing.T) {
	// -present-only skips absent packages, never a present one under its floor:
	// sampleProfile has internal/domain/brief at 40%.
	dir := writeProfile(t, sampleProfile)
	var stdout, stderr bytes.Buffer
	code := judge(dir, "coverage.out", true, listing(samplePackages()...), &stdout, &stderr)
	if code != 1 {
		t.Fatalf("judge exited %d, want 1; stderr:\n%s", code, stderr.String())
	}
	want := "FAIL " + mp + "/internal/domain/brief 40.0% (min 95%)\n"
	if !strings.Contains(stdout.String(), want) {
		t.Errorf("stdout = %q, want it to contain %q", stdout.String(), want)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

// writeNamedProfile writes a coverprofile named name into dir.
func writeNamedProfile(t *testing.T, dir, name, profile string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(profile), 0o644); err != nil {
		t.Fatal(err)
	}
}

const serverPkg = mp + "/internal/huddle/server"

func TestRunMergesTheProfilesOfASplitPackage(t *testing.T) {
	// Each half of a split package runs only some of its tests, so each profile alone is under the
	// 90% floor; the blocks they share merge, a block covered by either half counts as covered.
	dir := t.TempDir()
	writeNamedProfile(t, dir, "a.out", "mode: atomic\n"+
		serverPkg+"/s.go:1.1,2.2 4 1\n"+serverPkg+"/s.go:3.1,4.2 6 0\n")
	writeNamedProfile(t, dir, "b.out", "mode: atomic\n"+
		serverPkg+"/s.go:1.1,2.2 4 0\n"+serverPkg+"/s.go:3.1,4.2 6 3\n")
	var stdout, stderr bytes.Buffer
	if code := judge(dir, "a.out,b.out", false, listing(serverPkg), &stdout, &stderr); code != 0 {
		t.Fatalf("judge exited %d, want 0; stdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	want := "ok   " + serverPkg + " 100.0% (min 90%)\n"
	if stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
	for _, alone := range []string{"a.out", "b.out"} {
		stdout.Reset()
		if code := judge(dir, alone, false, listing(serverPkg), &stdout, &stderr); code != 1 {
			t.Errorf("%s alone exited %d, want 1: one half does not clear the floor", alone, code)
		}
	}
}

func TestRunMergedProfilesStillFailBelowTheFloor(t *testing.T) {
	dir := t.TempDir()
	writeNamedProfile(t, dir, "a.out", "mode: atomic\n"+serverPkg+"/s.go:1.1,2.2 4 1\n"+serverPkg+"/s.go:3.1,4.2 6 0\n")
	writeNamedProfile(t, dir, "b.out", "mode: atomic\n"+serverPkg+"/s.go:1.1,2.2 4 1\n"+serverPkg+"/s.go:3.1,4.2 6 0\n")
	var stdout, stderr bytes.Buffer
	if code := judge(dir, "a.out,b.out", false, listing(serverPkg), &stdout, &stderr); code != 1 {
		t.Fatalf("judge exited %d, want 1; stdout:\n%s", code, stdout.String())
	}
	if want := "FAIL " + serverPkg + " 40.0% (min 90%)\n"; !strings.Contains(stdout.String(), want) {
		t.Errorf("stdout = %q, want it to contain %q", stdout.String(), want)
	}
}

func TestRunFailsWhenOneOfTheMergedProfilesIsMissing(t *testing.T) {
	dir := t.TempDir()
	writeNamedProfile(t, dir, "a.out", "mode: atomic\n"+serverPkg+"/s.go:1.1,2.2 4 1\n")
	var stdout, stderr bytes.Buffer
	if code := judge(dir, "a.out,gone.out", false, listing(serverPkg), &stdout, &stderr); code != 1 {
		t.Fatalf("judge exited %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "gone.out") {
		t.Errorf("stderr = %q, want it to name gone.out", stderr.String())
	}
}

func TestCIGatesTheMergedProfileOfEveryShard(t *testing.T) {
	ci := readCI(t)
	if got := strings.Count(ci, "name: coverage-${{ matrix.shard }}"); got != 1 {
		t.Errorf("ci.yml uploads a coverage profile named after the shard %d times, want 1", got)
	}
	if got := strings.Count(ci, "scripts/covergate -present-only -profile"); got != 1 {
		t.Errorf("ci.yml gates profiles %d times, want 1: the floors are judged once, on every shard's profile merged", got)
	}
}

// ciShards are the test shards ci.yml runs, in matrix order. Each is a list of packages that
// scripts/shard-packages.sh prints.
var ciShards = []string{"core", "server", "server-a", "cmd", "cmd-heavy", "rest"}

// splitPairs maps a split package's shard, which skips the heavy tests, to its sibling, which runs
// only them; both run the same package, told apart by the flags scripts/shard-flags.sh prints.
var splitPairs = map[string]string{"server": "server-a", "cmd": "cmd-heavy"}

// shardScript runs one of the shard selectors the CI jobs run, from the repo root.
func shardScript(t *testing.T, script, shard string) string {
	t.Helper()
	cmd := exec.Command("sh", "scripts/"+script, shard)
	cmd.Dir = "../.."
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s %s: %v: %s", script, shard, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out))
}

// shardPackages returns the import paths a shard tests.
func shardPackages(t *testing.T, shard string) []string {
	t.Helper()
	return strings.Fields(shardScript(t, "shard-packages.sh", shard))
}

func TestEveryModulePackageFallsInExactlyOneShard(t *testing.T) {
	all, err := goListPackages("../..")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) == 0 {
		t.Fatal("go list found no packages; the sweep would guard nothing")
	}
	owners := map[string][]string{}
	for _, shard := range ciShards {
		pkgs := shardPackages(t, shard)
		if len(pkgs) == 0 {
			t.Errorf("shard %s selects no package", shard)
		}
		for _, pkg := range pkgs {
			owners[pkg] = append(owners[pkg], shard)
		}
	}
	for _, pkg := range all {
		shards := owners[pkg]
		switch {
		case len(shards) == 0:
			t.Errorf("%s falls between the shards: no shard runs it and no covergate pass judges it", pkg)
		case len(shards) == 2 && splitPairs[shards[0]] == shards[1]:
		case len(shards) > 1:
			t.Errorf("%s runs in the %s shards, which are not the two halves of a split package", pkg, strings.Join(shards, " and "))
		}
	}
}

func TestEachHeavyPackageHasShardsOfItsOwn(t *testing.T) {
	tests := []struct {
		shards []string
		want   []string
	}{
		{[]string{"server", "server-a"}, []string{mp + "/internal/huddle/server"}},
		{[]string{"cmd", "cmd-heavy"}, []string{mp + "/cmd/huddle"}},
	}
	for _, tt := range tests {
		for _, shard := range tt.shards {
			if got := shardPackages(t, shard); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("shard %s = %v, want %v", shard, got, tt.want)
			}
		}
	}
	for _, own := range []string{mp + "/internal/core", mp + "/internal/huddle/server", mp + "/cmd/huddle"} {
		for _, pkg := range shardPackages(t, "rest") {
			if pkg == own || strings.HasPrefix(pkg, own+"/") {
				t.Errorf("the rest shard also runs %s", pkg)
			}
		}
	}
}

// splitFlags returns the run and skip patterns of one split pair, failing unless the pair's flags
// are -skip= on the shard and -run= on its sibling.
func splitFlags(t *testing.T, shard, sibling string) (skip, run string) {
	t.Helper()
	skipFlag, runFlag := shardScript(t, "shard-flags.sh", shard), shardScript(t, "shard-flags.sh", sibling)
	if !strings.HasPrefix(skipFlag, "-skip=") || !strings.HasPrefix(runFlag, "-run=") {
		t.Fatalf("flags of %s = %q and of %s = %q, want -skip=<heavy> and -run=<heavy>", shard, skipFlag, sibling, runFlag)
	}
	return strings.TrimPrefix(skipFlag, "-skip="), strings.TrimPrefix(runFlag, "-run=")
}

func TestSplitShardsUseOneRegex(t *testing.T) {
	for shard, sibling := range splitPairs {
		skip, run := splitFlags(t, shard, sibling)
		if skip != run {
			t.Errorf("%s skips %q but %s runs %q: a test could fall between them", shard, skip, sibling, run)
		}
		if _, err := regexp.Compile(run); err != nil {
			t.Errorf("the %s heavy pattern does not compile: %v", shard, err)
		}
	}
}

func TestUnsplitShardsPrintNoFlags(t *testing.T) {
	for _, shard := range []string{"core", "rest"} {
		if got := shardScript(t, "shard-flags.sh", shard); got != "" {
			t.Errorf("shard %s prints flags %q, want none", shard, got)
		}
	}
}

// listTests returns the Test, Fuzz and Example names go test -list finds in pkg.
func listTests(t *testing.T, pkg string) []string {
	t.Helper()
	cmd := exec.Command("go", "test", "-list", ".*", pkg)
	cmd.Dir = "../.."
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go test -list %s: %v: %s", pkg, err, strings.TrimSpace(stderr.String()))
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "Test") || strings.HasPrefix(line, "Fuzz") || strings.HasPrefix(line, "Example") {
			names = append(names, strings.TrimSpace(line))
		}
	}
	return names
}

func TestEveryTestRunsInExactlyOneHalfOfASplitPackage(t *testing.T) {
	for shard, sibling := range splitPairs {
		skip, run := splitFlags(t, shard, sibling)
		pkg := shardPackages(t, shard)[0]
		names := listTests(t, pkg)
		if len(names) == 0 {
			t.Fatalf("go test -list found no test in %s; the check would guard nothing", pkg)
		}
		skipRe, runRe := regexp.MustCompile(skip), regexp.MustCompile(run)
		inShardCount, inSiblingCount := 0, 0
		for _, name := range names {
			inShard, inSibling := !skipRe.MatchString(name), runRe.MatchString(name)
			if inShard == inSibling {
				t.Errorf("%s runs in %d of the %s and %s shards, want exactly 1", name, btoi(inShard)+btoi(inSibling), shard, sibling)
			}
			inShardCount += btoi(inShard)
			inSiblingCount += btoi(inSibling)
		}
		if inShardCount == 0 || inSiblingCount == 0 {
			t.Errorf("%s runs %d tests of %s and %s runs %d: a half is empty", shard, inShardCount, pkg, sibling, inSiblingCount)
		}
	}
}

// A heavy pattern that lists test names names real ones, so a renamed test cannot drop out of the
// heavy shard unseen; a pattern of another shape is held by the checks above.
func TestHeavyPatternNamesRealTests(t *testing.T) {
	for shard, sibling := range splitPairs {
		_, run := splitFlags(t, shard, sibling)
		inner, ok := strings.CutSuffix(strings.TrimPrefix(run, "^("), ")$")
		if !ok || !strings.HasPrefix(run, "^(") {
			continue
		}
		exists := map[string]bool{}
		for _, name := range listTests(t, shardPackages(t, shard)[0]) {
			exists[name] = true
		}
		for _, name := range strings.Split(inner, "|") {
			if !exists[name] {
				t.Errorf("the %s pattern names %s, which is not a test of its package", sibling, name)
			}
		}
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestShardSelectorRejectsAnUnknownShard(t *testing.T) {
	cmd := exec.Command("sh", "scripts/shard-packages.sh", "everything")
	cmd.Dir = "../.."
	out, err := cmd.Output()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 || len(out) != 0 {
		t.Errorf("an unknown shard printed %q with err %v, want no packages and exit 2", out, err)
	}
}

func readCI(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestCIRunsEveryShardOnLinuxAndMacOS(t *testing.T) {
	matrices := regexp.MustCompile(`(?m)^\s+shard: \[(.*)\]$`).FindAllStringSubmatch(readCI(t), -1)
	if len(matrices) != 2 {
		t.Fatalf("ci.yml has %d shard matrices, want 2: the Linux job and the macOS job", len(matrices))
	}
	want := strings.Join(ciShards, ", ")
	for _, m := range matrices {
		if m[1] != want {
			t.Errorf("a shard matrix lists [%s], want [%s]", m[1], want)
		}
	}
}

func TestCIJobsSelectShardPackagesWithTheScript(t *testing.T) {
	ci := readCI(t)
	if got := strings.Count(ci, "sh scripts/shard-packages.sh"); got != 2 {
		t.Errorf("ci.yml calls scripts/shard-packages.sh %d times, want 2: the Linux job and the macOS job", got)
	}
	if got := strings.Count(ci, "sh scripts/shard-flags.sh"); got != 2 {
		t.Errorf("ci.yml calls scripts/shard-flags.sh %d times, want 2: the Linux job and the macOS job", got)
	}
	if regexp.MustCompile(`-(run|skip)[ =]'?\^\(`).MatchString(ci) {
		t.Error("ci.yml carries a test pattern inline; the heavy tests are listed once, in scripts/shard-flags.sh")
	}
	if strings.Contains(ci, "go list ./...") {
		t.Error("ci.yml lists packages inline; the shard selector lives in scripts/shard-packages.sh")
	}
}

func TestGoListPackagesListsTheModule(t *testing.T) {
	// The package's own dir is a one-package module subtree; the real go list must find it.
	pkgs, err := goListPackages(".")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{mp + "/scripts/covergate"}
	if !reflect.DeepEqual(pkgs, want) {
		t.Errorf("goListPackages(\".\") = %v, want %v", pkgs, want)
	}
}
