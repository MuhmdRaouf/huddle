package shared

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

func TestAlertMarshalJSON(t *testing.T) {
	cost := USD(1.5)
	agent := "a1"
	cases := []struct {
		name  string
		alert Alert
		want  string
	}{
		{
			"writes a budget alert with id first and costUsd after detail",
			Alert{ID: "b1", Kind: AlertBudget, SessionID: "", Project: "p", Since: 7, Detail: "d", CostUsd: &cost, Severity: SeverityErr},
			`{"id":"b1","kind":"budget","sessionId":"","agentId":null,"project":"p","since":7,"detail":"d","costUsd":1.5,"severity":"err"}`,
		},
		{
			"writes a session alert with kind first and costUsd before id",
			Alert{ID: "s1", Kind: AlertStuck, SessionID: "s", AgentID: &agent, Project: "p", Since: 3, Detail: "d", Severity: SeverityWarn},
			`{"kind":"stuck","sessionId":"s","agentId":"a1","project":"p","costUsd":null,"id":"s1","since":3,"detail":"d","severity":"warn"}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := domain.MarshalCompactJSON(c.alert)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(got) != c.want {
				t.Errorf("alert = %s, want %s", got, c.want)
			}
		})
	}
}

func TestJSONMarshalJSON(t *testing.T) {
	t.Run("encodes an absent value as null", func(t *testing.T) {
		got, err := JSON{}.MarshalJSON()
		if err != nil || string(got) != "null" {
			t.Errorf("JSON{} = %s, %v, want null", got, err)
		}
	})
	t.Run("encodes a value with its keys in the order they arrived", func(t *testing.T) {
		value, err := jsfmt.DecodeValue([]byte(`{"z":1,"a":[true,"x"]}`))
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		got, err := JSON{Value: value}.MarshalJSON()
		if err != nil || string(got) != `{"z":1,"a":[true,"x"]}` {
			t.Errorf("JSON = %s, %v", got, err)
		}
	})
}

func TestSpoolLineShapes(t *testing.T) {
	t.Run("reads a null line as the empty line", func(t *testing.T) {
		line := SpoolLine{Ts: "x"}
		if err := json.Unmarshal([]byte(`null`), &line); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if line.Ts != "" || line.Field("ts") != nil {
			t.Errorf("line = %+v, want the empty line", line)
		}
	})
	for _, raw := range []string{`true`, `1`, `"s"`, `[1]`} {
		t.Run("refuses a line that is not an object: "+raw, func(t *testing.T) {
			var line SpoolLine
			err := line.UnmarshalJSON([]byte(raw))
			var typeErr *json.UnmarshalTypeError
			if !errors.As(err, &typeErr) {
				t.Fatalf("err = %v, want an UnmarshalTypeError", err)
			}
		})
	}
	t.Run("refuses a line that is not JSON", func(t *testing.T) {
		var line SpoolLine
		if err := line.UnmarshalJSON([]byte(`{`)); err == nil {
			t.Error("err = nil, want the decode error")
		}
	})
	t.Run("answers a key's last value, nil for a missing key or a line built in code", func(t *testing.T) {
		var line SpoolLine
		if err := json.Unmarshal([]byte(`{"ts":"a","cwd":5,"cwd":"/w"}`), &line); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got := line.Field("cwd"); got == nil || got.EncodeJSON() != `"/w"` {
			t.Errorf("Field(cwd) = %v, want \"/w\"", got)
		}
		if got := line.Field("event"); got != nil {
			t.Errorf("Field(event) = %v, want nil", got)
		}
		if got := (SpoolLine{Ts: "a"}).Field("ts"); got != nil {
			t.Errorf("a built line's Field(ts) = %v, want nil", got)
		}
	})
}
