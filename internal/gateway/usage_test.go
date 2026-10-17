package gateway

import (
	"os"
	"reflect"
	"testing"
)

// The usage an answer carries, read off each API family's wire shape: Anthropic's counters as
// they stand, OpenAI's cache-inclusive inputs split, streams and single documents alike. The
// fixtures live in testdata and are fed both whole and one byte at a time, so a chunk boundary
// never changes what the collector finds.

// fixture reads one testdata file.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("reading testdata/%s: %v", name, err)
	}
	return data
}

// counters builds a Usage from whole numbers, nil left as nil.
func counters(input, output, cacheRead, cacheWrite *int64) *Usage {
	return &Usage{Input: input, Output: output, CacheRead: cacheRead, CacheWrite: cacheWrite}
}

func whole(n int64) *int64 { return &n }

// feedUsage runs the fixture through a fresh collector, all at once or one byte at a time, and
// answers what it found.
func feedUsage(t *testing.T, kind apiKind, streaming bool, data []byte, split bool) *Usage {
	t.Helper()
	finder := newUsageFinder(kind, streaming)
	if split {
		for i := range data {
			if n, err := finder.Write(data[i : i+1]); err != nil || n != 1 {
				t.Fatalf("write %d: %d bytes, err %v", i, n, err)
			}
		}
	} else {
		if n, err := finder.Write(data); err != nil || n != len(data) {
			t.Fatalf("write: %d of %d bytes, err %v", n, len(data), err)
		}
	}
	return finder.Usage()
}

func TestUsageFromEachWireShape(t *testing.T) {
	cases := []struct {
		name      string
		kind      apiKind
		streaming bool
		file      string
		want      *Usage
	}{
		{"anthropic non-stream counts its counters as they stand", kindAnthropic, false,
			"anthropic-messages.json", counters(whole(12), whole(34), whole(7), whole(2))},
		{"anthropic stream overlays the deltas on the message start", kindAnthropic, true,
			"anthropic-stream.txt", counters(whole(25), whole(15), nil, nil)},
		{"openai chat non-stream splits cached tokens out of the prompt", kindChat, false,
			"openai-chat.json", counters(whole(60), whole(20), whole(40), nil)},
		{"openai chat stream takes the final chunk's usage", kindChat, true,
			"openai-chat-stream.txt", counters(whole(5), whole(3), whole(4), nil)},
		{"openai responses stream reads response.completed", kindResponses, true,
			"openai-responses.txt", counters(whole(40), whole(8), whole(10), nil)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := fixture(t, tc.file)
			for _, split := range []bool{false, true} {
				if got := feedUsage(t, tc.kind, tc.streaming, data, split); !reflect.DeepEqual(got, tc.want) {
					t.Errorf("usage (one write: %t) = %+v, want %+v", split, got, tc.want)
				}
			}
		})
	}
}

// A chat stream the client did not ask usage for carries none: the collector answers nil, and
// the route line records no tokens rather than an estimate.
func TestUsageUnknownStaysUnknown(t *testing.T) {
	stream := "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hi\"}}]}\n\ndata: [DONE]\n\n"
	if got := feedUsage(t, kindChat, true, []byte(stream), false); got != nil {
		t.Errorf("usage = %+v, want nil: no chunk said usage", got)
	}
	if got := feedUsage(t, kindChat, true, []byte("not sse at all"), false); got != nil {
		t.Errorf("usage = %+v, want nil for a body no event shaped", got)
	}
}

// A document far bigger than the collector's cap stops feeding the buffer, and its usage reads
// as unknown rather than as a body the gateway held whole.
func TestUsageCollectorCapsItsCopy(t *testing.T) {
	finder := newUsageFinder(kindAnthropic, false)
	big := make([]byte, usageCap+4096)
	for i := range big {
		big[i] = ' '
	}
	if n, err := finder.Write(big); err != nil || n != len(big) {
		t.Fatalf("write = %d bytes, err %v", n, err)
	}
	if got := finder.Usage(); got != nil {
		t.Errorf("usage = %+v, want nil: the usage sat past the cap", got)
	}
}
