package server

import (
	"context"
	"encoding/json"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

var bg = context.Background()

// openChannel opens one channel over a fresh SQLite file, closed with the test.
func openChannel(t *testing.T, name string) *channel.Channel {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(bg, filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	ch, err := channel.Open(bg, name, st)
	if err != nil {
		t.Fatalf("open channel: %v", err)
	}
	t.Cleanup(func() { _ = ch.Close(bg) })
	return ch
}

func vv(t *testing.T, json string) *jsfmt.Value {
	t.Helper()
	v, err := jsfmt.DecodeValue([]byte(json))
	if err != nil {
		t.Fatalf("decode %s: %v", json, err)
	}
	return v
}

// mustJSON renders one answer the way the server writes it out.
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := domain.MarshalCompactJSON(v)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return string(raw)
}

// queryOf builds one query string's values ("a=b" → {a: [b]}).
func queryOf(s string) url.Values {
	values, err := url.ParseQuery(s)
	if err != nil {
		panic(err)
	}
	return values
}

// bodyMap parses one JSON answer body for field assertions.
func bodyMap(t *testing.T, body string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("body %q: %v", body, err)
	}
	return out
}
