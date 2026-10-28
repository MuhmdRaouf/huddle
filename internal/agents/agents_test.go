package agents

import (
	"bytes"
	"slices"
	"strings"
	"testing"
)

// equalModels compares two model lists family, version and id at a time.
func equalModels(a, b []Model) bool {
	return slices.EqualFunc(a, b, func(x, y Model) bool {
		return x.Family == y.Family && x.ID == y.ID && slices.Equal(x.Version, y.Version)
	})
}

// Newest on the pricing table's own ids picks one model per family, families in order.
func TestNewestOfClaudeIDsPicksEachFamilyNewest(t *testing.T) {
	got := Newest(ClaudeIDs())
	want := []Model{
		{Family: "fable", Version: []int{5, 1}, ID: "claude-fable-5-1"},
		{Family: "opus", Version: []int{5, 5}, ID: "claude-opus-5-5"},
		{Family: "sonnet", Version: []int{5, 5}, ID: "claude-sonnet-5-5"},
		{Family: "haiku", Version: []int{5, 5}, ID: "claude-haiku-5-5"},
	}
	if !equalModels(got, want) {
		t.Errorf("Newest(ClaudeIDs()) = %+v, want %+v", got, want)
	}
}

// Newest compares versions as numbers and counts only ids shaped claude-<family>-<major>[-<minor>].
func TestNewestComparesVersionsAsNumbersAndIgnoresOtherIds(t *testing.T) {
	cases := []struct {
		name string
		ids  []string
		want []Model
	}{
		{
			name: "ten beats five",
			ids:  []string{"claude-opus-10-1", "claude-opus-5-5"},
			want: []Model{{Family: "opus", Version: []int{10, 1}, ID: "claude-opus-10-1"}},
		},
		{
			name: "a one-part major beats an older major-minor",
			ids:  []string{"claude-opus-6", "claude-opus-5-5"},
			want: []Model{{Family: "opus", Version: []int{6}, ID: "claude-opus-6"}},
		},
		{
			name: "the minor decides within a major",
			ids:  []string{"claude-opus-5", "claude-opus-5-5"},
			want: []Model{{Family: "opus", Version: []int{5, 5}, ID: "claude-opus-5-5"}},
		},
		{
			name: "a dated snapshot id is ignored",
			ids:  []string{"claude-haiku-4-5-20251001", "claude-haiku-5-5"},
			want: []Model{{Family: "haiku", Version: []int{5, 5}, ID: "claude-haiku-5-5"}},
		},
		{
			name: "an unknown family is ignored",
			ids:  []string{"claude-grok-9-1", "glm-5.3"},
			want: nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Newest(c.ids)
			if !equalModels(got, c.want) {
				t.Errorf("Newest(%q) = %+v, want %+v", c.ids, got, c.want)
			}
		})
	}
}

// Name joins the family and the version with dots; Title capitalises the family; FileName appends .md.
func TestModelNameTitleAndFileName(t *testing.T) {
	t.Run("a two-part version names opus-5.5", func(t *testing.T) {
		m := Model{Family: "opus", Version: []int{5, 5}, ID: "claude-opus-5-5"}
		if got, want := m.Name(), "opus-5.5"; got != want {
			t.Errorf("Name() = %q, want %q", got, want)
		}
		if got, want := m.Title(), "Claude Opus 5.5"; got != want {
			t.Errorf("Title() = %q, want %q", got, want)
		}
		if got, want := m.FileName(), "opus-5.5.md"; got != want {
			t.Errorf("FileName() = %q, want %q", got, want)
		}
	})
	t.Run("a one-part version names sonnet-6", func(t *testing.T) {
		m := Model{Family: "sonnet", Version: []int{6}, ID: "claude-sonnet-6"}
		if got, want := m.Name(), "sonnet-6"; got != want {
			t.Errorf("Name() = %q, want %q", got, want)
		}
		if got, want := m.Title(), "Claude Sonnet 6"; got != want {
			t.Errorf("Title() = %q, want %q", got, want)
		}
		if got, want := m.FileName(), "sonnet-6.md"; got != want {
			t.Errorf("FileName() = %q, want %q", got, want)
		}
	})
}

// Render writes front matter with the name, the title, the model id and the marker, then the
// one-line body: no effort, no tools, LF ends, a final newline.
func TestRenderWritesFrontMatterAndOneLineBody(t *testing.T) {
	got := Render(Model{Family: "opus", Version: []int{5, 5}, ID: "claude-opus-5-5"})
	want := []byte("---\n" +
		"name: opus-5.5\n" +
		"description: Claude Opus 5.5\n" +
		"model: claude-opus-5-5\n" +
		"managed-by: huddle\n" +
		"---\n" +
		"\n" +
		"Do the task in your prompt and report what you did.\n")
	if !bytes.Equal(got, want) {
		t.Errorf("Render(opus 5.5) =\n%q\nwant\n%q", got, want)
	}
	for _, line := range strings.Split(string(got), "\n") {
		if strings.HasPrefix(line, "effort:") || strings.HasPrefix(line, "tools:") {
			t.Errorf("rendered file has a %q line; the files carry the model only", line)
		}
	}
}

// Managed reads the marker from the front matter only.
func TestManagedReadsTheMarkerFromTheFrontMatter(t *testing.T) {
	rendered := Render(Model{Family: "opus", Version: []int{5, 5}, ID: "claude-opus-5-5"})
	if !Managed(rendered) {
		t.Error("a rendered file is managed")
	}
	without := bytes.Replace(rendered, []byte(Marker+"\n"), nil, 1)
	if Managed(without) {
		t.Error("the same file without the marker line is not managed")
	}
	bodyOnly := []byte("---\nname: mine\n---\n\n" + Marker + "\n")
	if Managed(bodyOnly) {
		t.Error("a marker in the body does not manage a file")
	}
}
