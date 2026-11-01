package convo

// One hand-built fixture per Claude Code version under testdata/cc/<version>/: the main transcript
// main.jsonl and the subagents folder main/subagents/ that SubagentsDir expects beside it. Every
// version's files carry the same shapes; only the version stamp differs.
//
// Versions 2.1.303, 2.1.308 and 2.1.309 have a Mac sample the shapes follow. Versions 2.1.304 to
// 2.1.307 are derived from 2.1.303 with the version stamp changed: they are not samples, and a
// real sample replaces a derived file when one is seen. The values are invented; no text comes
// from a real transcript.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// fixtureVersions are the Claude Code versions that have a fixture folder.
var fixtureVersions = []string{"2.1.303", "2.1.304", "2.1.305", "2.1.306", "2.1.307", "2.1.308", "2.1.309"}

// fixtureMain answers the main transcript of one version's fixture.
func fixtureMain(version string) string {
	return filepath.Join("testdata", "cc", version, "main.jsonl")
}

// fixtureFiles answers every file of one version that holds decoded keys: the main transcript and
// the files in its subagents folder.
func fixtureFiles(t *testing.T, version string) []string {
	t.Helper()
	files := []string{fixtureMain(version)}
	subs, err := filepath.Glob(filepath.Join(SubagentsDir(fixtureMain(version)), "*"))
	if err != nil {
		t.Fatal(err)
	}
	return append(files, subs...)
}

// wantFixtureItems are the items every version's main transcript decodes to.
func wantFixtureItems() []Item {
	return []Item{
		{ID: "u-01:0", Kind: KindUser, At: "2026-01-01T09:00:00.000Z", Text: "Plan the fixture work."},
		{ID: "a-01:0", Kind: KindThinking, At: "2026-01-01T09:00:01.000Z", Text: "Weigh the options.", DurationMS: 1200},
		{ID: "a-01:1", Kind: KindAssistant, At: "2026-01-01T09:00:01.000Z", Text: "Starting with a shell check."},
		{ID: "a-01:2", Kind: KindToolUse, At: "2026-01-01T09:00:01.000Z", Tool: "Bash", ToolUseID: "toolu_sh1",
			Input: `{"command":"false","description":"A failing check"}`},
		{ID: "u-02:0", Kind: KindToolResult, At: "2026-01-01T09:00:02.000Z", ToolUseID: "toolu_sh1", IsError: true,
			Text:   "Exit code 2\nfixture failure",
			Result: &ToolFacts{ExitCode: intPtr(2), Stderr: "fixture failure", Interrupted: true}},
		{ID: "u-03:0", Kind: KindUser, At: "2026-01-01T09:00:03.000Z", Text: "Look at this picture."},
		{ID: "u-03:1", Kind: KindImage, At: "2026-01-01T09:00:03.000Z", MediaType: "image/png", Bytes: 5},
		{ID: "att-01:0", Kind: KindFile, At: "2026-01-01T09:00:04.000Z", Parent: "u-03", Path: "docs/notes.txt", Lines: 7,
			Text: "line one\nline two\n"},
		{ID: "a-02:0", Kind: KindToolUse, At: "2026-01-01T09:00:05.000Z", Tool: "Edit", ToolUseID: "toolu_ed1",
			Input: `{"file_path":"/srv/fixture/docs/notes.txt","old_string":"one","new_string":"1"}`},
		{ID: "u-04:0", Kind: KindToolResult, At: "2026-01-01T09:00:06.000Z", ToolUseID: "toolu_ed1",
			Text: "Edited the file.\nNothing else changed.",
			Result: &ToolFacts{FilePath: "/srv/fixture/docs/notes.txt", Hunks: []Hunk{
				{OldStart: 1, OldLines: 2, NewStart: 1, NewLines: 2, Lines: []string{" top", "-line one", "+line 1"}},
			}}},
		{ID: "a-03:0", Kind: KindToolUse, At: "2026-01-01T09:00:07.000Z", Tool: "AskUserQuestion", ToolUseID: "toolu_q1",
			Input: `{"questions":[{"question":"Which fixture?","header":"Fixture"}]}`},
		{ID: "u-05:0", Kind: KindToolResult, At: "2026-01-01T09:00:08.000Z", ToolUseID: "toolu_q1", Text: "User answered.",
			Result: &ToolFacts{Answers: []Answer{{Header: "Fixture", Question: "Which fixture?", Answer: "The small one"}}}},
		{ID: "a-04:0", Kind: KindToolUse, At: "2026-01-01T09:00:09.000Z", Tool: "Bash", ToolUseID: "toolu_bg1",
			Input: `{"command":"sleep 1","description":"A background wait","run_in_background":true}`},
		{ID: "u-06:0", Kind: KindToolResult, At: "2026-01-01T09:00:10.000Z", ToolUseID: "toolu_bg1",
			Text: "Command running in background with ID: bg1a2b3c. Output is being written to: " +
				"/srv/fixture/sess-fixture-0001/tasks/bg1a2b3c.output. You will be notified when it completes.",
			Result: &ToolFacts{BackgroundID: "bg1a2b3c"}},
		{ID: "a-05:0", Kind: KindAssistant, At: "2026-01-01T09:00:14.000Z", Text: "All done."},
	}
}

// wantFixtureShells is the background command every version's main transcript ran.
func wantFixtureShells() []Shell {
	return []Shell{{
		ID:          "bg1a2b3c",
		Command:     "sleep 1",
		Description: "A background wait",
		State:       "ended",
		Status:      "completed",
		ExitCode:    intPtr(0),
		StartedAt:   "2026-01-01T09:00:09.000Z",
		EndedAt:     "2026-01-01T09:00:12.000Z",
		output:      "/srv/fixture/sess-fixture-0001/tasks/bg1a2b3c.output",
	}}
}

