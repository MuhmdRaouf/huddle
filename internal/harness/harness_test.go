package harness

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// The machine a harness reads: a zero Env and ProcessEnv read the process's own world and
// run the real PATH search, a name no adapter answers is an error that says so, and the
// XDG fallback lands in the user's config home.

func TestEnvDefaultsReadTheProcess(t *testing.T) {
	env := ProcessEnv()
	if env.lookup("PATH") == "" {
		t.Error("lookup with no function found nothing, even for PATH")
	}
	if env.which("no-such-harness-binary-xyz") {
		t.Error("which with no function found a binary that is not there")
	}
}

func TestByNameAnswersEveryAdapterAndRejectsTheRest(t *testing.T) {
	for _, name := range Names() {
		a, err := ByName(name)
		if err != nil {
			t.Fatalf("ByName(%q): %v", name, err)
		}
		if a.Name() != name {
			t.Errorf("ByName(%q).Name() = %q", name, a.Name())
		}
	}
	_, err := ByName("not-a-harness")
	var unknown UnknownHarnessError
	if !errors.As(err, &unknown) || unknown.Name != "not-a-harness" {
		t.Fatalf("error %T (%v), want an UnknownHarnessError naming the name", err, err)
	}
	if !strings.Contains(err.Error(), "not-a-harness") {
		t.Errorf("error %q does not name the name", err)
	}
}

func TestXDGDirPrefersTheXDGRootAndFallsBackToHome(t *testing.T) {
	xdg := Env{Lookup: func(key string) string {
		switch key {
		case "HOME":
			return "/users/home"
		case "XDG_CONFIG_HOME":
			return "/users/xdg"
		}
		return ""
	}}
	if got := xdgDir(xdg, "opencode"); got != filepath.Join("/users/xdg", "opencode") {
		t.Errorf("xdgDir = %q, want it under the XDG root", got)
	}
	plain := Env{Lookup: func(key string) string {
		if key == "HOME" {
			return "/users/home"
		}
		return ""
	}}
	if got := xdgDir(plain, "crush"); got != filepath.Join("/users/home", ".config", "crush") {
		t.Errorf("xdgDir = %q, want it under the home's config", got)
	}
}

// Every build copes with an entry that names no env: the record the harness reads still
// carries its env object, empty as it is.
func TestBuildsCarryAnEmptyEnvWhenTheEntryNamesNone(t *testing.T) {
	entry := Entry{Command: "huddle", Args: []string{"mcp"}}
	cases := []struct {
		name   string
		build  func(Entry) ([]byte, error)
		envKey string
	}{
		{"gemini and qwen", stdioBuild, "env"},
		{"opencode", openCodeBuild, "environment"},
		{"crush", crushBuild, "env"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := tc.build(entry)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			value, err := jsfmt.DecodeValue(raw)
			if err != nil {
				t.Fatalf("the record does not parse: %v\n%s", err, raw)
			}
			envBlock, ok := field(value, tc.envKey)
			if !ok || envBlock.Kind() != jsfmt.KindObject || len(envBlock.Keys()) != 0 {
				t.Errorf("%s = %v, want an empty object", tc.envKey, value.EncodeJSON())
			}
		})
	}
}
