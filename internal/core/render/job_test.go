package render

import (
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/core/app"
	"github.com/MuhmdRaouf/huddle/internal/core/testkit"
	"github.com/MuhmdRaouf/huddle/internal/domain"
)

func TestJobViewGoldens(t *testing.T) {
	for i, row := range goldenRows(t, "jobview") {
		input := inputOf[struct {
			Provider string       `json:"provider"`
			Row      app.BoardRow `json:"row"`
			Now      int64        `json:"now"`
		}](t, row)
		checkString(t, i, JobView(providerOf(t, input.Provider), input.Row, input.Now), row.Output)
	}
}

// A running job with no live driver is flagged stale, with the way out.
func TestJobViewFlagsStaleDrivers(t *testing.T) {
	job := testkit.AJob(func(job *domain.Job) { job.State = domain.JobStateRunning })
	text := JobView(testkit.ReferenceProvider, app.BoardRow{Job: job}, 0)
	if !strings.Contains(text, "stale") {
		t.Errorf("no stale flag:\n%s", text)
	}
}
