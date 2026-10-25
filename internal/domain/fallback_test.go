package domain

import "testing"

const fallbackT0 int64 = 1_700_000_000_000

func TestThreeFailuresPickTheFallback(t *testing.T) {
	f := NewFallback([]string{"glm-5.3-flash"})
	for i := range 2 {
		if _, switched := f.Answered("glm-5.3", 503, fallbackT0+int64(i)); switched {
			t.Fatalf("failure %d switched", i+1)
		}
		if got := f.Pick("glm-5.3", fallbackT0+int64(i)); got != "glm-5.3" {
			t.Fatalf("after %d failures Pick = %s", i+1, got)
		}
	}
	change, switched := f.Answered("glm-5.3", 503, fallbackT0+2)
	if !switched {
		t.Fatal("the third failure did not switch")
	}
	if got := change.String(); got != "fallback glm-5.3 -> glm-5.3-flash (3 x 503)" {
		t.Errorf("switch = %q", got)
	}
	if got := f.Pick("glm-5.3", fallbackT0+2); got != "glm-5.3-flash" {
		t.Errorf("Pick after the switch = %s", got)
	}
	if got := f.Pick("glm-5.3", fallbackT0+2+FallbackWindowMs-1); got != "glm-5.3-flash" {
		t.Errorf("Pick inside the window = %s", got)
	}
	if got := f.Pick("glm-5.3", fallbackT0+2+FallbackWindowMs); got != "glm-5.3" {
		t.Errorf("Pick after the window = %s, want the primary again", got)
	}
}

func TestFallbackCountsOnlyConsecutiveFailures(t *testing.T) {
	f := NewFallback([]string{"glm-5.3-flash"})
	f.Answered("glm-5.3", 503, fallbackT0)
	f.Answered("glm-5.3", 429, fallbackT0+1)
	f.Answered("glm-5.3", 200, fallbackT0+2)
	f.Answered("glm-5.3", 500, fallbackT0+3)
	if _, switched := f.Answered("glm-5.3", 502, fallbackT0+4); switched {
		t.Fatal("a 2xx did not reset the count")
	}
	change, switched := f.Answered("glm-5.3", 429, fallbackT0+5)
	if !switched || change.String() != "fallback glm-5.3 -> glm-5.3-flash (3 x 429)" {
		t.Fatalf("switch = %q, %v", change.String(), switched)
	}
}

func TestFallbackIgnoresOtherClientErrors(t *testing.T) {
	f := NewFallback([]string{"glm-5.3-flash"})
	for i, status := range []int{503, 400, 404, 503} {
		if _, switched := f.Answered("glm-5.3", status, fallbackT0+int64(i)); switched {
			t.Fatalf("status %d switched", status)
		}
	}
	if _, switched := f.Answered("glm-5.3", 503, fallbackT0+9); !switched {
		t.Fatal("a 400 broke the run of failures; only a 2xx resets it")
	}
}

func TestFallbackCountsDroppedConnections(t *testing.T) {
	f := NewFallback([]string{"glm-5.3-flash"})
	f.Dropped("glm-5.3", "ECONNREFUSED", fallbackT0)
	f.Dropped("glm-5.3", "ECONNREFUSED", fallbackT0+1)
	change, switched := f.Dropped("glm-5.3", "ECONNREFUSED", fallbackT0+2)
	if !switched || change.String() != "fallback glm-5.3 -> glm-5.3-flash (3 x ECONNREFUSED)" {
		t.Fatalf("switch = %q, %v", change.String(), switched)
	}
}

func TestFallbackWalksTheChainInOrder(t *testing.T) {
	f := NewFallback([]string{"a", "b", "c"})
	trip := func(model string, at int64) {
		for i := range 3 {
			f.Answered(model, 500, at+int64(i))
		}
	}
	trip("a", fallbackT0)
	if got := f.Pick("a", fallbackT0+3); got != "b" {
		t.Errorf("a down: Pick = %s, want b", got)
	}
	trip("b", fallbackT0+3)
	if got := f.Pick("a", fallbackT0+6); got != "c" {
		t.Errorf("a and b down: Pick(a) = %s, want c", got)
	}
	if got := f.Pick("b", fallbackT0+6); got != "c" {
		t.Errorf("b down: Pick(b) = %s, want c", got)
	}
	trip("c", fallbackT0+6)
	if got := f.Pick("a", fallbackT0+9); got != "a" {
		t.Errorf("every model down: Pick = %s, want the model asked for", got)
	}
}

func TestFallbackWithNoNextModelNeverSwitches(t *testing.T) {
	for _, chain := range [][]string{nil, {"glm-5.3"}} {
		f := NewFallback(chain)
		for i := range 3 {
			if _, switched := f.Answered("glm-5.3", 503, fallbackT0+int64(i)); switched {
				t.Fatalf("chain %v switched", chain)
			}
		}
		if got := f.Pick("glm-5.3", fallbackT0+3); got != "glm-5.3" {
			t.Errorf("chain %v: Pick = %s", chain, got)
		}
	}
}

func TestFallbackTriesThePrimaryAfreshAfterTheWindow(t *testing.T) {
	f := NewFallback([]string{"glm-5.3-flash"})
	for i := range 3 {
		f.Answered("glm-5.3", 503, fallbackT0+int64(i))
	}
	back := fallbackT0 + 2 + FallbackWindowMs
	if _, switched := f.Answered("glm-5.3", 503, back); switched {
		t.Fatal("one failure after the window switched again; the count starts over")
	}
	if got := f.Pick("glm-5.3", back); got != "glm-5.3" {
		t.Errorf("Pick = %s", got)
	}
}
