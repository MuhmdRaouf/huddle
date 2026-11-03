package server

// The insights preview shows the owner what a Run would send before any model is asked: the
// provider and model, the window, the counts, the estimated input and its price, and the label
// map. It reads the mounted radar tab's rows and calls no lane.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/insights/report"
	"github.com/MuhmdRaouf/huddle/internal/insights/sources"
	"github.com/MuhmdRaouf/huddle/internal/radar/history"
	"github.com/MuhmdRaouf/huddle/internal/radar/ledger"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
)

const previewSentinel = "SENTINEL-0x42"

// seededRequests is more than the one page of 1000 rows any list of the radar tab holds.
const seededRequests = 1250

// previewFixture is a radar home holding a seeded ledger and history, the hub registry, a router
// home that enables the given providers, and a fake lane on the router port that counts the
// calls it gets. No radar tab is mounted: the preview reads the stores themselves.
type previewFixture struct {
	t     *testing.T
	srv   *server
	clock time.Time
	lane  *atomic.Int64
	env   domain.EnvLookup
}

func newPreviewFixture(t *testing.T, enabled ...string) *previewFixture {
	t.Helper()
	homeRoot := t.TempDir()
	reg, err := hub.Open(context.Background(), home.Home{Root: homeRoot}.HubDB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	routerHome, radarHome := t.TempDir(), t.TempDir()
	list, err := json.Marshal(map[string][]string{"providers": enabled})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(routerHome, "providers.json"), list, 0o600); err != nil {
		t.Fatal(err)
	}
	fix := &previewFixture{t: t, clock: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC), lane: new(atomic.Int64)}
	lane := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fix.lane.Add(1)
	}))
	t.Cleanup(lane.Close)
	laneURL, err := url.Parse(lane.URL)
	if err != nil {
		t.Fatal(err)
	}
	environ := []string{
		"HUDDLE_ROUTER_HOME=" + routerHome,
		"HUDDLE_ROUTER_PORT=" + laneURL.Port(),
		"RADAR_HOME=" + radarHome,
	}
	fix.env = isoenv.Lookup(environ)
	fix.srv = &server{registry: reg, now: func() time.Time { return fix.clock }, o: Options{Home: homeRoot, Environ: environ}}
	return fix
}

// seed files seededRequests requests in the last week over two sessions, 1100 tool calls, and
// one request a month old; the ledger rows carry captured text holding the sentinel.
func (f *previewFixture) seed() {
	f.t.Helper()
	ctx := context.Background()
	st, err := ledger.Open(paths.LedgerPath(ctx, f.env))
	if err != nil {
		f.t.Fatal(err)
	}
	inside := f.clock.Add(-2 * 24 * time.Hour).UnixMilli()
	what := "prompt: " + previewSentinel
	for i := 0; i < seededRequests; i++ {
		session := "s-a"
		if i%10 == 0 {
			session = "s-b"
		}
		if err := st.Upsert(ctx, ledger.Row{
			ID: fmt.Sprintf("r-%05d", i), StartedAt: inside + int64(i), DurationMs: 90, Status: 200,
			Model: "glm-5.3", Provider: "zai", SessionID: session, Input: 5_000, Output: 200,
			PriceUSD: 0.002, What: &what,
		}); err != nil {
			f.t.Fatal(err)
		}
	}
	old := f.clock.Add(-30 * 24 * time.Hour).UnixMilli()
	if err := st.Upsert(ctx, ledger.Row{ID: "r-old", StartedAt: old, SessionID: "s-a", Input: 1}); err != nil {
		f.t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		f.t.Fatal(err)
	}
	h, err := history.OpenHistory(paths.HistoryPath(ctx, f.env), nil)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, n := range []history.NodeUpsert{
		{ID: "n-a", Kind: shared.NodeMain, SessionID: "s-a", AgentID: "main", Repo: &previewRepo, StartedAt: &inside},
		{ID: "n-b", Kind: shared.NodeMain, SessionID: "s-b", AgentID: "main", Cwd: &previewRepo, StartedAt: &inside},
	} {
		if err := h.UpsertNode(n); err != nil {
			f.t.Fatal(err)
		}
	}
	for i := 0; i < 1100; i++ {
		tool := shared.ToolCallRecord{ID: fmt.Sprintf("t-%05d", i), SessionID: "s-a", Name: "Bash", StartedAt: inside + int64(i), Ok: true}
		if err := h.PutTool("n-a", tool); err != nil {
			f.t.Fatal(err)
		}
	}
	if err := h.Close(); err != nil {
		f.t.Fatal(err)
	}
}

