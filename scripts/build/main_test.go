package main

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseVersion(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
		wantErr bool
	}{
		{"valid", "0.1.0", "0.1.0", false},
		{"trailing newline", "0.2.1\n", "0.2.1", false},
		{"surrounding whitespace", "  1.2.3\t\n", "1.2.3", false},
		{"two components", "0.1", "", true},
		{"four components", "0.1.0.0", "", true},
		{"prerelease suffix", "0.1.0-rc1", "", true},
		{"leading v", "v0.1.0", "", true},
		{"non numeric patch", "0.1.x", "", true},
		{"empty", "", "", true},
		{"whitespace only", " \n", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseVersion(tt.content)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseVersion(%q) = %q, want an error", tt.content, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseVersion(%q) failed: %v", tt.content, err)
			}
			if got != tt.want {
				t.Errorf("parseVersion(%q) = %q, want %q", tt.content, got, tt.want)
			}
		})
	}
}

// goldenAssets is a fixed fake build result: every target, one digest per
// target in release order, so a wrong order shows up in the goldens.
func goldenAssets() []builtAsset {
	return []builtAsset{
		{Target: target{OS: "darwin", Arch: "amd64"}, Name: "huddle-darwin-amd64", SHA256: "aa", Size: 1024},
		{Target: target{OS: "darwin", Arch: "arm64"}, Name: "huddle-darwin-arm64", SHA256: "bb", Size: 2048},
		{Target: target{OS: "linux", Arch: "amd64"}, Name: "huddle-linux-amd64", SHA256: "cc", Size: 3072},
		{Target: target{OS: "linux", Arch: "arm64"}, Name: "huddle-linux-arm64", SHA256: "dd", Size: 4096},
	}
}

func TestTargets(t *testing.T) {
	want := []struct {
		key   string
		asset string
	}{
		{"darwin-amd64", "huddle-darwin-amd64"},
		{"darwin-arm64", "huddle-darwin-arm64"},
		{"linux-amd64", "huddle-linux-amd64"},
		{"linux-arm64", "huddle-linux-arm64"},
	}
	if len(targets) != len(want) {
		t.Fatalf("targets has %d entries, want %d", len(targets), len(want))
	}
	for i, tt := range want {
		if got := targets[i].key(); got != tt.key {
			t.Errorf("targets[%d].key() = %q, want %q", i, got, tt.key)
		}
		if got := targets[i].asset("huddle"); got != tt.asset {
			t.Errorf("targets[%d].asset() = %q, want %q", i, got, tt.asset)
		}
	}
}

func TestBinaryByName(t *testing.T) {
	for _, name := range []string{"huddle", "radar", "kanban"} {
		bin, ok := binaryByName(name)
		if !ok {
			t.Errorf("binaryByName(%q) not found", name)
			continue
		}
		if bin.name != name {
			t.Errorf("binaryByName(%q).name = %q", name, bin.name)
		}
	}
	if _, ok := binaryByName("plugin-core"); ok {
		t.Error("binaryByName(\"plugin-core\") found, want only huddle, radar and kanban")
	}
}

func TestBinaryAssetNames(t *testing.T) {
	want := []struct {
		key    string
		radar  string
		huddle string
		kanban string
	}{
		{"darwin-amd64", "radar-darwin-amd64", "huddle-darwin-amd64", "kanban-darwin-amd64"},
		{"darwin-arm64", "radar-darwin-arm64", "huddle-darwin-arm64", "kanban-darwin-arm64"},
		{"linux-amd64", "radar-linux-amd64", "huddle-linux-amd64", "kanban-linux-amd64"},
		{"linux-arm64", "radar-linux-arm64", "huddle-linux-arm64", "kanban-linux-arm64"},
	}
	radar, ok := binaryByName("radar")
	if !ok {
		t.Fatal("radar binary not found")
	}
	huddle, ok := binaryByName("huddle")
	if !ok {
		t.Fatal("huddle binary not found")
	}
	kanban, ok := binaryByName("kanban")
	if !ok {
		t.Fatal("kanban binary not found")
	}
	for i, tt := range want {
		if got := targets[i].key(); got != tt.key {
			t.Errorf("targets[%d].key() = %q, want %q", i, got, tt.key)
		}
		if got := targets[i].asset(radar.name); got != tt.radar {
			t.Errorf("targets[%d].asset(radar) = %q, want %q", i, got, tt.radar)
		}
		if got := targets[i].asset(huddle.name); got != tt.huddle {
			t.Errorf("targets[%d].asset(huddle) = %q, want %q", i, got, tt.huddle)
		}
		if got := targets[i].asset(kanban.name); got != tt.kanban {
			t.Errorf("targets[%d].asset(kanban) = %q, want %q", i, got, tt.kanban)
		}
	}
}

