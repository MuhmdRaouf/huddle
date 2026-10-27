package hooks

// The permit hook's answers on an AskUserQuestion: an allow the owner picked on the phone runs
// the question without its dialog, and a bare allow is not enough for that — Claude Code runs
// the tool only when the decision also carries updatedInput, the call's own input with answers
// set. The hook prints it only when it can give a complete one: the owner's answers and a
// tool_input that is a JSON object to echo whole. A deny and every other tool print exactly
// what they always printed.

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

// permitAskInput is the stdin Claude Code sends for a one-question AskUserQuestion call.
const permitAskInput = `{"session_id":"s-ask","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"Which database?","header":"Database","options":[{"label":"SQLite","description":"embedded"}],"multiSelect":false}]}}`

// permitAskAnswered is the wait answer for an item the owner allowed with answers.
const permitAskAnswered = `{"id":7,"kind":"permission","state":"answered","decision":"allow","tool":"AskUserQuestion","answers":{"Which database?":"SQLite"}}`

// permitAskRuns runs the permit hook against a fake hub whose poll answers waitBody, with
// input on stdin, and answers what the hook printed.
func permitAskRuns(t *testing.T, input, waitBody string) string {
	t.Helper()
	fake := startPermitFake(t, 200, 200, waitBody)
	stdout, _, _ := runHook(t, "permit", input, map[string]string{"HUDDLE_URL": fake.server.URL})
	return stdout
}

// permitRawMember answers the named member of the JSON object raw, failing the test when the
// object does not carry it.
func permitRawMember(t *testing.T, raw []byte, where, member string) json.RawMessage {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("%s is not a JSON object: %v", where, err)
	}
	memberRaw, ok := object[member]
	if !ok {
		t.Fatalf("%s carries no %s member: %s", where, member, raw)
	}
	return memberRaw
}

