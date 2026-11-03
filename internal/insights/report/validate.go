package report

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Validate reads a model's answer as a report and checks it against the payload the run sent:
// at most MaxTips tips, each quoting a number that is in the payload.
func Validate(answer string, payload []byte) (Report, error) {
	r, err := Parse(answer)
	if err != nil {
		return Report{}, err
	}
	if len(r.Tips) > MaxTips {
		return Report{}, invalid("%d tips, want at most %d", len(r.Tips), MaxTips)
	}
	if len(r.Tips) == 0 {
		return r, nil
	}
	known, err := payloadNumbers(payload)
	if err != nil {
		return Report{}, err
	}
	for _, tip := range r.Tips {
		if !quotesAny(tip, known) {
			return Report{}, invalid("tip %q quotes no number from the data", tip)
		}
	}
	return r, nil
}

// payloadNumbers is every number in the payload's JSON, by value.
func payloadNumbers(payload []byte) (map[float64]bool, error) {
	known := map[float64]bool{}
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return known, nil
		}
		if err != nil {
			return nil, err
		}
		if n, ok := tok.(json.Number); ok {
			if v, err := strconv.ParseFloat(n.String(), 64); err == nil {
				known[v] = true
			}
		}
	}
}

// quoted matches a number as people write it: digits with optional thousands commas and decimals.
var quoted = regexp.MustCompile(`\d{1,3}(?:,\d{3})+(?:\.\d+)?|\d+(?:\.\d+)?`)

// quotesAny reports whether the tip writes a number whose value is in known. The match is by
// exact value: "1,200" is 1200, but "1.2k" and a rounded "0.002" for 0.0021 are not. A digit
// that follows a letter or hyphen belongs to a label such as session-3, not to a quote.
func quotesAny(tip string, known map[float64]bool) bool {
	for _, at := range quoted.FindAllStringIndex(tip, -1) {
		if before, _ := utf8.DecodeLastRuneInString(tip[:at[0]]); unicode.IsLetter(before) || before == '-' {
			continue
		}
		v, err := strconv.ParseFloat(strings.ReplaceAll(tip[at[0]:at[1]], ",", ""), 64)
		if err == nil && known[v] {
			return true
		}
	}
	return false
}