// wantFixtureAgents are the agents every version's session lists.
func wantFixtureAgents() []Agent {
	return []Agent{
		{ID: MainAgent, Type: "main", Running: true, StartedAt: "2026-01-01T09:00:00.000Z"},
		{ID: "a1", Type: "general-purpose", Description: "Fixture worker", Depth: 1, Model: "fixture-model",
			StartedAt: "2026-01-01T09:00:20.000Z"},
	}
}

// wantFixtureAgentItems are the items the subagent a1 transcript decodes to in every version.
func wantFixtureAgentItems() []Item {
	return []Item{
		{ID: "su-01:0", Kind: KindUser, At: "2026-01-01T09:00:20.000Z", Text: "Scan the fixture folder."},
		{ID: "sa-01:0", Kind: KindAssistant, At: "2026-01-01T09:00:21.000Z", Text: "Scanned."},
	}
}

func TestEveryVersionDecodesTheSameItems(t *testing.T) {
	ctx := context.Background()
	for _, version := range fixtureVersions {
		t.Run(version, func(t *testing.T) {
			main := fixtureMain(version)

			page, err := Read(ctx, main, 0)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if !reflect.DeepEqual(page.Items, wantFixtureItems()) {
				t.Errorf("items differ:\n got %+v\nwant %+v", page.Items, wantFixtureItems())
			}

			shells, err := Shells(ctx, main)
			if err != nil {
				t.Fatalf("shells: %v", err)
			}
			if !reflect.DeepEqual(shells, wantFixtureShells()) {
				t.Errorf("shells differ:\n got %+v\nwant %+v", shells, wantFixtureShells())
			}

			agents, err := Agents(ctx, main)
			if err != nil {
				t.Fatalf("agents: %v", err)
			}
			if !reflect.DeepEqual(agents, wantFixtureAgents()) {
				t.Errorf("agents differ:\n got %+v\nwant %+v", agents, wantFixtureAgents())
			}

			path, ok := AgentPath(main, "a1")
			if !ok {
				t.Fatal("agent a1 has no transcript")
			}
			agentPage, err := ReadAgent(ctx, path, 0)
			if err != nil {
				t.Fatalf("read agent: %v", err)
			}
			if !reflect.DeepEqual(agentPage.Items, wantFixtureAgentItems()) {
				t.Errorf("agent items differ:\n got %+v\nwant %+v", agentPage.Items, wantFixtureAgentItems())
			}
		})
	}
}

// decodedJSONTags answers the json key names the structs of the decoded shapes read, descending
// into the anonymous structs that hold nested objects.
func decodedJSONTags() []string {
	shapes := []reflect.Type{
		reflect.TypeOf(transcriptLine{}), reflect.TypeOf(contentBlock{}), reflect.TypeOf(imageSource{}),
		reflect.TypeOf(toolUseResult{}), reflect.TypeOf(structuredHunk{}), reflect.TypeOf(askedQuestion{}),
		reflect.TypeOf(attachment{}), reflect.TypeOf(attachedContent{}), reflect.TypeOf(attachedFile{}),
		reflect.TypeOf(shellLine{}), reflect.TypeOf(agentMeta{}),
	}
	seen := map[string]bool{}
	var collect func(reflect.Type)
	collect = func(shape reflect.Type) {
		for i := 0; i < shape.NumField(); i++ {
			field := shape.Field(i)
			if tag, _, _ := strings.Cut(field.Tag.Get("json"), ","); tag != "" && tag != "-" {
				seen[tag] = true
			}
			if field.Type.Kind() == reflect.Struct && field.Type.Name() == "" {
				collect(field.Type)
			}
		}
	}
	for _, shape := range shapes {
		collect(shape)
	}
	tags := make([]string, 0, len(seen))
	for tag := range seen {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	return tags
}

// addKeys adds every object key found anywhere in the decoded JSON value to keys.
func addKeys(value any, keys map[string]bool) {
	switch v := value.(type) {
	case map[string]any:
		for key, inner := range v {
			keys[key] = true
			addKeys(inner, keys)
		}
	case []any:
		for _, inner := range v {
			addKeys(inner, keys)
		}
	}
}

// fixtureKeys answers every JSON key name in one version's files.
func fixtureKeys(t *testing.T, version string) map[string]bool {
	t.Helper()
	keys := map[string]bool{}
	for _, file := range fixtureFiles(t, version) {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		documents := [][]byte{raw}
		if strings.HasSuffix(file, ".jsonl") {
			documents = nil
			for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
				documents = append(documents, []byte(line))
			}
		}
		for _, document := range documents {
			var value any
			if err := json.Unmarshal(document, &value); err != nil {
				t.Fatalf("%s: %v", file, err)
			}
			addKeys(value, keys)
		}
	}
	return keys
}

func TestEveryDecodedKeyIsInEveryFixture(t *testing.T) {
	tags := decodedJSONTags()
	if len(tags) == 0 {
		t.Fatal("no decoded keys found")
	}
	for _, version := range fixtureVersions {
		t.Run(version, func(t *testing.T) {
			keys := fixtureKeys(t, version)
			for _, tag := range tags {
				if !keys[tag] {
					t.Errorf("no fixture file of %s has the decoded key %q", version, tag)
				}
			}
		})
	}
}
