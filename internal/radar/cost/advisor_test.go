package cost

import (
	"fmt"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

func TestModelAdvisor(t *testing.T) {
	small := shared.Tokens{Input: 10_000, Output: 500}

	t.Run("finds main-model subagent runs that only read, and prices the flash saving", func(t *testing.T) {
		requests := []shared.RequestRecord{
			makeRequest(func(r *shared.RequestRecord) {
				r.ID, r.AgentID, r.Model, r.Upstream, r.Tokens = "1", "w1", "glm-5.3", zai, small
			}),
			makeRequest(func(r *shared.RequestRecord) {
				r.ID, r.AgentID, r.Model, r.Upstream, r.Tokens = "2", "w1", "glm-5.3", zai, small
			}),
			makeRequest(func(r *shared.RequestRecord) {
				r.ID, r.AgentID, r.Model, r.Upstream, r.Tokens = "3", "w2", "glm-5.3", zai, small
			}),
			makeRequest(func(r *shared.RequestRecord) {
				r.ID, r.AgentID, r.Model, r.Upstream, r.Tokens = "4", "w3", "glm-5.3-flash", zai, small
			}),
			makeRequest(func(r *shared.RequestRecord) {
				r.ID, r.AgentID, r.Model = "5", "w4", "claude-opus-5"
				r.Tokens = small
			}),
			makeRequest(func(r *shared.RequestRecord) {
				r.ID, r.AgentID, r.Model, r.Upstream, r.Tokens = "6", "main", "glm-5.3", zai, small
			}),
		}
		tools := []shared.ToolCallRecord{
			makeTool(func(r *shared.ToolCallRecord) { r.ID, r.AgentID, r.Name = "t1", str("w1"), "Read" }),
			makeTool(func(r *shared.ToolCallRecord) { r.ID, r.AgentID, r.Name = "t2", str("w2"), "Edit" }),
			makeTool(func(r *shared.ToolCallRecord) { r.ID, r.AgentID, r.Name = "t3", str("w4"), "Grep" }),
			makeTool(func(r *shared.ToolCallRecord) { r.ID, r.AgentID, r.Name = "t4", str("nobody"), "Read" }),
			makeTool(func(r *shared.ToolCallRecord) { r.ID, r.AgentID, r.Name = "t5", nil, "Read" }),
		}
		report := Advise(requests, tools)
		if report.RunsChecked != 3 {
			t.Errorf("runsChecked: got %d want 3", report.RunsChecked)
		}
		if report.Candidates != 2 {
			t.Errorf("candidates: got %d want 2", report.Candidates)
		}
		wantModels := []struct {
			model string
			runs  int64
			flash *string
		}{
			{"claude-opus-5", 1, nil},
			{"glm-5.3", 1, str("glm-5.3-flash")},
		}
		if len(report.ByModel) != len(wantModels) {
			t.Fatalf("byModel: got %d rows want %d", len(report.ByModel), len(wantModels))
		}
		for i, want := range wantModels {
			got := report.ByModel[i]
			if got.Model != want.model || got.Runs != want.runs {
				t.Errorf("byModel %d: got %s/%d want %s/%d", i, got.Model, got.Runs, want.model, want.runs)
			}
			if (got.Flash == nil) != (want.flash == nil) ||
				(got.Flash != nil && *got.Flash != *want.flash) {
				t.Errorf("byModel %s flash: got %v want %v", want.model, got.Flash, want.flash)
			}
		}
		glmCost := RequestCost(pricedOfRecord(makeRequest(func(r *shared.RequestRecord) {
			r.Model, r.Upstream, r.Tokens = "glm-5.3", zai, small
		})))
		flashCost := RequestCost(pricedOfRecord(makeRequest(func(r *shared.RequestRecord) {
			r.Model, r.Upstream, r.Tokens = "glm-5.3-flash", zai, small
		})))
		if glmCost == nil || flashCost == nil {
			t.Fatal("the run's own models: no price")
		}
		saving := float64(glmCost.USD)*2 - float64(flashCost.USD)*2
		if report.SavingUsd == nil {
			t.Fatal("savingUsd: no price")
		}
		closeTo(t, "savingUsd", float64(*report.SavingUsd), saving)
		if len(report.Examples) != 2 {
			t.Errorf("examples: got %d want 2", len(report.Examples))
		}
	})

	t.Run("skips runs that are too big", func(t *testing.T) {
		many := make([]shared.RequestRecord, 0, AdvisorLimits.MaxRequests+1)
		for i := range AdvisorLimits.MaxRequests + 1 {
			many = append(many, makeRequest(func(r *shared.RequestRecord) {
				r.ID, r.AgentID, r.Model = fmt.Sprintf("r%d", i), "w1", "glm-5.3"
			}))
		}
		if got := Advise(many, nil).Candidates; got != 0 {
			t.Errorf("too many requests: got %d candidates want 0", got)
		}
		loud := []shared.RequestRecord{
			makeRequest(func(r *shared.RequestRecord) {
				r.AgentID, r.Model = "w1", "glm-5.3"
				r.Tokens = shared.Tokens{Output: 9000}
			}),
		}
		report := Advise(loud, nil)
		if report.RunsChecked != 1 || report.Candidates != 0 || report.SavingUsd != nil {
			t.Errorf("too much output: got %d/%d/%v want 1/0/null",
				report.RunsChecked, report.Candidates, report.SavingUsd)
		}
	})

	t.Run("orders equal-run models the way localeCompare does, not by bytes", func(t *testing.T) {
		requests := []shared.RequestRecord{
			makeRequest(func(r *shared.RequestRecord) {
				r.ID, r.AgentID, r.Model, r.Upstream, r.Tokens = "1", "w1", "GLM-4.6", zai, small
			}),
			makeRequest(func(r *shared.RequestRecord) {
				r.ID, r.AgentID, r.Model, r.Upstream, r.Tokens = "2", "w2", "glm-4.6", zai, small
			}),
		}
		report := Advise(requests, nil)
		if len(report.ByModel) != 2 {
			t.Fatalf("byModel: got %d rows want 2", len(report.ByModel))
		}
		// one run each: localeCompare weighs case behind the letters, so the lowercase id
		// leads where byte order would put the capital first
		if report.ByModel[0].Model != "glm-4.6" || report.ByModel[1].Model != "GLM-4.6" {
			var order []string
			for _, row := range report.ByModel {
				order = append(order, row.Model)
			}
			t.Errorf("byModel order = %v, want [glm-4.6 GLM-4.6]", order)
		}
	})

	t.Run("breaks a dominant-model count tie the way localeCompare does, not by bytes", func(t *testing.T) {
		run := &advisorRun{requests: []shared.RequestRecord{
			makeRequest(func(r *shared.RequestRecord) {
				r.ID, r.AgentID, r.Model = "1", "w1", "GLM-4.6"
			}),
			makeRequest(func(r *shared.RequestRecord) {
				r.ID, r.AgentID, r.Model = "2", "w1", "glm-4.6"
			}),
		}}
		if got := dominantModel(run); got != "glm-4.6" {
			t.Errorf("dominantModel = %q, want %q (byte order would pick GLM-4.6)", got, "glm-4.6")
		}
	})
}