func TestBinaryPaths(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		bin          string
		versionFile  string
		manifestPath string
	}{
		{"huddle", "VERSION", filepath.Join("release", "huddle", "v0.0.1.json")},
		{"radar", "VERSION", filepath.Join("release", "radar", "v0.0.1.json")},
		{"kanban", "VERSION", filepath.Join("release", "kanban", "v0.0.1.json")},
	}
	for _, tt := range tests {
		t.Run(tt.bin, func(t *testing.T) {
			bin, ok := binaryByName(tt.bin)
			if !ok {
				t.Fatalf("binaryByName(%q) not found", tt.bin)
			}
			if got, want := bin.versionFile, tt.versionFile; got != want {
				t.Errorf("versionFile = %q, want %q", got, want)
			}
			if got, want := bin.manifestPath(root, "0.0.1"), filepath.Join(root, tt.manifestPath); got != want {
				t.Errorf("manifestPath() = %q, want %q", got, want)
			}
		})
	}
}

func TestSelectTargets(t *testing.T) {
	if got := selectTargets(nil); !reflect.DeepEqual(got, targets) {
		t.Errorf("selectTargets(nil) = %v, want every target", got)
	}
	got := selectTargets([]string{"linux/arm64", "linux/amd64", "linux/arm64"})
	want := []target{{OS: "linux", Arch: "amd64"}, {OS: "linux", Arch: "arm64"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("selectTargets() = %v, want %v (release order, deduplicated)", got, want)
	}
}

func TestSHA256SUMS(t *testing.T) {
	want := "aa  huddle-darwin-amd64\n" +
		"bb  huddle-darwin-arm64\n" +
		"cc  huddle-linux-amd64\n" +
		"dd  huddle-linux-arm64\n"
	assets := goldenAssets()
	reversed := make([]builtAsset, 0, len(assets))
	for i := len(assets) - 1; i >= 0; i-- {
		reversed = append(reversed, assets[i])
	}
	for _, list := range [][]builtAsset{assets, reversed} {
		if got := string(sha256sums(list)); got != want {
			t.Errorf("sha256sums() = %q, want %q", got, want)
		}
	}
}

func TestManifestJSON(t *testing.T) {
	want := `{
  "version": "0.1.0",
  "tag": "huddle-v0.1.0",
  "assets": {
    "darwin-amd64": {
      "name": "huddle-darwin-amd64",
      "sha256": "aa",
      "size": 1024
    },
    "darwin-arm64": {
      "name": "huddle-darwin-arm64",
      "sha256": "bb",
      "size": 2048
    },
    "linux-amd64": {
      "name": "huddle-linux-amd64",
      "sha256": "cc",
      "size": 3072
    },
    "linux-arm64": {
      "name": "huddle-linux-arm64",
      "sha256": "dd",
      "size": 4096
    }
  }
}
`
	data, err := manifestBytes(buildManifest(defaultBinary(t), "0.1.0", goldenAssets()))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Errorf("manifest bytes =\n%s\nwant\n%s", data, want)
	}
}

