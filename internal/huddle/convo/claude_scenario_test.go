package convo

// Feature: the transcript reader shows Claude's thinking between its replies: a thinking line
// comes out as one thinking item in conversation order, carrying the line's thinking duration.
// Feature: the reader serves a prompt's attached files and images as references beside it, and
// leaves out every attachment that is not a file.
// Feature: the reader serves what a tool call did beside its answer: an exit code and stderr, a
// background command's id, an edit's hunks and the answers to its questions.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

func TestTheReaderShowsTheThinking(t *testing.T) {
	bdd.Feature(t, "The transcript reader shows Claude's thinking", func(f *bdd.F) {
		f.Scenario("a thinking line with its duration comes between the prompt and the reply", func(s *bdd.S) {
			var path string
			var page Page
			s.Given("a transcript with a prompt, a thinking line of 2400 ms and a text reply", func(t *testing.T) {
				lines := []string{
					`{"type":"user","uuid":"u1","timestamp":"2026-10-26T10:00:01.000Z","message":{"role":"user","content":"run the tests"}}`,
					`{"type":"assistant","uuid":"a1","timestamp":"2026-10-26T10:00:02.000Z","thinkingDurationMs":2400,"message":{"role":"assistant","content":[{"type":"thinking","thinking":"which package fails","signature":"sig-a1"}]}}`,
					`{"type":"assistant","uuid":"a2","timestamp":"2026-10-26T10:00:03.000Z","message":{"role":"assistant","content":[{"type":"text","text":"Done."}]}}`,
				}
				path = filepath.Join(s.T.TempDir(), "claude.jsonl")
				if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			})
			s.When("the reader reads it from offset 0", func(t *testing.T) {
				read, err := Read(t.Context(), path, 0)
				if err != nil {
					t.Fatalf("read the transcript: %v", err)
				}
				page = read
			})
			s.Then("the items are the prompt, the thinking with its 2400 ms and the reply, in that order", func(t *testing.T) {
				checkItems(t, page.Items, []Item{
					{ID: "u1:0", Kind: KindUser, At: "2026-10-26T10:00:01.000Z", Text: "run the tests"},
					{ID: "a1:0", Kind: KindThinking, At: "2026-10-26T10:00:02.000Z", Text: "which package fails", DurationMS: 2400},
					{ID: "a2:0", Kind: KindAssistant, At: "2026-10-26T10:00:03.000Z", Text: "Done."},
				})
			})
		})
	})
}

func TestTheReaderServesAttachmentsAndImages(t *testing.T) {
	bdd.Feature(t, "The transcript reader serves a prompt's attached files and images", func(f *bdd.F) {
		f.Scenario("A prompt's attached files and images are read as references", func(s *bdd.S) {
			var path string
			var page Page
			s.Given("a prompt with an image, two attached files chained to it and an attachment of another kind", func(t *testing.T) {
				lines := []string{
					`{"type":"user","uuid":"u1","timestamp":"2026-10-26T10:00:01.000Z","message":{"role":"user","content":[{"type":"text","text":"read this"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]}}`,
					`{"type":"attachment","uuid":"f1","parentUuid":"u1","timestamp":"2026-10-26T10:00:02.000Z","attachment":{"type":"file","filename":"/abs/path/spec.md","displayPath":"spec.md","content":{"type":"text","file":{"filePath":"/abs/path/spec.md","content":"line one\nline two\n","numLines":2,"startLine":1,"totalLines":2}}}}`,
					`{"type":"attachment","uuid":"f2","parentUuid":"f1","timestamp":"2026-10-26T10:00:03.000Z","attachment":{"type":"file","filename":"/abs/path/notes.log","displayPath":"notes.log"}}`,
					`{"type":"attachment","uuid":"p1","parentUuid":"u1","timestamp":"2026-10-26T10:00:04.000Z","attachment":{"type":"paste","displayPath":"pasted.txt","content":"pasted words"}}`,
				}
				path = filepath.Join(s.T.TempDir(), "claude.jsonl")
				if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			})
			s.When("the reader reads it from offset 0", func(t *testing.T) {
				read, err := Read(t.Context(), path, 0)
				if err != nil {
					t.Fatalf("read the transcript: %v", err)
				}
				page = read
			})
			s.Then("the image and the files come out as references beside the prompt and the paste yields nothing", func(t *testing.T) {
				checkItems(t, page.Items, []Item{
					{ID: "u1:0", Kind: KindUser, At: "2026-10-26T10:00:01.000Z", Text: "read this"},
					{ID: "u1:1", Kind: KindImage, At: "2026-10-26T10:00:01.000Z", MediaType: "image/png", Bytes: 5},
					{ID: "f1:0", Kind: KindFile, At: "2026-10-26T10:00:02.000Z", Parent: "u1", Path: "spec.md", Lines: 2, Text: "line one\nline two\n"},
					{ID: "f2:0", Kind: KindFile, At: "2026-10-26T10:00:03.000Z", Parent: "f1", Path: "notes.log"},
				})
			})
		})
	})
}

