package domain

import "testing"

func TestParseDriverLock(t *testing.T) {
	t.Run("a two-field lock reads the pid and the start time", func(t *testing.T) {
		lock, ok := ParseDriverLock("202 1729\n")
		if !ok {
			t.Fatalf("ParseDriverLock(\"202 1729\\n\") = not ok")
		}
		want := DriverLock{Pid: 202, Start: 1729, StartKnown: true}
		if lock != want {
			t.Errorf("ParseDriverLock(\"202 1729\\n\") = %+v, want %+v", lock, want)
		}
	})

	t.Run("an old pid-only lock reads the pid with no start time", func(t *testing.T) {
		lock, ok := ParseDriverLock("202\n")
		if !ok {
			t.Fatalf("ParseDriverLock(\"202\\n\") = not ok")
		}
		want := DriverLock{Pid: 202, StartKnown: false}
		if lock != want {
			t.Errorf("ParseDriverLock(\"202\\n\") = %+v, want %+v", lock, want)
		}
	})

	t.Run("surrounding space is tolerated", func(t *testing.T) {
		if _, ok := ParseDriverLock("  202 1729  \n"); !ok {
			t.Errorf("ParseDriverLock with surrounding space = not ok, want ok")
		}
	})

	t.Run("everything else is not a driver lock", func(t *testing.T) {
		for _, text := range []string{
			"",
			"garbage",
			"202 1729 7",
			"0",
			"1",
			"202 0",
			"202 -5",
			"202 x",
			"x 1729",
		} {
			if _, ok := ParseDriverLock(text); ok {
				t.Errorf("ParseDriverLock(%q) = ok, want not ok", text)
			}
		}
	})
}
