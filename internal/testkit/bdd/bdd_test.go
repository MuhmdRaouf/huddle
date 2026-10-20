package bdd_test

import (
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
)

func TestStepsRunInOrder(t *testing.T) {
	var ran []string
	bdd.Feature(t, "ordering", func(f *bdd.F) {
		f.Scenario("steps run in order", func(s *bdd.S) {
			s.Given("a started scenario", func(t *testing.T) { ran = append(ran, "given") })
			s.When("the action happens", func(t *testing.T) { ran = append(ran, "when") })
			s.Then("the outcome holds", func(t *testing.T) { ran = append(ran, "then") })
			s.And("one more thing", func(t *testing.T) { ran = append(ran, "and") })
		})
	})
	want := []string{"given", "when", "then", "and"}
	if !slices.Equal(ran, want) {
		t.Errorf("steps ran %q, want %q", ran, want)
	}
}

func TestStepNamesCarryTheirKeyword(t *testing.T) {
	var names []string
	record := func(t *testing.T) { names = append(names, t.Name()) }
	bdd.Feature(t, "f", func(f *bdd.F) {
		f.Scenario("s", func(s *bdd.S) {
			s.Given("a thing", record)
			s.When("it acts", record)
			s.Then("it shows", record)
		})
	})
	suffixes := []string{
		"Feature:_f/Scenario:_s/Given_a_thing",
		"Feature:_f/Scenario:_s/When_it_acts",
		"Feature:_f/Scenario:_s/Then_it_shows",
	}
	if len(names) != len(suffixes) {
		t.Fatalf("recorded %d names (%q), want %d", len(names), names, len(suffixes))
	}
	for i, suffix := range suffixes {
		if !strings.HasSuffix(names[i], suffix) {
			t.Errorf("step name %q does not end with %q", names[i], suffix)
		}
	}
}

// A failing step must fail the test, so the check runs the scenario in a child process: a step
// that fails inside this own test binary would stop the test run here.
func TestAStepAfterAFailureIsSkipped(t *testing.T) {
	if os.Getenv("BDD_CHILD") == "1" {
		bdd.Feature(t, "failure", func(f *bdd.F) {
			f.Scenario("a failing step skips the rest", func(s *bdd.S) {
				s.Given("a broken thing", func(t *testing.T) { t.Error("given failed") })
				s.Then("it is never reached", func(t *testing.T) { t.Log("THEN-RAN") })
			})
		})
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestAStepAfterAFailureIsSkipped$", "-test.v")
	cmd.Env = isoenv.Environ(t, t.TempDir(), "BDD_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Errorf("the child exited 0, want non-zero: a failing step must fail the test; output:\n%s", out)
	}
	if !strings.Contains(string(out), "skipped: an earlier step failed") {
		t.Errorf("output does not name the skip reason; output:\n%s", out)
	}
	if strings.Contains(string(out), "THEN-RAN") {
		t.Errorf("the step after the failure ran; output:\n%s", out)
	}
}

func TestScenarioExposesItsT(t *testing.T) {
	bdd.Feature(t, "scenario t", func(f *bdd.F) {
		f.Scenario("exposes its testing.T", func(s *bdd.S) {
			dir := s.T.TempDir()
			info, err := os.Stat(dir)
			if err != nil {
				s.T.Fatalf("stat %s: %v", dir, err)
			}
			if !info.IsDir() {
				s.T.Errorf("%s is not a directory", dir)
			}
		})
	})
}