func TestThePermitHookAnswersAnAskUserQuestionWithTheOwnersAnswers(t *testing.T) {
	bdd.Feature(t, "the permit hook answers an answered AskUserQuestion", func(f *bdd.F) {
		f.Scenario("the owner answered a one-question ask", func(s *bdd.S) {
			var stdout string
			s.Given("an answered allow that carries the picked answer", func(t *testing.T) {})
			s.When("the hook polls its decision", func(t *testing.T) {
				stdout = permitAskRuns(t, permitAskInput, permitAskAnswered)
			})
			s.Then("it prints the allow with the call's input and the answers", func(t *testing.T) {
				want := `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow","updatedInput":{"answers":{"Which database?":"SQLite"},"questions":[{"question":"Which database?","header":"Database","options":[{"label":"SQLite","description":"embedded"}],"multiSelect":false}]}}}}` + "\n"
				if stdout != want {
					t.Fatalf("the hook printed %q, want the allow with updatedInput", stdout)
				}
			})
			s.And("the printed questions are the stdin input's own bytes", func(t *testing.T) {
				var call struct {
					HookSpecificOutput struct {
						Decision struct {
							UpdatedInput json.RawMessage `json:"updatedInput"`
						} `json:"decision"`
					} `json:"hookSpecificOutput"`
				}
				if err := json.Unmarshal([]byte(stdout), &call); err != nil {
					t.Fatalf("the hook's answer is not decision JSON: %v", err)
				}
				stdinToolInput := permitRawMember(t, []byte(permitAskInput), "the stdin input", "tool_input")
				stdinQuestions := permitRawMember(t, stdinToolInput, "the stdin tool_input", "questions")
				updated := permitRawMember(t, call.HookSpecificOutput.Decision.UpdatedInput, "updatedInput", "questions")
				if !bytes.Equal(stdinQuestions, updated) {
					t.Fatalf("updatedInput.questions = %s, want the stdin's own %s", updated, stdinQuestions)
				}
			})
			s.And("the printed answers are the item's", func(t *testing.T) {
				var printed struct {
					HookSpecificOutput struct {
						Decision struct {
							UpdatedInput struct {
								Answers map[string]string `json:"answers"`
							} `json:"updatedInput"`
						} `json:"decision"`
					} `json:"hookSpecificOutput"`
				}
				if err := json.Unmarshal([]byte(stdout), &printed); err != nil {
					t.Fatalf("the hook's answer is not decision JSON: %v", err)
				}
				want := map[string]string{"Which database?": "SQLite"}
				if !reflect.DeepEqual(printed.HookSpecificOutput.Decision.UpdatedInput.Answers, want) {
					t.Fatalf("updatedInput.answers = %+v, want the item's %+v",
						printed.HookSpecificOutput.Decision.UpdatedInput.Answers, want)
				}
			})
		})

		f.Scenario("the owner answered two questions, one of them a multi-select", func(s *bdd.S) {
			var stdout string
			s.Given("a two-question call and an answered allow with both answers", func(t *testing.T) {})
			s.When("the hook polls its decision", func(t *testing.T) {
				input := `{"session_id":"s-ask","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"Which database?","header":"Database","options":[{"label":"SQLite","description":"embedded"}],"multiSelect":false},{"question":"Which flags?","header":"Flags","options":[{"label":"A","description":"first"},{"label":"B","description":"second"}],"multiSelect":true}]}}`
				answered := `{"id":7,"kind":"permission","state":"answered","decision":"allow","tool":"AskUserQuestion","answers":{"Which database?":"SQLite","Which flags?":"A,B"}}`
				stdout = permitAskRuns(t, input, answered)
			})
			s.Then("both answers are in the printed updatedInput", func(t *testing.T) {
				var printed struct {
					HookSpecificOutput struct {
						Decision struct {
							UpdatedInput struct {
								Answers map[string]string `json:"answers"`
							} `json:"updatedInput"`
						} `json:"decision"`
					} `json:"hookSpecificOutput"`
				}
				if err := json.Unmarshal([]byte(stdout), &printed); err != nil {
					t.Fatalf("the hook's answer is not decision JSON: %v", err)
				}
				want := map[string]string{"Which database?": "SQLite", "Which flags?": "A,B"}
				if got := printed.HookSpecificOutput.Decision.UpdatedInput.Answers; !reflect.DeepEqual(got, want) {
					t.Fatalf("updatedInput.answers = %+v, want both answers %+v", got, want)
				}
			})
		})

		f.Scenario("the call's input carries a key of its own", func(s *bdd.S) {
			var stdout string
			var stdinMetadata json.RawMessage
			s.Given("an input with questions and a metadata object beside them", func(t *testing.T) {})
			s.When("the hook polls its decision", func(t *testing.T) {
				input := `{"session_id":"s-ask","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"Which database?","header":"Database","options":[{"label":"SQLite","description":"embedded"}],"multiSelect":false}],"metadata":{"source":"x"}}}`
				toolInput := permitRawMember(t, []byte(input), "the stdin input", "tool_input")
				stdinMetadata = permitRawMember(t, toolInput, "the stdin tool_input", "metadata")
				stdout = permitAskRuns(t, input, permitAskAnswered)
			})
			s.Then("the printed input keeps the key unchanged and adds only answers", func(t *testing.T) {
				var printed struct {
					HookSpecificOutput struct {
						Decision struct {
							UpdatedInput json.RawMessage `json:"updatedInput"`
						} `json:"decision"`
					} `json:"hookSpecificOutput"`
				}
				if err := json.Unmarshal([]byte(stdout), &printed); err != nil {
					t.Fatalf("the hook's answer is not decision JSON: %v", err)
				}
				var updated map[string]json.RawMessage
				if err := json.Unmarshal(printed.HookSpecificOutput.Decision.UpdatedInput, &updated); err != nil {
					t.Fatalf("updatedInput is not a JSON object: %v", err)
				}
				if !bytes.Equal(permitRawMember(t, printed.HookSpecificOutput.Decision.UpdatedInput, "updatedInput", "metadata"), stdinMetadata) {
					t.Fatalf("updatedInput.metadata = %s, want the stdin's own %s",
						updated["metadata"], stdinMetadata)
				}
				if len(updated) != 3 {
					t.Fatalf("updatedInput carries %d members (%v), want questions, metadata and answers", len(updated), updated)
				}
			})
		})

		f.Scenario("the answered allow carries no answers", func(s *bdd.S) {
			var stdout string
			s.Given("an answered allow on a question with nothing picked stored", func(t *testing.T) {})
			s.When("the hook polls its decision", func(t *testing.T) {
				open := `{"id":7,"kind":"permission","state":"answered","decision":"allow","tool":"AskUserQuestion"}`
				stdout = permitAskRuns(t, permitAskInput, open)
			})
			s.Then("it prints nothing, so the terminal dialog stands", func(t *testing.T) {
				if stdout != "" {
					t.Fatalf("the hook printed %q, want silence: never a bare allow for a question", stdout)
				}
			})
		})

		f.Scenario("the call's input is not a JSON object", func(s *bdd.S) {
			var stdout string
			s.Given("an answered allow and a string where the input object should be", func(t *testing.T) {})
			s.When("the hook polls its decision", func(t *testing.T) {
				input := `{"session_id":"s-ask","tool_name":"AskUserQuestion","tool_input":"which database?"}`
				stdout = permitAskRuns(t, input, permitAskAnswered)
			})
			s.Then("it prints nothing, so the terminal dialog stands", func(t *testing.T) {
				if stdout != "" {
					t.Fatalf("the hook printed %q, want silence: an input it cannot echo whole is not allowed remotely", stdout)
				}
			})
		})

		f.Scenario("the owner denied the question", func(s *bdd.S) {
			var stdout string
			s.Given("an answered deny on a question", func(t *testing.T) {})
			s.When("the hook polls its decision", func(t *testing.T) {
				denied := `{"id":7,"kind":"permission","state":"answered","decision":"deny","message":"not tonight"}`
				stdout = permitAskRuns(t, permitAskInput, denied)
			})
			s.Then("it prints today's deny", func(t *testing.T) {
				want := `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"not tonight"}}}` + "\n"
				if stdout != want {
					t.Fatalf("the hook printed %q, want the deny", stdout)
				}
			})
		})

		f.Scenario("the owner allowed a Bash call", func(s *bdd.S) {
			var stdout string
			s.Given("an answered allow on a Bash call", func(t *testing.T) {})
			s.When("the hook polls its decision", func(t *testing.T) {
				allowed := `{"id":7,"kind":"permission","state":"answered","decision":"allow","tool":"Bash"}`
				stdout = permitAskRuns(t, permitInputJSON, allowed)
			})
			s.Then("it prints today's exact bare allow", func(t *testing.T) {
				want := `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}` + "\n"
				if stdout != want {
					t.Fatalf("the hook printed %q, want the bare allow", stdout)
				}
			})
		})
	})
}
