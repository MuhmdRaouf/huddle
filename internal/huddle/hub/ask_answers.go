// The answers an AskUserQuestion allow carries: one answer per question the input
// asks, keyed by the question's exact text, multi-select labels joined by commas. The decide
// route checks the answers here before it stores them, and reads them through the same path
// floor every input string passes.
package hub

import (
	"fmt"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// AnswersError is why a decide route refused the answers it was handed; Msg is the one calm
// line the dashboard shows.
type AnswersError struct {
	Msg string
}

func (e *AnswersError) Error() string { return e.Msg }

// CheckAnswers refuses answers that answer nothing: a tool other than a question takes no
// answers at all, a question takes one answer per question it asks, and an input whose
// questions cannot be read has no answers to check — the owner answers it in the terminal.
func CheckAnswers(tool, input string, answers map[string]string) error {
	if tool != "AskUserQuestion" {
		if len(answers) > 0 {
			return &AnswersError{"answers: only a question takes answers"}
		}
		return nil
	}
	if len(answers) == 0 {
		return &AnswersError{"answers: name an answer for each question"}
	}
	questions := questionsOf(input)
	if len(questions) == 0 {
		return &AnswersError{"answers: the question cannot be read; answer it in the terminal"}
	}
	asked := make(map[string]bool, len(questions))
	for _, question := range questions {
		asked[question] = true
	}
	for key := range answers {
		if !asked[key] {
			return &AnswersError{fmt.Sprintf("answers: no question reads %q", key)}
		}
	}
	for _, question := range questions {
		if answers[question] == "" {
			return &AnswersError{fmt.Sprintf("answers: no answer for %q", question)}
		}
	}
	return nil
}

// questionsOf reads the question texts an AskUserQuestion input carries — questions[].question,
// the only part the answers key against. Text that reads as no question answers none.
func questionsOf(input string) []string {
	value, err := jsfmt.DecodeValue([]byte(input))
	if err != nil || value == nil || value.Kind() != jsfmt.KindObject {
		return nil
	}
	list := value.Get("questions")
	if list == nil || list.Kind() != jsfmt.KindArray {
		return nil
	}
	var questions []string
	for _, item := range list.Items() {
		if item == nil || item.Kind() != jsfmt.KindObject {
			continue
		}
		if question := item.Get("question"); question != nil && question.Kind() == jsfmt.KindString {
			questions = append(questions, question.Text())
		}
	}
	return questions
}

// AnswersFloor answers the path floor's read of the answers themselves: a question
// or an answer naming a .ssh, .gnupg or secrets path refuses, the same floor the input passes —
// an answer is text the machine would act on, never a note it ignores.
func AnswersFloor(answers map[string]string) (string, bool) {
	for key, answer := range answers {
		if guardedText(key) || guardedText(answer) {
			return floorPath, false
		}
	}
	return "", true
}
