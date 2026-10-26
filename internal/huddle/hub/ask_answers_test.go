package hub

import (
	"errors"
	"testing"
)

// TestCheckAnswersReadsAQuestionOnly proves the shape the decide route accepts for an
// AskUserQuestion allow: every question the input carries needs one answer keyed by its exact
// text, a plain tool takes no answers at all, and an input the route cannot read as questions
// sends the owner to the terminal.
func TestCheckAnswersReadsAQuestionOnly(t *testing.T) {
	one := `{"questions":[{"question":"Which database?","options":[{"label":"Postgres"},{"label":"SQLite"}]}]}`
	two := `{"questions":[{"question":"Which database?","options":[{"label":"Postgres"},{"label":"SQLite"}]},{"question":"Seed it?","options":[{"label":"Yes"},{"label":"No"}],"multiSelect":true}]}`
	tests := []struct {
		name    string
		tool    string
		input   string
		answers map[string]string
		want    string // the refusal; "" means the check passes
	}{
		{"one question answered", "AskUserQuestion", one, map[string]string{"Which database?": "Postgres"}, ""},
		{"two questions answered", "AskUserQuestion", two, map[string]string{"Which database?": "SQLite", "Seed it?": "Yes"}, ""},
		{"multi-select joins labels with a comma", "AskUserQuestion", two,
			map[string]string{"Which database?": "Postgres", "Seed it?": "Yes,No"}, ""},
		{"a plain tool takes no answers", "Bash", `{"command":"npm test"}`, map[string]string{"x": "y"},
			"answers: only a question takes answers"},
		{"no answers names none", "AskUserQuestion", one, nil, "answers: name an answer for each question"},
		{"an empty map is no answers", "AskUserQuestion", one, map[string]string{},
			"answers: name an answer for each question"},
		{"an unreadable question goes to the terminal", "AskUserQuestion", `{"command":"ls"}`,
			map[string]string{"x": "y"}, "answers: the question cannot be read; answer it in the terminal"},
		{"an unknown key names itself", "AskUserQuestion", one, map[string]string{"Which server?": "Apache"},
			`answers: no question reads "Which server?"`},
		{"a question with no answer names it", "AskUserQuestion", two, map[string]string{"Which database?": "Postgres"},
			`answers: no answer for "Seed it?"`},
		{"an empty answer is no answer", "AskUserQuestion", one, map[string]string{"Which database?": ""},
			`answers: no answer for "Which database?"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := CheckAnswers(test.tool, test.input, test.answers)
			if test.want == "" {
				if err != nil {
					t.Fatalf("CheckAnswers = %v, want it to pass", err)
				}
				return
			}
			var answers *AnswersError
			if !errors.As(err, &answers) {
				t.Fatalf("CheckAnswers = %v, want an *AnswersError", err)
			}
			if answers.Msg != test.want {
				t.Errorf("the refusal = %q, want %q", answers.Msg, test.want)
			}
		})
	}
}

// TestAnswersFloorGuardsWhatTheAnswersCarry proves the floor reads the answers as text: a
// question or an answer naming a .ssh, .gnupg or secrets path refuses the remote allow, the
// same floor the input itself answers to.
func TestAnswersFloorGuardsWhatTheAnswersCarry(t *testing.T) {
	tests := []struct {
		name    string
		answers map[string]string
		want    string
		wantOK  bool
	}{
		{"plain options pass", map[string]string{"Which database?": "Postgres"}, "", true},
		{"no answers pass", nil, "", true},
		{"an answer naming a secrets path is refused", map[string]string{"Which file?": ".agents/secrets/keys.env"},
			floorPath, false},
		{"a question naming a .ssh path is refused", map[string]string{"read ~/.ssh/id_ed25519?": "yes"},
			floorPath, false},
		{"an answer naming a .gnupg path is refused", map[string]string{"Which keyring?": "~/.gnupg/pubring.kbx"},
			floorPath, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reason, ok := AnswersFloor(test.answers)
			if ok != test.wantOK || reason != test.want {
				t.Errorf("AnswersFloor = (%q, %v), want (%q, %v)", reason, ok, test.want, test.wantOK)
			}
		})
	}
}