func TestRadarManifestJSON(t *testing.T) {
	want := `{
  "version": "0.0.1",
  "tag": "radar-v0.0.1",
  "assets": {
    "darwin-amd64": {
      "name": "radar-darwin-amd64",
      "sha256": "aa",
      "size": 1024
    },
    "linux-arm64": {
      "name": "radar-linux-arm64",
      "sha256": "ff",
      "size": 6144
    }
  }
}
`
	assets := []builtAsset{
		{Target: target{OS: "darwin", Arch: "amd64"}, Name: "radar-darwin-amd64", SHA256: "aa", Size: 1024},
		{Target: target{OS: "linux", Arch: "arm64"}, Name: "radar-linux-arm64", SHA256: "ff", Size: 6144},
	}
	radar, ok := binaryByName("radar")
	if !ok {
		t.Fatal("radar binary not found")
	}
	data, err := manifestBytes(buildManifest(radar, "0.0.1", assets))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Errorf("manifest bytes =\n%s\nwant\n%s", data, want)
	}
}

func TestKanbanManifestJSON(t *testing.T) {
	want := `{
  "version": "0.0.1",
  "tag": "kanban-v0.0.1",
  "assets": {
    "darwin-arm64": {
      "name": "kanban-darwin-arm64",
      "sha256": "aa",
      "size": 1024
    },
    "linux-amd64": {
      "name": "kanban-linux-amd64",
      "sha256": "cc",
      "size": 3072
    }
  }
}
`
	assets := []builtAsset{
		{Target: target{OS: "darwin", Arch: "arm64"}, Name: "kanban-darwin-arm64", SHA256: "aa", Size: 1024},
		{Target: target{OS: "linux", Arch: "amd64"}, Name: "kanban-linux-amd64", SHA256: "cc", Size: 3072},
	}
	kanban, ok := binaryByName("kanban")
	if !ok {
		t.Fatal("kanban binary not found")
	}
	data, err := manifestBytes(buildManifest(kanban, "0.0.1", assets))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Errorf("manifest bytes =\n%s\nwant\n%s", data, want)
	}
}

// defaultBinary is the huddle binary spec, the -bin default.
func defaultBinary(t *testing.T) binary {
	t.Helper()
	bin, ok := binaryByName("huddle")
	if !ok {
		t.Fatal("huddle binary not found")
	}
	return bin
}

func TestCompareLines(t *testing.T) {
	linux := builtAsset{Target: target{OS: "linux", Arch: "amd64"}, Name: "huddle-linux-amd64", SHA256: "zz", Size: 1}
	darwin := builtAsset{Target: target{OS: "darwin", Arch: "arm64"}, Name: "huddle-darwin-arm64", SHA256: "ee", Size: 2}
	assets := []builtAsset{linux, darwin}

	t.Run("match", func(t *testing.T) {
		want := map[string]manifestAsset{
			"linux-amd64":  {Name: "huddle-linux-amd64", SHA256: "zz", Size: 1},
			"darwin-arm64": {Name: "huddle-darwin-arm64", SHA256: "ee", Size: 2},
		}
		lines, ok := compareLines(assets, want)
		if !ok {
			t.Error("compareLines reported a mismatch for matching digests")
		}
		wantLines := []string{"ok huddle-linux-amd64", "ok huddle-darwin-arm64"}
		if !reflect.DeepEqual(lines, wantLines) {
			t.Errorf("lines = %q, want %q", lines, wantLines)
		}
	})

	t.Run("mismatch", func(t *testing.T) {
		want := map[string]manifestAsset{
			"linux-amd64":  {Name: "huddle-linux-amd64", SHA256: "aa", Size: 1},
			"darwin-arm64": {Name: "huddle-darwin-arm64", SHA256: "ee", Size: 2},
		}
		lines, ok := compareLines(assets, want)
		if ok {
			t.Error("compareLines reported ok for a differing digest")
		}
		wantLines := []string{
			"MISMATCH huddle-linux-amd64 want aa got zz",
			"ok huddle-darwin-arm64",
		}
		if !reflect.DeepEqual(lines, wantLines) {
			t.Errorf("lines = %q, want %q", lines, wantLines)
		}
	})

	t.Run("missing manifest entry", func(t *testing.T) {
		want := map[string]manifestAsset{
			"darwin-arm64": {Name: "huddle-darwin-arm64", SHA256: "ee", Size: 2},
		}
		lines, ok := compareLines(assets, want)
		if ok {
			t.Error("compareLines reported ok for a target the manifest does not list")
		}
		wantLines := []string{
			"MISMATCH huddle-linux-amd64 want missing got zz",
			"ok huddle-darwin-arm64",
		}
		if !reflect.DeepEqual(lines, wantLines) {
			t.Errorf("lines = %q, want %q", lines, wantLines)
		}
	})
}