func TestTheReaderServesWhatToolsDid(t *testing.T) {
	bdd.Feature(t, "The transcript reader serves what a tool call did", func(f *bdd.F) {
		f.Scenario("A tool's answer carries what the tool did", func(s *bdd.S) {
			var path string
			var page Page
			s.Given("a transcript with a failing command, a background command, an edit and a question, each answered", func(t *testing.T) {
				lines := []string{
					`{"type":"assistant","uuid":"a1","timestamp":"2026-10-26T10:00:01.000Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"go test ./..."}}]}}`,
					`{"type":"user","uuid":"u1","timestamp":"2026-10-26T10:00:02.000Z","toolUseResult":{"stdout":"","stderr":"FAIL pkg\n","interrupted":false,"isImage":false,"noOutputExpected":false},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","is_error":true,"content":"Exit code 2\nFAIL pkg\n"}]}}`,
					`{"type":"assistant","uuid":"a2","timestamp":"2026-10-26T10:00:03.000Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_2","name":"Bash","input":{"command":"bun run dev","run_in_background":true}}]}}`,
					`{"type":"user","uuid":"u2","timestamp":"2026-10-26T10:00:04.000Z","toolUseResult":{"stdout":"dev server up\n","stderr":"","interrupted":false,"isImage":false,"noOutputExpected":false,"backgroundTaskId":"bg9watch"},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_2","content":"dev server up\n"}]}}`,
					`{"type":"assistant","uuid":"a3","timestamp":"2026-10-26T10:00:05.000Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_3","name":"Edit","input":{"file_path":"/abs/a.go","old_string":"old","new_string":"new"}}]}}`,
					`{"type":"user","uuid":"u3","timestamp":"2026-10-26T10:00:06.000Z","toolUseResult":{"filePath":"/abs/a.go","originalFile":"package a\n","structuredPatch":[{"oldStart":10,"oldLines":3,"newStart":10,"newLines":4,"lines":[" keep","-old","+new","+added"," keep"]}]},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_3","content":"updated"}]}}`,
					`{"type":"assistant","uuid":"a4","timestamp":"2026-10-26T10:00:07.000Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_4","name":"AskUserQuestion","input":{"questions":[{"question":"Which port?","header":"Port","options":[{"label":"A"}]}]}}]}}`,
					`{"type":"user","uuid":"u4","timestamp":"2026-10-26T10:00:08.000Z","toolUseResult":{"questions":[{"question":"Which port?","header":"Port","options":[{"label":"A"}]}],"answers":{"Which port?":"A"}},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_4","content":"answered"}]}}`,
				}
				path = filepath.Join(s.T.TempDir(), "claude.jsonl")
				if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			})
			s.When("the reader reads it from offset 0", func(t *testing.T) {
				read, err := Read(t.Context(), path, 0)
				if err != nil {
					t.Fatalf("read the transcript: %v", err)
				}
				page = read
			})
			s.Then("each answer's item carries its exit code and stderr, background id, hunks and answers", func(t *testing.T) {
				checkItems(t, page.Items, []Item{
					{ID: "a1:0", Kind: KindToolUse, At: "2026-10-26T10:00:01.000Z", Tool: "Bash", ToolUseID: "toolu_1", Input: `{"command":"go test ./..."}`},
					{ID: "u1:0", Kind: KindToolResult, At: "2026-10-26T10:00:02.000Z", ToolUseID: "toolu_1", IsError: true,
						Text:   "Exit code 2\nFAIL pkg\n",
						Result: &ToolFacts{ExitCode: intPtr(2), Stderr: "FAIL pkg\n"}},
					{ID: "a2:0", Kind: KindToolUse, At: "2026-10-26T10:00:03.000Z", Tool: "Bash", ToolUseID: "toolu_2", Input: `{"command":"bun run dev","run_in_background":true}`},
					{ID: "u2:0", Kind: KindToolResult, At: "2026-10-26T10:00:04.000Z", ToolUseID: "toolu_2", Text: "dev server up\n",
						Result: &ToolFacts{BackgroundID: "bg9watch"}},
					{ID: "a3:0", Kind: KindToolUse, At: "2026-10-26T10:00:05.000Z", Tool: "Edit", ToolUseID: "toolu_3", Input: `{"file_path":"/abs/a.go","old_string":"old","new_string":"new"}`},
					{ID: "u3:0", Kind: KindToolResult, At: "2026-10-26T10:00:06.000Z", ToolUseID: "toolu_3", Text: "updated",
						Result: &ToolFacts{
							FilePath: "/abs/a.go",
							Hunks:    []Hunk{{OldStart: 10, OldLines: 3, NewStart: 10, NewLines: 4, Lines: []string{" keep", "-old", "+new", "+added", " keep"}}},
						}},
					{ID: "a4:0", Kind: KindToolUse, At: "2026-10-26T10:00:07.000Z", Tool: "AskUserQuestion", ToolUseID: "toolu_4",
						Input: `{"questions":[{"question":"Which port?","header":"Port","options":[{"label":"A"}]}]}`},
					{ID: "u4:0", Kind: KindToolResult, At: "2026-10-26T10:00:08.000Z", ToolUseID: "toolu_4", Text: "answered",
						Result: &ToolFacts{Answers: []Answer{{Header: "Port", Question: "Which port?", Answer: "A"}}}},
				})
			})
		})
	})
}
