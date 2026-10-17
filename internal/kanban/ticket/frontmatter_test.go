package ticket

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

const longSummary = "Ports the router front process so a dead worker never strands a request in flight: it forks the worker, restarts it when it dies, retries on a fresh worker and serves passthrough while none is up. Done when all front cases pass against a fake worker."

// card wraps front matter in the ticket file shape.
func card(fm string) string {
	return "---\n" + fm + "\n---\n\n# C-1\n\nBody text.\n"
}

func TestSplit(t *testing.T) {
	t.Run("front matter and body", func(t *testing.T) {
		src := "---\nid: A-1\ntitle: \"a\"\n---\n\n# A-1\n\n## Description\ntext\n"
		front, body, ok := Split(src)
		if !ok {
			t.Fatal("ok = false, want true")
		}
		if front != "id: A-1\ntitle: \"a\"" {
			t.Errorf("front = %q, want %q", front, "id: A-1\ntitle: \"a\"")
		}
		if want := "\n# A-1\n\n## Description\ntext\n"; body != want {
			t.Errorf("body = %q, want %q", body, want)
		}
	})
	t.Run("no front matter", func(t *testing.T) {
		src := "# C-1\n\nBody text.\n"
		front, body, ok := Split(src)
		if ok || front != "" || body != src {
			t.Errorf("Split(...) = %q, %q, %v; want %q, %q, false", front, body, ok, "", src)
		}
	})
	t.Run("unclosed front matter", func(t *testing.T) {
		front, body, ok := Split("---\nid: A-1\ntitle: \"a\"\n")
		if !ok || front != "id: A-1\ntitle: \"a\"\n" || body != "" {
			t.Errorf("Split(...) = %q, %q, %v", front, body, ok)
		}
	})
	t.Run("closing marker may carry trailing spaces", func(t *testing.T) {
		front, _, ok := Split("---\nid: A-1\n---  \n\n# A-1\n")
		if !ok || front != "id: A-1" {
			t.Errorf("Split(...) = %q, %v, want \"id: A-1\", true", front, ok)
		}
	})
	t.Run("crlf line ends read as lf", func(t *testing.T) {
		front, body, ok := Split("---\r\nid: A-1\r\n---\r\n\r\n# A-1\r\n")
		if !ok || front != "id: A-1" || body != "\n# A-1\n" {
			t.Errorf("Split(...) = %q, %q, %v", front, body, ok)
		}
	})
	t.Run("a later marker is not an opening one", func(t *testing.T) {
		_, body, ok := Split("prose\n---\nid: X\n")
		if ok || body != "prose\n---\nid: X\n" {
			t.Errorf("Split(...) body = %q, ok = %v", body, ok)
		}
	})
}

