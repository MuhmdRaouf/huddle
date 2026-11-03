package server

// The insights report's routes. The preview is the owner's look at what a Run would send: the
// payload built from the stores' structure, sized and priced, and no model asked. It runs when
// the owner asks for it and holds no timer.

import (
	"context"
	"errors"
	"net/http"

	"github.com/MuhmdRaouf/huddle/internal/core/providers"
	"github.com/MuhmdRaouf/huddle/internal/core/router"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/insights/report"
	"github.com/MuhmdRaouf/huddle/internal/insights/sources"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
)

// previewDays is the window a Run reports on.
const previewDays = 7

// insightsPreviewRoute answers GET /api/insights/preview for the owner alone. The provider and
// model queries pick among the enabled providers and default to GLM; with none enabled the
// answer is the one-line note and nothing is read.
func (s *server) insightsPreviewRoute(w http.ResponseWriter, r *http.Request, me auth.Who) error {
	if !(me.Root || me.Browser != "") {
		return &channel.HuddleError{Status: 403, Msg: "only the owner previews Insights"}
	}
	choices := s.reportChoices()
	if len(choices) == 0 {
		s.writeJSON(w, 200, report.NoProviderPreview(), nil)
		return nil
	}
	now := s.now()
	stores, err := s.openReportStores(r.Context())
	if err != nil {
		return err
	}
	defer func() { _ = stores.Close() }()
	structure, labels, err := report.Build(r.Context(), stores.Sources(), report.LastDays(now, previewDays))
	if err != nil {
		return err
	}
	query := r.URL.Query()
	preview, err := report.NewPreview(structure, labels, choices, query.Get("provider"), query.Get("model"), now)
	var unknown *report.UnknownChoiceError
	if errors.As(err, &unknown) {
		return &channel.HuddleError{Status: 400, Msg: unknown.Error()}
	}
	if err != nil {
		return err
	}
	s.writeJSON(w, 200, preview, nil)
	return nil
}

// openReportStores opens the stores a report reads, read-only, under the radar home the server
// was started with. The caller closes them.
func (s *server) openReportStores(ctx context.Context) (*sources.Stores, error) {
	env := bodiesEnv(s.o.Environ)
	var asks sources.AskHistory
	if s.registry != nil {
		asks = s.registry
	}
	return sources.Open(paths.LedgerPath(ctx, env), paths.HistoryPath(ctx, env), asks)
}

// reportChoices lists the providers huddle-router has enabled, each with its main and flash
// model. A server that names no environment cannot be told where the router's files live and
// reads none, so it never reads the owner's.
func (s *server) reportChoices() []report.ProviderChoice {
	if len(s.o.Environ) == 0 {
		return nil
	}
	env := bodiesEnv(s.o.Environ)
	enabled, err := router.ReadEnabled(env)
	if err != nil {
		return nil
	}
	var choices []report.ProviderChoice
	for _, name := range enabled {
		provider, ok := providers.ByName(name)
		if !ok {
			continue
		}
		provider = domain.ResolveProvider(provider, env)
		choices = append(choices, report.ProviderChoice{
			Provider: name,
			Name:     provider.Display,
			Models:   []string{provider.Catalog[domain.ModelTierMain].ID, provider.Catalog[domain.ModelTierFlash].ID},
		})
	}
	return choices
}
