package cost

import (
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/core/providers"
	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// providerOfOffering is the provider plugin behind each offering a plugin bills against;
// Anthropic has no plugin and so no main model to fall back to.
var providerOfOffering = map[OfferingID]string{
	OfferingZai:      "zai",
	OfferingMoonshot: "kimi",
	OfferingDeepseek: "deepseek",
	OfferingMinimax:  "minimax",
	OfferingQwen:     "qwen",
}

// familyOf is the catalog model an id the catalog does not know prices as, and the offering
// that sells it. The family is the longest dash-bounded prefix of the id some catalog id
// starts with ("claude-opus-9-9" → "claude-opus"), its newest id (the greatest in string
// order) the rate; an id no prefix reaches takes its provider's main model. An offering
// bounds the search to its own sheets. False when nothing claims the id.
func familyOf(offering *OfferingID, model string) (string, OfferingID, bool) {
	id := NormalizeModel(model)
	for prefix := id; prefix != ""; {
		cut := strings.LastIndex(prefix, "-")
		if cut <= 0 {
			break
		}
		prefix = prefix[:cut]
		if match, at, ok := newestWithPrefix(offering, prefix+"-"); ok {
			return match, at, true
		}
	}
	plugin, ok := "", false
	if offering != nil {
		plugin, ok = providerOfOffering[*offering]
	} else {
		plugin, ok = PluginOf(id)
	}
	if !ok {
		return "", "", false
	}
	provider, ok := providers.ByName(plugin)
	if !ok {
		return "", "", false
	}
	main := provider.Catalog[domain.ModelTierMain].ID
	for _, candidate := range Offerings {
		if _, sells := candidate.Models[main]; sells && (offering == nil || candidate.ID == *offering) {
			return main, candidate.ID, true
		}
	}
	return "", "", false
}

// newestWithPrefix is the greatest catalog id that starts with prefix, on the offering when
// one is named.
func newestWithPrefix(offering *OfferingID, prefix string) (string, OfferingID, bool) {
	best, at := "", OfferingID("")
	for _, candidate := range Offerings {
		if offering != nil && candidate.ID != *offering {
			continue
		}
		for key := range candidate.Models {
			if strings.HasPrefix(NormalizeModel(key), prefix) && NormalizeModel(key) > NormalizeModel(best) {
				best, at = key, candidate.ID
			}
		}
	}
	return best, at, best != ""
}