func TestLoadManifestAssets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v0.1.0.json")
	data, err := manifestBytes(buildManifest(defaultBinary(t), "0.1.0", goldenAssets()))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := loadManifestAssets(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(targets) {
		t.Fatalf("loaded %d assets, want %d", len(got), len(targets))
	}
	darwin := got["darwin-amd64"]
	if darwin.Name != "huddle-darwin-amd64" || darwin.SHA256 != "aa" || darwin.Size != 1024 {
		t.Errorf("darwin-amd64 = %+v, want {huddle-darwin-amd64 aa 1024}", darwin)
	}
}

func TestLoadManifestAssetsMissing(t *testing.T) {
	_, err := loadManifestAssets(filepath.Join(t.TempDir(), "v9.9.9.json"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("error = %v, want fs.ErrNotExist", err)
	}
}

func TestGoBuildArgs(t *testing.T) {
	huddle := defaultBinary(t)
	out := filepath.Join("dist", "huddle-linux-amd64")
	want := []string{
		"build",
		"-trimpath",
		"-buildvcs=false",
		"-ldflags", ldflags(huddle, "0.1.0"),
		"-o", out,
		"./cmd/huddle",
	}
	if got := goBuildArgs(huddle, out, "0.1.0"); !reflect.DeepEqual(got, want) {
		t.Errorf("goBuildArgs() = %q, want %q", got, want)
	}
	const oneTwoThree = "-s -w -buildid= -X main.version=1.2.3"
	if got := ldflags(huddle, "1.2.3"); got != oneTwoThree {
		t.Errorf("ldflags(1.2.3) = %q, want %q", got, oneTwoThree)
	}
}

func TestGoBuildArgsRadarKanban(t *testing.T) {
	tests := []struct {
		bin     string
		pkg     string
		ldflags string
	}{
		{"radar", "./cmd/radar", "-s -w -buildid= -X main.version=0.0.1"},
		{"kanban", "./cmd/kanban", "-s -w -buildid= -X main.version=0.0.1"},
	}
	for _, tt := range tests {
		t.Run(tt.bin, func(t *testing.T) {
			bin, ok := binaryByName(tt.bin)
			if !ok {
				t.Fatalf("binaryByName(%q) not found", tt.bin)
			}
			if bin.pkg != tt.pkg {
				t.Errorf("pkg = %q, want %q", bin.pkg, tt.pkg)
			}
			out := filepath.Join("dist", bin.name+"-linux-amd64")
			want := []string{
				"build",
				"-trimpath",
				"-buildvcs=false",
				"-ldflags", tt.ldflags,
				"-o", out,
				tt.pkg,
			}
			if got := goBuildArgs(bin, out, "0.0.1"); !reflect.DeepEqual(got, want) {
				t.Errorf("goBuildArgs() = %q, want %q", got, want)
			}
			if got := ldflags(bin, "0.0.1"); got != tt.ldflags {
				t.Errorf("ldflags() = %q, want %q", got, tt.ldflags)
			}
		})
	}
}

func TestBuildEnv(t *testing.T) {
	environ := []string{
		"GOFLAGS=-tags=debug",
		"CGO_ENABLED=1",
		"GOOS=darwin",
		"GOARCH=arm64",
		"PATH=/usr/bin:/bin",
		"HOME=/users/raouf",
	}
	want := []string{
		"PATH=/usr/bin:/bin",
		"HOME=/users/raouf",
		"CGO_ENABLED=0",
		"GOOS=linux",
		"GOARCH=amd64",
		"GOFLAGS=",
	}
	got := buildEnv(environ, target{OS: "linux", Arch: "amd64"})
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildEnv() = %q, want %q", got, want)
	}
}