func TestField(t *testing.T) {
	cases := []struct {
		name   string
		front  string
		key    string
		want   string
		wantOK bool
	}{
		{"plain scalar", "id: A-1", "id", "A-1", true},
		{"double-quoted scalar", "title: \"a b\"", "title", "a b", true},
		{"single-quoted scalar", "title: 'a b'", "title", "a b", true},
		{"empty value", "id: A-1\nclaimed_by:", "claimed_by", "", true},
		{"absent key", "id: A-1", "title", "", false},
		{"the key must match exactly", "ids: x", "id", "", false},
		{"a value with a colon", "branch: fix: a", "branch", "fix: a", true},
		{"test_gen_board_renders_v2_cards: a folded block joins its lines", "summary: >-\n  one two\n  three four", "summary", "one two three four", true},
		{"a literal block reads the same", "summary: |-\n  one two\n  three four", "summary", "one two three four", true},
		{"a folded block keeps paragraphs", "summary: >-\n  first paragraph\n  second line\n\n  second paragraph", "summary", "first paragraph second line\nsecond paragraph", true},
		{"a multi-line quoted scalar", "title: \"first\n  second\"", "title", "first\nsecond", true},
		{"first occurrence wins", "id: A-1\nid: B-2", "id", "A-1", true},
		{"test_body_lines_never_shadow_front_matter: only the front matter is searched", "id: A-1", "claimed_by", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Field(tc.front, tc.key)
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("Field(%q, %q) = %q, %v; want %q, %v", tc.front, tc.key, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestSetField(t *testing.T) {
	base := card("id: C-1\ntitle: \"old\"\ntype: docs\ncluster: c")
	cases := []struct {
		name       string
		src        string
		key, value string
		wantBlock  string
		wantValue  string
		wantErr    bool
	}{
		{"test_pull_id_sets_branch_and_worktree: plain scalars stay bare", base, "branch", "port/RTR-FRONT", "branch: port/RTR-FRONT", "port/RTR-FRONT", false},
		{"test_pull_id_sets_branch_and_worktree: a worktree stays bare", base, "worktree", "scratchpad/wt/RTR-FRONT", "worktree: scratchpad/wt/RTR-FRONT", "scratchpad/wt/RTR-FRONT", false},
		{"an actor stays bare", base, "claimed_by", "w1", "claimed_by: w1", "w1", false},
		{"a column stays bare", base, "blocked_from", "in-progress", "blocked_from: in-progress", "in-progress", false},
		{"test_set_writes_descriptive_fields: a title is double-quoted", base, "title", "new title", "title: \"new title\"", "new title", false},
		{"a cluster stays bare with two words", base, "cluster", "go port", "cluster: go port", "go port", false},
		{"priority stays bare", base, "priority", "P0", "priority: P0", "P0", false},
		{"unblock_when is quoted", base, "unblock_when", "green CI", "unblock_when: \"green CI\"", "green CI", false},
		{"test_block_records_new_fields: the block reason is quoted", base, "blocked_reason", "the CI is red", "blocked_reason: \"the CI is red\"", "the CI is red", false},
		{"a single-line summary is quoted", base, "summary", longSummary, "summary: \"" + longSummary + "\"", longSummary, false},
		{"whitespace collapses in quoted values", base, "title", "new   title", "title: \"new title\"", "new title", false},
		{"test_set_quotes_safely: a double quote flips to single quotes", base, "title", "say \"hi\" there", "title: 'say \"hi\" there'", "say \"hi\" there", false},
		{"a lone single quote keeps double quotes", base, "title", "it's here", "title: \"it's here\"", "it's here", false},
		{"test_backslashes_are_single_quoted: a backslash flips to single quotes", base, "result", `copied C:\Users\x to the share`, `result: 'copied C:\Users\x to the share'`, `copied C:\Users\x to the share`, false},
		{"test_non_utf8_card_stays_put: a scalar with a colon is quoted", base, "branch", "fix: a", "branch: \"fix: a\"", "fix: a", false},
		{"test_stamped_fields: a reserved word is quoted", base, "branch", "true", "branch: \"true\"", "true", false},
		{"test_stamped_fields: a reserved null is quoted", base, "repos", "null", "repos: \"null\"", "null", false},
		{"a unicode value round-trips", base, "title", "café résumé — naïve", "title: \"café résumé — naïve\"", "café résumé — naïve", false},
		{"test_set_multiline_value_written_folded: a multi-line value folds", base, "summary", "one line of what changes here\ntwo lines of why it matters to us\nthree lines of what done looks like", "summary: >-\n  one line of what changes here\n  two lines of why it matters to us\n  three lines of what done looks like", "one line of what changes here two lines of why it matters to us three lines of what done looks like", false},
		{"a blank line in a folded value becomes one newline", base, "summary", "one\n\ntwo", "summary: >-\n  one\n  \n  two", "one\ntwo", false},
		{"test_list_items_quote_safely: mixed quote kinds are refused", base, "title", "it's \"both\"", "", "", true},
		{"a backslash with a single quote is refused", base, "title", `it's C:\x`, "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SetField(tc.src, tc.key, tc.value)
			if tc.wantErr {
				if !errors.Is(err, ErrUnquotable) {
					t.Fatalf("SetField(...) err = %v, want ErrUnquotable", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("SetField(...) err = %v", err)
			}
			if !strings.Contains(got, tc.wantBlock) {
				t.Errorf("SetField(...) = %q, want it to contain %q", got, tc.wantBlock)
			}
			front, _, _ := Split(got)
			value, ok := Field(front, tc.key)
			if !ok || value != tc.wantValue {
				t.Errorf("Field(...) = %q, %v; want %q, true", value, ok, tc.wantValue)
			}
		})
	}

	t.Run("test_stamped_fields: a new key is appended after the authored ones", func(t *testing.T) {
		got, err := SetField(base, "claimed_by", "w1")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, "cluster: c\nclaimed_by: w1") {
			t.Errorf("SetField(...) = %q, want the stamped field after the authored ones", got)
		}
	})
	t.Run("an old folded value is replaced whole", func(t *testing.T) {
		src := card("id: C-1\nsummary: >-\n  old first\n  old second\ntitle: \"a\"")
		got, err := SetField(src, "summary", "fresh")
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{`summary: "fresh"`, `title: "a"`} {
			if !strings.Contains(got, want) {
				t.Errorf("SetField(...) = %q, want it to contain %q", got, want)
			}
		}
		if strings.Contains(got, "old first") || strings.Contains(got, "old second") {
			t.Errorf("SetField(...) = %q, want the old folded lines gone", got)
		}
	})
	t.Run("a file with no front matter gains one", func(t *testing.T) {
		got, err := SetField("# C-1\n\nBody.\n", "cluster", "c")
		if err != nil {
			t.Fatal(err)
		}
		if want := "---\ncluster: c\n---\n# C-1\n\nBody.\n"; got != want {
			t.Errorf("SetField(...) = %q, want %q", got, want)
		}
	})
	t.Run("crlf input is written back with lf", func(t *testing.T) {
		got, err := SetField("---\r\nid: C-1\r\ntitle: \"a\"\r\n---\r\n", "cluster", "c")
		if err != nil {
			t.Fatal(err)
		}
		if strings.ContainsRune(got, '\r') {
			t.Errorf("SetField(...) = %q, want no carriage returns", got)
		}
		if !strings.Contains(got, "cluster: c") {
			t.Errorf("SetField(...) = %q, want the new field", got)
		}
	})
}

func TestClearFields(t *testing.T) {
	t.Run("test_unblock_clears_new_fields_keeps_from", func(t *testing.T) {
		src := card("id: C-1\nblocked_reason: \"waiting\"\nblocked_from: in-progress\nblocked_at: 2026-10-13T11:20:00+0300\nblocked_by: lead\nblocked_on: [WIN-1]\nunblock_when: \"legal says yes\"")
		got := ClearFields(src, "blocked_reason", "blocked_at", "blocked_by", "blocked_on", "unblock_when")
		for _, key := range []string{"blocked_reason", "blocked_at", "blocked_by", "blocked_on", "unblock_when"} {
			if strings.Contains(got, key+":") {
				t.Errorf("ClearFields(...) = %q, want %s gone", got, key)
			}
		}
		if !strings.Contains(got, "blocked_from: in-progress") {
			t.Errorf("ClearFields(...) = %q, want blocked_from kept", got)
		}
	})
	t.Run("a folded value is cleared whole", func(t *testing.T) {
		src := card("id: C-1\nsummary: >-\n  one\n  two\ntitle: \"a\"")
		got := ClearFields(src, "summary")
		if strings.Contains(got, "summary:") || strings.Contains(got, "  one") || strings.Contains(got, "  two") {
			t.Errorf("ClearFields(...) = %q, want the folded value gone", got)
		}
		if !strings.Contains(got, `title: "a"`) {
			t.Errorf("ClearFields(...) = %q, want the other fields kept", got)
		}
	})
	t.Run("text without front matter is unchanged", func(t *testing.T) {
		src := "# C-1\n\nBody.\n"
		if got := ClearFields(src, "cluster"); got != src {
			t.Errorf("ClearFields(...) = %q, want %q", got, src)
		}
	})
}

func TestParseList(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{"test_set_writes_descriptive_fields: a bracketed id list", "[A-1, B-2]", []string{"A-1", "B-2"}},
		{"test_block_records_new_fields: quoted items lose their quotes", "[WIN-1, \"a push to GitHub\"]", []string{"WIN-1", "a push to GitHub"}},
		{"test_stamped_fields: quoted numbers read back as text", "[\"2026\", A-1, \"1.5\"]", []string{"2026", "A-1", "1.5"}},
		{"test_set_block_fields: a bare comma-separated string", "INFRA-7, the vendor", []string{"INFRA-7", "the vendor"}},
		{"test_every_past_holder: a holder list", "[w2, w3, w4]", []string{"w2", "w3", "w4"}},
		{"test_list_items_quote_safely: an item may hold a double quote", "['the \"vendor\" call', INFRA-7]", []string{`the "vendor" call`, "INFRA-7"}},
		{"empty raw", "", nil},
		{"empty brackets", "[]", nil},
		{"empty items drop out", "[a, , b]", []string{"a", "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseList(tc.raw); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParseList(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestFormatList(t *testing.T) {
	cases := []struct {
		name  string
		items []string
		want  string
	}{
		{"test_set_writes_descriptive_fields: plain ids stay bare", []string{"A-1", "B-2"}, "[A-1, B-2]"},
		{"test_block_records_new_fields: text is double-quoted", []string{"WIN-1", "a push to GitHub"}, `[WIN-1, "a push to GitHub"]`},
		{"test_stamped_fields: numbers are quoted, ids stay bare", []string{"2026", "A-1", "1.5"}, `["2026", A-1, "1.5"]`},
		{"test_every_past_holder: actors stay bare", []string{"w2", "w3", "w4"}, "[w2, w3, w4]"},
		{"test_list_items_quote_safely: an item with a double quote is single-quoted", []string{`the "vendor" call`, "INFRA-7"}, `['the "vendor" call', INFRA-7]`},
		{"a reserved word is quoted", []string{"true"}, `["true"]`},
		{"two words are quoted", []string{"two words"}, `["two words"]`},
		{"empty list", nil, "[]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FormatList(tc.items); got != tc.want {
				t.Errorf("FormatList(%q) = %q, want %q", tc.items, got, tc.want)
			}
		})
	}
}

func TestAppendNote(t *testing.T) {
	t.Run("test_pass_through_after_unblock_from_testing: notes append with a pipe", func(t *testing.T) {
		src := card("id: C-1\nreview_note: \"first verdict\"")
		got, err := AppendNote(src, "review_note", "pass-through: already reviewed")
		if err != nil {
			t.Fatal(err)
		}
		want := `review_note: "first verdict | pass-through: already reviewed"`
		if !strings.Contains(got, want) {
			t.Errorf("AppendNote(...) = %q, want it to contain %q", got, want)
		}
	})
	t.Run("test_appended_notes_never_mix_quote_kinds: the earlier quote kind is kept", func(t *testing.T) {
		src := card("id: C-1\n" + `review_note: 'check the "token" table'`)
		got, err := AppendNote(src, "review_note", "it is missing the index")
		if err != nil {
			t.Fatal(err)
		}
		want := `review_note: 'check the "token" table | it is missing the index'`
		if !strings.Contains(got, want) {
			t.Errorf("AppendNote(...) = %q, want it to contain %q", got, want)
		}
	})
	t.Run("a join that would mix quote kinds is refused", func(t *testing.T) {
		src := card("id: C-1\n" + `review_note: 'check the "token" table'`)
		if _, err := AppendNote(src, "review_note", "it's missing"); !errors.Is(err, ErrUnquotable) {
			t.Errorf("AppendNote(...) err = %v, want ErrUnquotable", err)
		}
	})
	t.Run("an empty note changes nothing", func(t *testing.T) {
		src := card("id: C-1\nreview_note: \"first verdict\"")
		got, err := AppendNote(src, "review_note", "")
		if err != nil {
			t.Fatal(err)
		}
		if got != src {
			t.Errorf("AppendNote(...) = %q, want %q", got, src)
		}
	})
	t.Run("a first note writes alone", func(t *testing.T) {
		got, err := AppendNote(card("id: C-1"), "test_note", "pytest exit 0")
		if err != nil {
			t.Fatal(err)
		}
		if want := `test_note: "pytest exit 0"`; !strings.Contains(got, want) {
			t.Errorf("AppendNote(...) = %q, want it to contain %q", got, want)
		}
	})
}

func TestWrapFolded(t *testing.T) {
	t.Run("lines wrap at 96 columns", func(t *testing.T) {
		text := strings.Repeat("word ", 40)
		got := WrapFolded("summary", text)
		lines := strings.Split(got, "\n")
		if lines[0] != "summary: >-" {
			t.Fatalf("WrapFolded(...) = %q, want a folded head", got)
		}
		var words []string
		for _, line := range lines[1:] {
			if !strings.HasPrefix(line, "  ") {
				t.Fatalf("WrapFolded(...) line %q is not indented", line)
			}
			if len(line)-2 > 96 {
				t.Errorf("WrapFolded(...) line is %d columns wide, want at most 96", len(line)-2)
			}
			words = append(words, strings.Fields(line)...)
		}
		if strings.Join(words, " ") != strings.Join(strings.Fields(text), " ") {
			t.Errorf("WrapFolded(...) lost words: %q", words)
		}
	})
	t.Run("short text stays one line", func(t *testing.T) {
		if got, want := WrapFolded("summary", "one two three"), "summary: >-\n  one two three"; got != want {
			t.Errorf("WrapFolded(...) = %q, want %q", got, want)
		}
	})
	t.Run("empty text folds to one empty line", func(t *testing.T) {
		if got, want := WrapFolded("summary", ""), "summary: >-\n  "; got != want {
			t.Errorf("WrapFolded(...) = %q, want %q", got, want)
		}
	})
}
