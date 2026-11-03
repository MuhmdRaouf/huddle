package lane

import (
	"strconv"

	"github.com/MuhmdRaouf/huddle/internal/core/providers"
	"github.com/MuhmdRaouf/huddle/internal/core/router"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/insights/report"
)

// Choices lists the providers huddle-router has enabled, each with its main and flash model; a
// missing or unreadable providers.json lists none. The first model of a choice is its default.
func Choices(env domain.EnvLookup) []report.ProviderChoice {
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

// URL is the address of huddle-router on this machine.
func URL(env domain.EnvLookup) (string, error) {
	port, err := router.PortOf(env)
	if err != nil {
		return "", err
	}
	return "http://127.0.0.1:" + strconv.Itoa(port), nil
}
