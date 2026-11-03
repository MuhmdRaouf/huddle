package router

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"runtime"
	"strings"
	"testing"
)

// bodyRequest is a request whose body is text, announcing length as its Content-Length.
func bodyRequest(text string, length int64) *http.Request {
	return &http.Request{Body: io.NopCloser(strings.NewReader(text)), ContentLength: length}
}

// allocatedBy is the fewest bytes run allocates over several runs: a run's own allocations repeat,
// while the runtime's background allocations come and go.
func allocatedBy(run func()) uint64 {
	least := ^uint64(0)
	for range 5 {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		run()
		runtime.ReadMemStats(&after)
		least = min(least, after.TotalAlloc-before.TotalAlloc)
	}
	return least
}

// A request that announces its length is read into one buffer of that size: a 2 MiB body that
// grew through io.ReadAll's doublings allocated twice its size and wrote every byte twice.
func TestReadBodyAllocatesAnAnnouncedBodyOnce(t *testing.T) {
	const size = 2 << 20
	text := strings.Repeat("x", size)
	var body []byte
	allocated := allocatedBy(func() {
		var err error
		if body, err = ReadBody(bodyRequest(text, size), BodyLimitBytes); err != nil {
			t.Fatalf("ReadBody: %v", err)
		}
	})
	if !bytes.Equal(body, []byte(text)) {
		t.Fatalf("ReadBody returned %d bytes that differ from the %d sent", len(body), size)
	}
	if limit := uint64(size + size/8); allocated > limit {
		t.Errorf("reading a %d byte body allocated %d bytes, want at most %d", size, allocated, limit)
	}
}

func TestReadBodyKeepsItsContractWhateverTheRequestAnnounces(t *testing.T) {
	for _, tc := range []struct {
		name   string
		text   string
		length int64
		limit  int64
		want   string
		err    error
	}{
		{"an unannounced length reads to the end", "hello", -1, 100, "hello", nil},
		{"an empty announced body is empty", "", 0, 100, "", nil},
		{"an announced body at the limit is read whole", "12345678", 8, 8, "12345678", nil},
		{"an announced body over the limit is too large", "123456789", 9, 8, "", ErrBodyTooLarge},
		{"an unannounced body over the limit is too large", "123456789", -1, 8, "", ErrBodyTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := ReadBody(bodyRequest(tc.text, tc.length), tc.limit)
			if !errors.Is(err, tc.err) {
				t.Fatalf("ReadBody error = %v, want %v", err, tc.err)
			}
			if string(body) != tc.want {
				t.Errorf("ReadBody = %q, want %q", body, tc.want)
			}
		})
	}
}
