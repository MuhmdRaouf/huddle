package agents

// Feature: the panel names each subagent's model. One pass over the folder decides every file:
// an empty folder gets one file per family, a newer model retires the managed file it replaces,
// a user's own file with the same name wins and huddle writes no copy, a second pass over files
// huddle already wrote does nothing, and a file without the marker is never written nor removed.

import (
	"slices"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

// userOpus is a file the user wrote themselves: the name huddle would use, no marker.
var userOpus = Existing{
	Name: "opus-5.5.md",
	Body: []byte("---\nname: opus-5.5\ndescription: my own opus agent\n---\n\nMine, leave it alone.\n"),
}

// reviewerFile is one user file no family ever names.
var reviewerFile = Existing{Name: "reviewer.md", Body: []byte("Review the diff and say what you saw.\n")}

// huddles renders huddle's own file for each model.
func huddles(models []Model) []Existing {
	files := make([]Existing, 0, len(models))
	for _, model := range models {
		files = append(files, Existing{Name: model.FileName(), Body: Render(model)})
	}
	return files
}

// opusSixTable is the pricing table's newest models with Opus 6 in opus's place.
func opusSixTable(t *testing.T) []Model {
	t.Helper()
	models := Newest(ClaudeIDs())
	at := slices.IndexFunc(models, func(m Model) bool { return m.Family == "opus" })
	if at < 0 {
		t.Fatal("no opus among the pricing table's newest models")
	}
	models[at] = Model{Family: "opus", Version: []int{6}, ID: "claude-opus-6"}
	return models
}

// writeNames lists the file names a plan writes.
func writeNames(plan Plan) []string {
	names := make([]string, 0, len(plan.Write))
	for _, file := range plan.Write {
		names = append(names, file.Name)
	}
	return names
}

// untouched fails the step when name shows up on either side of the plan.
func untouched(t *testing.T, plan Plan, name string) {
	t.Helper()
	if slices.Contains(writeNames(plan), name) {
		t.Errorf("%s is in Write; a user's file is never written", name)
	}
	if slices.Contains(plan.Remove, name) {
		t.Errorf("%s is in Remove; a user's file is never removed", name)
	}
}

func TestThePanelNamesEachSubagentsModel(t *testing.T) {
	bdd.Feature(t, "The panel names each subagent's model", func(f *bdd.F) {
		f.Scenario("an empty folder starts from four writes and removes nothing", func(s *bdd.S) {
			var plan Plan
			s.Given("the pricing table's newest models and no files in the folder", func(t *testing.T) {
				plan = Decide(Newest(ClaudeIDs()), nil)
			})
			s.Then("the plan writes the four family files, sorted by name", func(t *testing.T) {
				got := writeNames(plan)
				want := []string{"fable-5.1.md", "haiku-5.5.md", "opus-5.5.md", "sonnet-5.5.md"}
				if !slices.Equal(got, want) {
					t.Errorf("Write = %v, want %v", got, want)
				}
			})
			s.And("nothing is removed", func(t *testing.T) {
				if len(plan.Remove) != 0 {
					t.Errorf("Remove = %v, want none", plan.Remove)
				}
			})
			s.And("every written file is managed", func(t *testing.T) {
				for _, file := range plan.Write {
					if !Managed(file.Body) {
						t.Errorf("%s is not marked as huddle's", file.Name)
					}
				}
			})
		})

		f.Scenario("a table where Opus 6 is newest retires huddle's opus-5.5.md", func(s *bdd.S) {
			var plan Plan
			s.Given("the folder holds the files huddle wrote for the current table, plus a user's reviewer.md", func(t *testing.T) {
				huddle := huddles(Newest(ClaudeIDs()))
				huddle = append(huddle, reviewerFile)
				plan = Decide(opusSixTable(t), huddle)
			})
			s.When("the pass runs", func(t *testing.T) {})
			s.Then("the plan writes opus-6.md and removes opus-5.5.md", func(t *testing.T) {
				if got := writeNames(plan); !slices.Equal(got, []string{"opus-6.md"}) {
					t.Errorf("Write = %v, want [opus-6.md]", got)
				}
				if got := plan.Remove; !slices.Equal(got, []string{"opus-5.5.md"}) {
					t.Errorf("Remove = %v, want [opus-5.5.md]", got)
				}
			})
			s.And("reviewer.md is untouched", func(t *testing.T) {
				untouched(t, plan, "reviewer.md")
			})
		})

		f.Scenario("the user's own opus-5.5.md wins", func(s *bdd.S) {
			var plan Plan
			s.Given("the folder holds the user's own opus-5.5.md, plus a user's reviewer.md", func(t *testing.T) {
				plan = Decide(Newest(ClaudeIDs()), []Existing{userOpus, reviewerFile})
			})
			s.When("the pass runs", func(t *testing.T) {})
			s.Then("huddle writes the other three families and no copy of opus-5.5.md", func(t *testing.T) {
				got := writeNames(plan)
				want := []string{"fable-5.1.md", "haiku-5.5.md", "sonnet-5.5.md"}
				if !slices.Equal(got, want) {
					t.Errorf("Write = %v, want %v", got, want)
				}
			})
			s.And("nothing is removed", func(t *testing.T) {
				if len(plan.Remove) != 0 {
					t.Errorf("Remove = %v, want none", plan.Remove)
				}
			})
			s.And("opus-5.5.md and reviewer.md are untouched", func(t *testing.T) {
				untouched(t, plan, "opus-5.5.md")
				untouched(t, plan, "reviewer.md")
			})
		})

		f.Scenario("a second pass over files huddle already wrote does nothing", func(s *bdd.S) {
			var plan Plan
			s.Given("the folder holds the four rendered files, plus a user's reviewer.md", func(t *testing.T) {
				huddle := huddles(Newest(ClaudeIDs()))
				huddle = append(huddle, reviewerFile)
				plan = Decide(Newest(ClaudeIDs()), huddle)
			})
			s.When("the pass runs", func(t *testing.T) {})
			s.Then("the plan is empty", func(t *testing.T) {
				if len(plan.Write) != 0 || len(plan.Remove) != 0 {
					t.Errorf("plan = write %v remove %v, want none", writeNames(plan), plan.Remove)
				}
			})
			s.And("reviewer.md is untouched", func(t *testing.T) {
				untouched(t, plan, "reviewer.md")
			})
		})

		f.Scenario("a user's unmarked reviewer.md is never written nor removed", func(s *bdd.S) {
			var plan Plan
			s.Given("the folder holds only a user's reviewer.md", func(t *testing.T) {
				plan = Decide(Newest(ClaudeIDs()), []Existing{reviewerFile})
			})
			s.When("the pass runs", func(t *testing.T) {})
			s.Then("the plan writes the four family files", func(t *testing.T) {
				got := writeNames(plan)
				want := []string{"fable-5.1.md", "haiku-5.5.md", "opus-5.5.md", "sonnet-5.5.md"}
				if !slices.Equal(got, want) {
					t.Errorf("Write = %v, want %v", got, want)
				}
			})
			s.And("reviewer.md is on neither side of the plan", func(t *testing.T) {
				untouched(t, plan, "reviewer.md")
			})
		})
	})
}
