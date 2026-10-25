package router

// modelOf reads only what picking a lane needs: the top-level `model`, read where Claude Code
// writes it, first in the object.

import "testing"

func TestModelOfStopsAtTheModelKey(t *testing.T) {
	t.Parallel()

	t.Run("the model is read without reading the bytes after it", func(t *testing.T) {
		t.Parallel()
		// The bytes after the model are no JSON at all: a reader that went past the model
		// would answer "".
		if got := modelOf([]byte(`{"model":"glm-5.3","messages":not json`)); got != "glm-5.3" {
			t.Errorf("modelOf = %q, want glm-5.3", got)
		}
	})

	t.Run("a model after other fields is still read", func(t *testing.T) {
		t.Parallel()
		if got := modelOf([]byte(`{"max_tokens":1,"stream":true,"model":"claude-opus-5-5"}`)); got != "claude-opus-5-5" {
			t.Errorf("modelOf = %q, want claude-opus-5-5", got)
		}
	})

	for name, body := range map[string]string{
		"a model that is no string": `{"model":7}`,
		"an array":                  `[1]`,
		"no json":                   `not json`,
		"an empty object":           `{}`,
		"an empty body":             ``,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := modelOf([]byte(body)); got != "" {
				t.Errorf("modelOf(%s) = %q, want none", body, got)
			}
		})
	}
}
