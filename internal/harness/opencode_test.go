package harness

import (
	"strconv"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// opencode cuts every MCP server request off after 5 s unless the entry names a longer
// timeout, and huddle's wait, ask and depend tools hold a call for up to a minute. The
// record install writes therefore names one itself, or every blocking tool times out
// before the channel answers.

func TestOpenCodeRecordCarriesATimeoutLongEnoughForWait(t *testing.T) {
	raw, err := openCodeBuild(NewEntry("opencode", "/users/home/bin/huddle"))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	value, err := jsfmt.DecodeValue(raw)
	if err != nil {
		t.Fatalf("the record does not parse: %v\n%s", err, raw)
	}
	timeout, ok := field(value, "timeout")
	if !ok || timeout.Kind() != jsfmt.KindNumber {
		t.Fatalf("the opencode record carries no timeout: %s", raw)
	}
	ms, perr := strconv.ParseInt(timeout.EncodeJSON(), 10, 64)
	if perr != nil || ms < 60000 {
		t.Fatalf("timeout = %s ms, want at least 60000 so a wait survives its long poll: %s", timeout.EncodeJSON(), raw)
	}
}