func versionRoot(t *testing.T, content string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "VERSION"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestRunUsageErrors(t *testing.T) {
	root := versionRoot(t, "0.1.0\n")
	tests := []struct {
		name string
		args []string
	}{
		{"unknown flag", []string{"-fresh"}},
		{"stray argument", []string{"extra"}},
		{"unknown binary", []string{"-bin", "zai"}},
		{"empty binary", []string{"-bin", ""}},
		{"target with manifest", []string{"-target", "linux/amd64", "-manifest"}},
		{"manifest with check", []string{"-manifest", "-check"}},
		{"unknown target", []string{"-target", "plan9/amd64"}},
		{"target without arch", []string{"-target", "linux"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(tt.args, root, &stdout, &stderr); code != 2 {
				t.Fatalf("exit = %d, want 2", code)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want empty", stdout.String())
			}
			if stderr.Len() == 0 {
				t.Error("stderr is empty, want a usage message")
			}
		})
	}
}

func TestRunRejectsBadVersion(t *testing.T) {
	root := versionRoot(t, "0.1.0-dev")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-target", "linux/amd64"}, root, &stdout, &stderr); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	if stderr.Len() == 0 {
		t.Error("stderr is empty, want a version error")
	}
}

func TestRunCheckWithoutManifest(t *testing.T) {
	root := versionRoot(t, "0.1.0\n")
	var stdout, stderr bytes.Buffer
	code := run([]string{"-check"}, root, &stdout, &stderr)
	if code != 4 {
		t.Fatalf("exit = %d, want 4", code)
	}
	if got, want := stderr.String(), "no manifest for 0.1.0\n"; got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
}

// repoWithVersion builds a throwaway module root and writes content to the
// root VERSION file every binary takes its version from.
func repoWithVersion(t *testing.T, name, content string) string {
	t.Helper()
	bin, ok := binaryByName(name)
	if !ok {
		t.Fatalf("binaryByName(%q) not found", name)
	}
	root := t.TempDir()
	path := bin.versionPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestRunBinCheckWithoutManifest(t *testing.T) {
	for _, bin := range []string{"huddle", "radar", "kanban"} {
		t.Run(bin, func(t *testing.T) {
			root := repoWithVersion(t, bin, "0.0.1\n")
			var stdout, stderr bytes.Buffer
			code := run([]string{"-bin", bin, "-check"}, root, &stdout, &stderr)
			if code != 4 {
				t.Fatalf("exit = %d, want 4", code)
			}
			if got, want := stderr.String(), "no manifest for 0.0.1\n"; got != want {
				t.Errorf("stderr = %q, want %q", got, want)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want empty", stdout.String())
			}
		})
	}
}

// TestReproducibleLinuxAmd64Build builds the same target twice from scratch
// and requires identical digests: the whole point of the pinned flags.
func TestReproducibleLinuxAmd64Build(t *testing.T) {
	if testing.Short() {
		t.Skip("real cross-build skipped in -short")
	}
	b := builder{GoTool: "go", Root: "../..", Bin: defaultBinary(t), Version: "0.0.0-test", Stderr: os.Stderr}
	linuxAmd64 := target{OS: "linux", Arch: "amd64"}
	first, err := b.build(context.Background(), linuxAmd64, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	second, err := b.build(context.Background(), linuxAmd64, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if first.SHA256 == "" || first.SHA256 != second.SHA256 {
		t.Errorf("two linux/amd64 builds differ: %q vs %q", first.SHA256, second.SHA256)
	}
	if first.Size == 0 || first.Size != second.Size {
		t.Errorf("sizes differ: %d vs %d", first.Size, second.Size)
	}
}
