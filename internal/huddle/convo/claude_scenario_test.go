package convo

// Feature: the transcript reader shows Claude's thinking between its replies: a thinking line
// comes out as one thinking item in conversation order, carrying the line's thinking duration.
// Feature: the reader serves a prompt's attached files and images as references beside it, and
// leaves out every attachment that is not a file.

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
