package pricing

import (
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
)

// refBracketSuffix and refDatedSuffix are referenceCanonicalModelID's suffix rules.
var refBracketSuffix = regexp.MustCompile(`\[[^\]]*\]$`)
var refDatedSuffix = regexp.MustCompile(`[-@]\d{8}$`)

// referenceCanonicalModelID is the regexp-based canon canonicalModelID is held against: the
// differential test proves the two answer identically on every input below.
func referenceCanonicalModelID(model string) string {
	bare := refBracketSuffix.ReplaceAllString(strings.ToLower(jsscalar.Trim(model)), "")
	unprefixed := bare
	if slash := strings.LastIndex(bare, "/"); slash >= 0 {
		unprefixed = bare[slash+1:]
	}
	return refDatedSuffix.ReplaceAllString(unprefixed, "")
}

// referenceOfferingOfHost is the url.Parse-based canon OfferingOfHost is held against: the
// differential test proves the two answer identically on every input below.
func referenceOfferingOfHost(urlOrHost string) (OfferingID, bool) {
	trimmed := strings.ToLower(jsscalar.Trim(urlOrHost))
	var host string
	if strings.Contains(trimmed, "://") {
		parsed, err := url.Parse(trimmed)
		if err != nil {
			return "", false
		}
		host = parsed.Hostname()
	} else {
		host = trimmed
		if slash := strings.Index(host, "/"); slash >= 0 {
			host = host[:slash]
		}
		if colon := strings.Index(host, ":"); colon >= 0 {
			host = host[:colon]
		}
	}
	for _, offering := range Offerings {
		if host == offering.Host {
			return offering.ID, true
		}
	}
	return "", false
}

// modelInputs lists every input the model-id differential test checks: every sheet id as
// written and upper-cased, with leading and trailing spaces and tabs, with the bracket,
// prefix and dated suffixes callers pass, and the awkward leftovers.
func modelInputs() []string {
	inputs := []string{}
	for _, offering := range Offerings {
		for id := range offering.Models {
			inputs = append(inputs,
				id,
				strings.ToUpper(id),
				" "+id,
				id+" ",
				"\t"+id+"\t",
				id+"[1m]",
				id+"-20260101",
				id+"@20260101",
				"anthropic/"+id,
				"openrouter/z-ai/"+id,
			)
		}
	}
	return append(inputs,
		"",
		"/",
		"//",
		"glm-5.3[]",
		"claude-opus-5-5[200k]",
		"GLM-5.3[1M]",
		"claude-sonnet-5-5[1m]-20260101",
		"a[b[c]",
		"x]",
		"[x",
		"a]b[c]",
		"glm-5.3-2026010",
		"glm-5.3-202601011",
		"glm-5.3_20260101",
		"@20260101",
		"-20260101",
		"[-20260101]",
		" glm-5.3 ",
		" glm-5.3 ",
		"　glm-5.3　",
		"ANTHROPIC/ CLAUDE-OPUS-5-5",
		"z-ai/glm-5.3[1m]",
		"claude-opus-4-5/",
		"/claude-opus-4-5",
	)
}

// hostInputs lists every input the host differential test checks: every offering's host in
// the shapes the read paths see — bare, scheme forms, paths, queries, fragments, ports,
// user info, upper case, surrounding spaces — plus the malformed and hostile leftovers.
func hostInputs() []string {
	inputs := []string{}
	for _, offering := range Offerings {
		host := offering.Host
		inputs = append(inputs,
			host,
			"https://"+host,
			"http://"+host,
			host+"/v1",
			"https://"+host+"/api/anthropic",
			"https://"+host+"/v1/messages?beta=true",
			"https://"+host+"/v1#fragment",
			host+":8080",
			"https://user:pw@"+host+"/v1",
			"HTTPS://"+strings.ToUpper(host)+"/V1",
			"  https://"+host+"  ",
			"https://USER:PW@"+strings.ToUpper(host),
		)
	}
	return append(inputs,
		"https://[::1]:8080/x",
		"https://",
		"://",
		"ftp://api.z.ai",
		"https://api.z.ai.evil.com",
		"https://evil.com/api.z.ai",
		"not a url",
		"https://%zz",
		"https://[::1",
		"https://@api.z.ai",
		"https://api.z.ai:8080/x",
		"http://API.Z.AI",
		"",
		" ",
		":",
		"a:",
	)
}

func TestCanonicalModelIDMatchesReference(t *testing.T) {
	for _, input := range modelInputs() {
		want := referenceCanonicalModelID(input)
		if got := canonicalModelID(input); got != want {
			t.Fatalf("canonicalModelID(%q) = %q, reference gives %q", input, got, want)
		}
	}
	// Anchors: the differential proves equality, these prove the shared answer.
	if got := canonicalModelID("zai/glm-5.3[1m]"); got != "glm-5.3" {
		t.Fatalf("canonicalModelID(%q) = %q, want %q", "zai/glm-5.3[1m]", got, "glm-5.3")
	}
	if got := canonicalModelID("anthropic/claude-opus-5-5-20260101"); got != "claude-opus-5-5" {
		t.Fatalf("canonicalModelID(%q) = %q, want %q", "anthropic/claude-opus-5-5-20260101", got, "claude-opus-5-5")
	}
}