var previewRepo = "/Users/me/playground/huddle"

// preview asks GET /api/insights/preview the way the dashboard does.
func (f *previewFixture) preview(me auth.Who, query string) (int, report.Preview, string) {
	f.t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/insights/preview?"+query, nil)
	err := f.srv.insightsPreviewRoute(rec, req, me)
	status := rec.Code
	var herr *channel.HuddleError
	if errors.As(err, &herr) {
		return herr.Status, report.Preview{}, herr.Msg
	}
	if err != nil {
		f.t.Fatalf("GET /api/insights/preview: %v", err)
	}
	var out report.Preview
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		f.t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return status, out, rec.Body.String()
}

var previewOwner = auth.Who{Name: "owner", Root: true}

func TestInsightsShowWhatTheySendBeforeRun(t *testing.T) {
	bdd.Feature(t, "Insights show what they send before Run", func(f *bdd.F) {
		f.Scenario("the owner asks for the preview of a seeded ledger", func(s *bdd.S) {
			var (
				fix    *previewFixture
				status int
				got    report.Preview
				body   string
			)
			s.Given("a ledger with requests, GLM and Kimi enabled, and a fake lane", func(t *testing.T) {
				fix = newPreviewFixture(s.T, "zai", "kimi")
				fix.seed()
			})
			s.When("the owner asks for the preview", func(t *testing.T) {
				status, got, body = fix.preview(previewOwner, "")
			})
			s.Then("it names GLM and its model as the default and lists the enabled providers", func(t *testing.T) {
				if status != 200 || got.Provider != "zai" || got.Model != "glm-5.3" {
					t.Fatalf("status %d, %s %s; want 200, zai glm-5.3 (%s)", status, got.Provider, got.Model, body)
				}
				if len(got.Choices) != 2 || got.Choices[0].Provider != "zai" || got.Choices[1].Provider != "kimi" {
					t.Errorf("choices = %+v, want zai then kimi", got.Choices)
				}
			})
			s.And("it names the window, the sessions and the requests", func(t *testing.T) {
				if got.Window.Days != 7 || got.Sessions != 2 || got.Requests != seededRequests {
					t.Errorf("window %+v, sessions %d, requests %d; want 7 days, 2 sessions, all %d requests",
						got.Window, got.Sessions, got.Requests, seededRequests)
				}
			})
			s.And("it estimates the input tokens and their price", func(t *testing.T) {
				if got.InputTokens <= 0 || got.InputCostUSD == nil || *got.InputCostUSD <= 0 {
					t.Errorf("tokens %d, cost %v; want both above zero", got.InputTokens, got.InputCostUSD)
				}
			})
			s.And("it sizes the payload a Run would build from the same stores", func(t *testing.T) {
				ctx := context.Background()
				stores, err := sources.Open(paths.LedgerPath(ctx, fix.env), paths.HistoryPath(ctx, fix.env), fix.srv.registry)
				if err != nil {
					t.Fatal(err)
				}
				defer stores.Close()
				structure, labels, err := report.Build(ctx, stores.Sources(), report.LastDays(fix.clock, 7))
				if err != nil {
					t.Fatal(err)
				}
				want, err := report.NewPreview(structure, labels, got.Choices, "", "", fix.clock)
				if err != nil {
					t.Fatal(err)
				}
				if got.InputTokens != want.InputTokens || structure.Totals.Requests != seededRequests {
					t.Errorf("preview tokens %d over %d requests, a Run's payload %d over %d",
						got.InputTokens, got.Requests, want.InputTokens, structure.Totals.Requests)
				}
			})
			s.And("it shows the label map with labels for the repo and the sessions", func(t *testing.T) {
				if len(got.Labels.Repos) != 1 || got.Labels.Repos[0].Label != "repo-1" ||
					got.Labels.Repos[0].Value != previewRepo {
					t.Errorf("repo labels = %+v, want repo-1 for the huddle repo", got.Labels.Repos)
				}
				if len(got.Labels.Sessions) != 2 || got.Labels.Sessions[0].Label != "session-1" {
					t.Errorf("session labels = %+v, want session-1 and session-2", got.Labels.Sessions)
				}
			})
			s.And("it carries no captured session text", func(t *testing.T) {
				if strings.Contains(body, previewSentinel) || strings.Contains(body, "prompt") {
					t.Errorf("the preview carries session text: %s", body)
				}
			})
			s.And("the fake lane has counted zero calls", func(t *testing.T) {
				if n := fix.lane.Load(); n != 0 {
					t.Errorf("the lane counted %d calls, want 0", n)
				}
			})
		})

		f.Scenario("the owner picks another enabled provider and model", func(s *bdd.S) {
			var (
				fix    *previewFixture
				status int
				got    report.Preview
			)
			s.Given("a seeded ledger with GLM and Kimi enabled", func(t *testing.T) {
				fix = newPreviewFixture(s.T, "zai", "kimi")
				fix.seed()
			})
			s.When("the owner asks for Kimi's flash model", func(t *testing.T) {
				status, got, _ = fix.preview(previewOwner, "provider=kimi&model=kimi-k2.6")
			})
			s.Then("the preview names that provider and model", func(t *testing.T) {
				if status != 200 || got.Provider != "kimi" || got.Model != "kimi-k2.6" {
					t.Errorf("status %d, %s %s; want 200, kimi kimi-k2.6", status, got.Provider, got.Model)
				}
			})
			s.And("a provider that is not enabled is refused with a 400", func(t *testing.T) {
				if status, _, _ := fix.preview(previewOwner, "provider=deepseek"); status != 400 {
					t.Errorf("status = %d, want 400", status)
				}
			})
		})

		f.Scenario("no provider is enabled", func(s *bdd.S) {
			var (
				fix    *previewFixture
				status int
				got    report.Preview
			)
			s.Given("a seeded ledger and no enabled provider", func(t *testing.T) {
				fix = newPreviewFixture(s.T)
				fix.seed()
			})
			s.When("the owner asks for the preview", func(t *testing.T) {
				status, got, _ = fix.preview(previewOwner, "")
			})
			s.Then("it says so in one line and names no provider", func(t *testing.T) {
				if status != 200 || got.Note == "" || strings.Contains(got.Note, "\n") || got.Provider != "" || len(got.Choices) != 0 {
					t.Errorf("status %d, preview %+v; want one note line and no provider", status, got)
				}
			})
		})

		f.Scenario("a session's credential asks", func(s *bdd.S) {
			var (
				fix    *previewFixture
				status int
			)
			s.Given("a seeded ledger with GLM enabled", func(t *testing.T) {
				fix = newPreviewFixture(s.T, "zai")
				fix.seed()
			})
			s.When("a member's token asks for the preview", func(t *testing.T) {
				status, _, _ = fix.preview(auth.Who{Name: "worker-1", Kind: auth.KindMember}, "")
			})
			s.Then("it is refused with a 403", func(t *testing.T) {
				if status != 403 {
					t.Errorf("status = %d, want 403", status)
				}
			})
		})
	})
}
