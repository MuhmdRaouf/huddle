package domain

import "testing"

func TestParseDriverLock(t *testing.T) {
	t.Run("a pid-only lock reads the pid", func(t *testing.T) {
		lock, ok := ParseDriverLock("123\n")
		if !ok {
			t.Fatalf("ParseDriverLock(\"123\\n\") = not ok")
		}
		want := DriverLock{Pid: 123}
		if lock != want {
			t.Errorf("ParseDriverLock(\"123\\n\") = %+v, want %+v", lock, want)
		}
	})

	t.Run("surrounding space is tolerated", func(t *testing.T) {
		if _, ok := ParseDriverLock("  123 \n"); !ok {
			t.Errorf("ParseDriverLock with surrounding space = not ok, want ok")
		}
	})

	t.Run("everything else is not a driver lock", func(t *testing.T) {
		for _, text := range []string{
			"",
			"garbage",
			"202 1729",
			"202 1729 7",
			"0",
			"1",
			"x",
		} {
			if _, ok := ParseDriverLock(text); ok {
				t.Errorf("ParseDriverLock(%q) = ok, want not ok", text)
			}
		}
	})
}

func TestParseDriverStart(t *testing.T) {
	t.Run("digits read as the start time", func(t *testing.T) {
		start, ok := ParseDriverStart("1729999000000\n")
		if !ok {
			t.Fatalf("ParseDriverStart(\"1729999000000\\n\") = not ok")
		}
		if start != 1729999000000 {
			t.Errorf("ParseDriverStart(\"1729999000000\\n\") = %d, want 1729999000000", start)
		}
	})

	t.Run("everything else is not a start time", func(t *testing.T) {
		for _, text := range []string{
			"",
			"   ",
			"+1729999000000",
			"-1729999000000",
			"1729 999",
			"1729x999",
		} {
			if _, ok := ParseDriverStart(text); ok {
				t.Errorf("ParseDriverStart(%q) = ok, want not ok", text)
			}
		}
	})
}
