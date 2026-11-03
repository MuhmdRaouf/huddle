package report

import (
	"errors"
	"strings"
	"testing"
)

// samplePayload is a payload with a few numbers a tip can quote: 3 sessions, 1,200 and 0.0021.
const samplePayload = `{"window":{"days":7},"totals":{"sessions":3,"requests":42,"tokens":{"input":1200},"costUsd":0.0021},` +
	`"sessions":[{"label":"session-3","requests":5}]}`

// answer is a valid answer with the given lines between the headings, one block per section.
func answer(blocks ...string) string {
	var out strings.Builder
	for i, title := range SectionTitles {
		out.WriteString("## " + title + "\n")
		if i < len(blocks) {
			out.WriteString(blocks[i])
		}
	}
	return out.String()
}

const prose = "The numbers are in the table.\n"

func TestValidAnswerBecomesAReport(t *testing.T) {
	text := answer(
		"Most tokens went to one session.\nTip: split the work, 42 requests ran in 7 days.\n",
		prose, "Tip: 1,200 input tokens were repeated.\n", prose,
		"Tip: cost stayed at 0.0021 dollars.\n",
	)
	got, err := Validate(text, []byte(samplePayload))
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(got.Sections) != 5 || got.Sections[0].Title != "Where the tokens went" || got.Sections[0].Body != "Most tokens went to one session." {
		t.Errorf("sections = %+v", got.Sections)
	}
	want := []string{"split the work, 42 requests ran in 7 days.", "1,200 input tokens were repeated.", "cost stayed at 0.0021 dollars."}
	if strings.Join(got.Tips, "|") != strings.Join(want, "|") {
		t.Errorf("tips = %q, want %q", got.Tips, want)
	}
}

func TestFourTipsFailValidation(t *testing.T) {
	text := answer(
		"Tip: one, 42 requests.\nTip: two, 7 days.\n", prose, "Tip: three, 3 sessions.\n", prose, "Tip: four, 1200 tokens.\n",
	)
	_, err := Validate(text, []byte(samplePayload))
	var invalid *InvalidReportError
	if !errors.As(err, &invalid) || !strings.Contains(err.Error(), "4 tips") {
		t.Fatalf("Validate = %v, want an InvalidReportError naming 4 tips", err)
	}
}

func TestTipWithoutPayloadNumberFails(t *testing.T) {
	cases := map[string]string{
		"no number":                       "Tip: use fewer tools.\n",
		"a number the payload lacks":      "Tip: 99 requests is too many.\n",
		"a number inside a session label": "Tip: look at session-5 first.\n",
		"a rounded number":                "Tip: 0.002 dollars a request.\n",
		"a shortened number":              "Tip: 1.2k input tokens.\n",
	}
	for name, tip := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Validate(answer(prose, prose, tip, prose, prose), []byte(samplePayload))
			var invalid *InvalidReportError
			if !errors.As(err, &invalid) || !strings.Contains(err.Error(), "tip") {
				t.Errorf("Validate = %v, want an InvalidReportError about the tip", err)
			}
		})
	}
}

func TestTipNumbersMatchThePayloadExactly(t *testing.T) {
	cases := map[string]string{
		"a whole number":                 "Tip: 42 requests.\n",
		"a thousands separator":          "Tip: 1,200 tokens.\n",
		"a decimal":                      "Tip: 0.0021 a request.\n",
		"one match among others":         "Tip: 99 beats 3 sessions.\n",
		"a bulleted tip":                 "- Tip: 42 requests.\n",
		"a number closing the sentence":  "Tip: you ran 42.\n",
		"a number after a currency sign": "Tip: spent $0.0021 in all.\n",
	}
	for name, tip := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Validate(answer(prose, prose, tip, prose, prose), []byte(samplePayload)); err != nil {
				t.Errorf("Validate: %v", err)
			}
		})
	}
}

func TestMalformedAnswersFail(t *testing.T) {
	good := answer(prose, prose, prose, prose, prose)
	cases := map[string]struct{ text, reason string }{
		"empty":                  {"", "0 sections"},
		"text before the first":  {"Here is your report.\n" + good, "before the first"},
		"a section missing":      {strings.Replace(good, "## Waste\n"+prose, "", 1), "4 sections"},
		"a section added":        {good + "## Extra\n" + prose, "6 sections"},
		"sections out of order":  {strings.Replace(strings.Replace(good, "Waste", "TMP", 1), "What went well", "Waste", 1), "section 3"},
		"a numbered heading":     {strings.Replace(good, "## Waste", "## 3. Waste", 1), "section 3"},
		"a section with no text": {strings.Replace(good, "## Waste\n"+prose, "## Waste\n", 1), "Waste"},
		"a tip with no text":     {answer(prose, prose, "Tip:\n", prose, prose), "empty tip"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Validate(c.text, []byte(samplePayload))
			var invalid *InvalidReportError
			if !errors.As(err, &invalid) || !strings.Contains(err.Error(), c.reason) || strings.Contains(err.Error(), "\n") {
				t.Errorf("Validate = %v, want a one-line InvalidReportError containing %q", err, c.reason)
			}
		})
	}
}
