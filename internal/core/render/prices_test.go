package render

import (
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/core/providers"
	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// Every PRICE_TABLE entry names an https pricing page and the day it was read; an unverified
// figure says which part was inferred.
func TestPriceTableEntriesCiteTheirSource(t *testing.T) {
	https := regexp.MustCompile(`^https://`)
	day := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	for model, entry := range PriceTable {
		if !https.MatchString(entry.Source) {
			t.Errorf("%s: source %q is not an https page", model, entry.Source)
		}
		if !day.MatchString(entry.Retrieved) {
			t.Errorf("%s: retrieved %q is not a day", model, entry.Retrieved)
		}
		if entry.Retrieved > PricesRetrieved {
			t.Errorf("%s: retrieved %q is after %s", model, entry.Retrieved, PricesRetrieved)
		}
		if !entry.Verified && (entry.Note == nil || !strings.Contains(*entry.Note, "inferred")) {
			t.Errorf("%s: unverified without an inference note", model)
		}
	}
}

// The flat table prices every catalog id the five provider plugins ship: the compiled provider
// table (internal/core/providers) is the data each plugin's catalog carries.
func TestPriceTableCoversEveryPluginCatalog(t *testing.T) {
	for _, name := range providers.Names {
		provider, ok := providers.ByName(name)
		if !ok {
			t.Fatalf("%s: providers has no such provider", name)
		}
		for _, tier := range []domain.ModelTier{domain.ModelTierMain, domain.ModelTierFlash} {
			id := provider.Catalog[tier].ID
			if _, ok := PriceOf(id); !ok {
				t.Errorf("%s: %s has no price", name, id)
			}
		}
	}
}

// An unknown model costs nothing rather than a wrong sum.
func TestUnknownModelCostsNothing(t *testing.T) {
	tokens := TokenCounts{InputTokens: 1_000_000, OutputTokens: 1_000_000, CacheReadTokens: 1_000_000}
	if got := CostUsdOf("not-a-model", tokens); got != 0 {
		t.Errorf("costUsdOf(not-a-model) = %v, want 0", got)
	}
	if _, ok := PriceOf("toString"); ok {
		t.Errorf(`priceOf("toString") is defined`)
	}
}

// Every offering names an https page, the day it was read, and only verified or noted figures.
func TestOfferingsCiteTheirSource(t *testing.T) {
	https := regexp.MustCompile(`^https://`)
	host := regexp.MustCompile(`^[a-z.-]+$`)
	if len(Offerings) == 0 {
		t.Fatal("no offerings")
	}
	for _, offering := range Offerings {
		if !https.MatchString(offering.Source) {
			t.Errorf("%s: source %q is not an https page", offering.ID, offering.Source)
		}
		if offering.Retrieved != PricesRetrieved {
			t.Errorf("%s: retrieved %q, want %s", offering.ID, offering.Retrieved, PricesRetrieved)
		}
		if !host.MatchString(offering.Host) {
			t.Errorf("%s: host %q is not a plain host name", offering.ID, offering.Host)
		}
		if len(offering.Models) == 0 {
			t.Errorf("%s: no model sheets", offering.ID)
		}
		for id, sheet := range offering.Models {
			if sheet.Verified {
				continue
			}
			if sheet.Note == nil || !strings.Contains(*sheet.Note, "inferred") {
				t.Errorf("%s/%s: unverified without an inference note", offering.ID, id)
			}
		}
	}
}

func TestOfferingOfHostGoldens(t *testing.T) {
	for i, row := range goldenRows(t, "offeringofhost") {
		input := inputOf[struct {
			URLOrHost string `json:"urlOrHost"`
		}](t, row)
		got, ok := OfferingOfHost(input.URLOrHost)
		want := outputOf[*string](t, row.Output)
		switch {
		case ok && want != nil:
			if string(got) != *want {
				t.Errorf("row %d: got %q, want %q", i, got, *want)
			}
		case !ok && want == nil:
		default:
			t.Errorf("row %d: got (%q, %v), want %v", i, got, ok, want)
		}
	}
}

func TestPriceRequestGoldens(t *testing.T) {
	for i, row := range goldenRows(t, "pricerequest") {
		input := inputOf[struct {
			Request PriceRequest `json:"request"`
		}](t, row)
		got := PriceRequestOf(input.Request)
		want := outputOf[*PriceResult](t, row.Output)
		switch {
		case got == nil && want == nil:
		case got == nil || want == nil:
			t.Errorf("row %d: got %v, want %v", i, got, want)
		default:
			if got.USD != want.USD || !slices.Equal(got.Detail, want.Detail) {
				t.Errorf("row %d:\ngot  (%v, %q)\nwant (%v, %q)", i, got.USD, got.Detail, want.USD, want.Detail)
			}
		}
	}
}

func TestPriceOfGoldens(t *testing.T) {
	for i, row := range goldenRows(t, "priceof") {
		input := inputOf[struct {
			Model string `json:"model"`
		}](t, row)
		got, ok := PriceOf(input.Model)
		want := outputOf[*PriceEntry](t, row.Output)
		switch {
		case ok && want != nil:
			if !reflect.DeepEqual(got, *want) {
				t.Errorf("row %d:\ngot  %#v\nwant %#v", i, got, *want)
			}
		case !ok && want == nil:
		default:
			t.Errorf("row %d: got (%#v, %v), want %v", i, got, ok, want)
		}
	}
}

func TestCostUsdOfGoldens(t *testing.T) {
	for i, row := range goldenRows(t, "costusdof") {
		input := inputOf[struct {
			Model  string      `json:"model"`
			Tokens TokenCounts `json:"tokens"`
		}](t, row)
		got := CostUsdOf(input.Model, input.Tokens)
		want := outputOf[float64](t, row.Output)
		if got != want {
			t.Errorf("row %d: got %v, want %v", i, got, want)
		}
	}
}

func TestEstimateTextGoldens(t *testing.T) {
	for i, row := range goldenRows(t, "estimatetext") {
		input := inputOf[struct {
			Model  string      `json:"model"`
			Tokens TokenCounts `json:"tokens"`
		}](t, row)
		checkString(t, i, EstimateText(input.Model, input.Tokens), row.Output)
	}
}