func TestOfferingOfHostMatchesReference(t *testing.T) {
	for _, input := range hostInputs() {
		wantID, wantOK := referenceOfferingOfHost(input)
		gotID, gotOK := OfferingOfHost(input)
		if gotID != wantID || gotOK != wantOK {
			t.Fatalf("OfferingOfHost(%q) = (%q, %v), reference gives (%q, %v)", input, gotID, gotOK, wantID, wantOK)
		}
	}
	// Anchors: the differential proves equality, these prove the shared answer.
	if id, ok := OfferingOfHost("https://api.z.ai/api/anthropic"); !ok || id != OfferingZai {
		t.Fatalf("OfferingOfHost(%q) = (%q, %v), want (%q, true)", "https://api.z.ai/api/anthropic", id, ok, OfferingZai)
	}
	if id, ok := OfferingOfHost("dashscope-intl.aliyuncs.com"); !ok || id != OfferingQwen {
		t.Fatalf("OfferingOfHost(%q) = (%q, %v), want (%q, true)", "dashscope-intl.aliyuncs.com", id, ok, OfferingQwen)
	}
}

// TestPriceLookupsAllocateNothing: the hub prices every request on its read paths, so a
// model id already in canonical form and a plain https upstream must answer with no
// allocation at all.
func TestPriceLookupsAllocateNothing(t *testing.T) {
	for _, model := range []string{"glm-5.3", "claude-opus-5-5"} {
		var got string
		if n := testing.AllocsPerRun(100, func() {
			got = canonicalModelID(model)
		}); n != 0 {
			t.Fatalf("canonicalModelID(%q) = %q allocates %v times per call, want 0", model, got, n)
		}
	}
	var id OfferingID
	var ok bool
	if n := testing.AllocsPerRun(100, func() {
		id, ok = OfferingOfHost("https://api.z.ai/api/anthropic")
	}); n != 0 {
		t.Fatalf("OfferingOfHost(%q) = (%q, %v) allocates %v times per call, want 0",
			"https://api.z.ai/api/anthropic", id, ok, n)
	}
}

// benchEntry is one request of the benchmark mix: where it ran and what to price.
type benchEntry struct {
	host    string // the upstream OfferingOfHost matches; empty when only the model id is known
	request PriceRequest
}

// BenchmarkRequestCostMix prices a fixed mix of eight realistic requests the way the hub's
// read paths do: an Offering looked up from the upstream host, then PriceRequestOf.
func BenchmarkRequestCostMix(b *testing.B) {
	// A Tuesday 02:00 UTC, inside DeepSeek's peak windows.
	ts := time.Date(2026, 10, 27, 2, 0, 0, 0, time.UTC).UnixMilli()
	mix := []benchEntry{
		{host: "https://api.z.ai/api/anthropic", request: PriceRequest{
			Model: "glm-5.3", Ts: ts,
			Tokens: TokenUse{Input: 12000, Output: 900, CacheRead: 48000, CacheWrite5m: 4000},
		}},
		{host: "", request: PriceRequest{
			Model: "claude-opus-5-5", Ts: ts,
			Tokens: TokenUse{Input: 21000, Output: 1400, CacheRead: 130000, CacheWrite5m: 9000, CacheWrite1h: 2000},
		}},
		{host: "https://api.anthropic.com/v1", request: PriceRequest{
			Model: "claude-sonnet-5-5[1m]", Ts: ts,
			Tokens: TokenUse{Input: 150000, Output: 2100, CacheRead: 640000, CacheWrite5m: 32000},
		}},
		{host: "https://api.anthropic.com/v1", request: PriceRequest{
			Model: "claude-opus-5-5-20260101", Ts: ts,
			Tokens: TokenUse{Input: 9500, Output: 700, CacheRead: 51000, CacheWrite5m: 3800},
		}},
		{host: "api.moonshot.ai", request: PriceRequest{
			Model: "kimi-k3", Ts: ts,
			Tokens: TokenUse{Input: 18000, Output: 1200, CacheRead: 74000, CacheWrite5m: 5100},
		}},
		{host: "https://api.deepseek.com/v1", request: PriceRequest{
			Model: "deepseek-v4-pro", Ts: ts,
			Tokens: TokenUse{Input: 22000, Output: 1600, CacheRead: 88000, CacheWrite5m: 6200},
		}},
		{host: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1", request: PriceRequest{
			Model: "qwen3.8-max", Ts: ts,
			Tokens: TokenUse{Input: 26000, Output: 1800, CacheRead: 91000, CacheWrite5m: 7000},
		}},
		{host: "https://api.minimax.io/v1", request: PriceRequest{
			Model: "MiniMax-M3", Ts: ts,
			Tokens: TokenUse{Input: 600000, Output: 2400, CacheRead: 30000, CacheWrite5m: 15000},
		}},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, entry := range mix {
			entry.request.Offering = nil
			if entry.host != "" {
				if id, ok := OfferingOfHost(entry.host); ok {
					entry.request.Offering = &id
				}
			}
			if PriceRequestOf(entry.request) == nil {
				b.Fatalf("no sheet prices %s on %q", entry.request.Model, entry.host)
			}
		}
	}
}
