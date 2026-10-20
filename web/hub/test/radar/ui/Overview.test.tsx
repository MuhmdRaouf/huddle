// Overview.test.tsx — the Radar tab's front page: the five stat cards over the data the shell and
// the controller hold, the spend chart and its fallback, the sessions table that opens the Session
// screen, the alert card, the routers list and the top tools.

import { describe, expect, it } from "vite-plus/test";
import { AppContext, type AppValue } from "../../../src/radar/app/context.ts";
import { OverviewView } from "../../../src/radar/app/views/Overview.tsx";
import { type ClientState, initialClientState } from "../../../src/radar/state.ts";
import { makeCtx, makeRegistry, makeState, renderIn } from "../../helpers.tsx";
import { uiFixtures } from "../helpers.ts";

const NOW = 1_790_000_000_000;
const fixtures = uiFixtures();

/** The registry rows the sessions card and the table's machine names read. */
const registrySessions = [
  { session_id: "s-lead", machine: "studio", state: "working", last_event: "Stop", last_event_at: "" },
  { session_id: "s-kanban", machine: "laptop", state: "needs-you", last_event: "Stop", last_event_at: "" },
];

/** Renders the Overview under both contexts it reads: radar's own and the huddle shell's. */
function overview(state: Partial<ClientState>, sessions = registrySessions) {
  const value: AppValue = {
    state: { ...initialClientState(), ...state },
    now: NOW,
    url: "127.0.0.1:18860",
    act: () => {},
  };
  return renderIn(
    <AppContext.Provider value={value}>
      <OverviewView />
    </AppContext.Provider>,
    makeCtx(makeState(), { registry: makeRegistry({ sessions }) }),
  );
}

/** An unpriced request at the range's far edge, so the requests in memory cover the whole hour
 *  and the cards read them instead of falling back to the history route's all-time words. */
function oldEdge() {
  return priced("mystery-model", NOW - 3_700_000);
}

/** One request the shared price table answers with whole cents, in the default range, on the
 *  model named; the upstream decides the table the model prices in. */
function priced(model: string, ts: number, upstream = "https://api.anthropic.com") {
  return {
    id: `req-${model}-${ts}`,
    sessionId: "s-lead",
    agentId: "main",
    model,
    upstream,
    ts,
    latencyMs: 100,
    tokens: { input: 900_000, output: 50_000, cacheRead: 7_050_000, cacheWrite: 0 },
    stopReason: "end_turn" as const,
    provider: "Anthropic",
  };
}

describe("the radar Overview", () => {
  it("shows the five cards: spend against its budget, requests with their failures, tokens with the cache share, sessions with who needs the owner, routers with the stopped one named", () => {
    const { container } = overview({
      summary: fixtures.summary,
      sessions: [
        { ...fixtures.sessionListItem, id: "s-lead", live: true },
        { ...fixtures.sessionListItem, id: "s-kanban", live: false },
      ],
      spendToday: 18.4,
      budgetStatus: {
        version: 1,
        updatedAt: NOW,
        stopped: ["provider:kimi"],
        spend: [
          {
            id: "b1",
            spentUsd: 18.4,
            limitUsd: 40,
            pct: 46,
            scope: "total",
            period: "day",
            action: "warn",
            periodStart: NOW - 3600_000,
          },
        ],
      },
      providers: ["zai", "kimi", "deepseek"],
      requests: [oldEdge(), priced("claude-opus-5-5", NOW - 60_000)],
    });
    expect(container.querySelector('[data-stat="spend today"] .num')?.textContent).toBe("$18.40");
    expect(container.querySelector('[data-stat="spend today"] .bar-seg')?.getAttribute("style")).toContain(
      "width: 46%",
    );
    expect(container.textContent).toContain("46% of $40.00");
    expect(container.querySelector('[data-stat="requests"] .num')?.textContent).toBe("1");
    expect(container.querySelector('[data-stat="requests"]')?.textContent).toContain("0 failed");
    expect(container.querySelector('[data-stat="tokens"] .num')?.textContent).toBe("8M");
    expect(container.querySelector('[data-stat="tokens"]')?.textContent).toContain("88% from cache");
    expect(container.querySelector('[data-stat="sessions"] .num')?.textContent).toBe("2");
    expect(container.querySelector('[data-stat="sessions"]')?.textContent).toContain("1 need you");
    expect(container.querySelector('[data-stat="routers"] .num')?.textContent).toBe("2 of 3");
    expect(container.querySelector('[data-stat="routers"]')?.textContent).toContain("kimi budget stopped");
  });

  it("stacks the range's spend by hour and model, with a legend that prices each band", () => {
    const { container } = overview({
      summary: fixtures.summary,
      requests: [
        oldEdge(),
        priced("claude-opus-5-5", NOW - 3_000_000),
        priced("glm-5.3", NOW - 2_000_000, "https://api.z.ai/api/anthropic"),
        priced("claude-sonnet-5-5", NOW - 1_000_000),
      ],
    });
    const chart = container.querySelector('section[aria-labelledby="ovw-chart"]');
    expect(chart).not.toBeNull();
    // one band per top model, each in its own hour bucket
    expect(chart?.querySelectorAll("rect")).toHaveLength(3);
    const legend = [...(chart?.querySelectorAll("span.flex") ?? [])].map((el) => el.textContent?.trim());
    // biggest spend first, each band priced by the shared table at list price
    expect(legend).toEqual(["Opus 5.5 $6.01", "GLM 5.3 $3.31", "Sonnet 5.5 $3.00"]);
  });

  it("falls back to the token flow when the range outruns the requests in memory", () => {
    const { container } = overview({
      summary: fixtures.summary,
      range: { preset: "7d", from: 0, to: null },
      requests: [priced("claude-opus-5-5", NOW - 60_000)],
    });
    expect(container.textContent).toContain("Token flow");
    expect(container.querySelector('section[aria-labelledby="ovw-chart"]')).toBeNull();
  });

  it("lists the top sessions by spend, each row opening the Session screen with its machine", () => {
    const { container } = overview({
      summary: fixtures.summary,
      sessions: [
        {
          ...fixtures.sessionListItem,
          id: "s-lead",
          name: "lead",
          costUsd: 9.8,
          requestCount: 412,
          model: "claude-opus-5-5",
          live: true,
        },
        {
          ...fixtures.sessionListItem,
          id: "s-kanban",
          name: "kanban-e2e",
          costUsd: 2.1,
          requestCount: 288,
          model: "glm-5.3",
          live: false,
        },
      ],
    });
    const rows = [...container.querySelectorAll('a[href^="#/now/"]')] as HTMLAnchorElement[];
    expect(rows.map((a) => a.getAttribute("href"))).toEqual(["#/now/s-lead", "#/now/s-kanban"]);
    expect(rows[0]?.textContent).toContain("lead");
    expect(rows[0]?.textContent).toContain("studio");
    expect(rows[1]?.textContent).toContain("kanban-e2e · laptop");
  });

  it("raises the budget from a budget alert, and dismisses any alert", () => {
    const { container } = overview({
      summary: fixtures.summary,
      alerts: [{ ...fixtures.alert, kind: "budget" }],
    });
    const card = container.querySelector('[aria-labelledby="ovw-alert"]');
    expect(card?.textContent).toContain(fixtures.alert.detail);
    expect((container.querySelector("a[data-alert-action]") as HTMLAnchorElement)?.getAttribute("href")).toBe(
      "#/settings/radar",
    );
    // a non-budget alert leads to the Alerts view instead
    const other = overview({
      summary: fixtures.summary,
      alerts: [{ ...fixtures.alert, kind: "stuck", id: "a2" }],
    });
    expect(other.container.querySelector("a[data-alert-action]")).toBeNull();
    expect(other.container.querySelector('[data-action="tab"][data-value="alerts"]')).not.toBeNull();
  });

  it("names the routers with their ports, the stopped one last with its word", () => {
    const { container } = overview({
      summary: fixtures.summary,
      providers: ["zai", "kimi"],
      budgetStatus: { version: 1, updatedAt: NOW, stopped: ["provider:kimi"], spend: [] },
    });
    const rows = [...container.querySelectorAll("[data-router]")];
    expect(rows.map((row) => row.getAttribute("data-router"))).toEqual(["kimi", "zai"]);
    expect(rows[0]?.textContent).toContain("kimi · :18788");
    expect(rows[0]?.textContent).toContain("budget stopped");
    expect(rows[1]?.textContent).toContain("zai · :18787");
    expect(rows[1]?.textContent).toContain("up");
  });

  it("bars the tools the agents used most", () => {
    const { container } = overview({
      summary: fixtures.summary,
      models: {
        models: [],
        upstreams: [],
        tools: [
          { name: "Bash", count: 318, failures: 0 },
          { name: "Read", count: 241, failures: 2 },
        ],
      },
    });
    const tools = container.querySelector('section[aria-labelledby="ovw-tools"]');
    expect(tools?.textContent).toContain("Bash");
    expect(tools?.textContent).toContain("318");
    expect(tools?.querySelectorAll(".bar-seg").length).toBe(2);
  });

  it("says where a budget comes from when none is set", () => {
    const { container } = overview({ summary: fixtures.summary });
    const link = container.querySelector('[data-stat="spend today"] a');
    expect(link?.getAttribute("href")).toBe("#/settings/radar");
    expect(link?.textContent).toContain("No budget set");
  });

  it("counts the range's failures in the requests card's tone", () => {
    const { container } = overview({
      summary: fixtures.summary,
      requests: [
        oldEdge(),
        priced("claude-opus-5-5", NOW - 120_000),
        {
          ...priced("claude-opus-5-5", NOW - 60_000),
          id: "req-failed",
          stopReason: "error" as const,
        },
      ],
    });
    expect(container.querySelector('[data-stat="requests"] .num')?.textContent).toBe("2");
    expect(container.querySelector('[data-stat="requests"]')?.textContent).toContain("1 failed");
  });

  it("reads a history range's counts from the fetched flow, and says all time", () => {
    const { container } = overview({
      summary: fixtures.summary,
      range: { preset: "7d", from: 0, to: null },
      flow: {
        key: "range=7d",
        series: {
          from: NOW - 7 * 86_400_000,
          to: NOW,
          bucketMs: 10_800_000,
          requests: [4, 5, 3],
          kinds: { input: [], output: [], cacheRead: [], cacheWrite: [] },
        },
      },
      requests: [priced("claude-opus-5-5", NOW - 60_000)],
    });
    expect(container.querySelector('[data-stat="requests"] .num')?.textContent).toBe("12");
    expect(container.querySelector('[data-stat="requests"]')?.textContent).toContain("all time");
    expect(container.querySelector('[data-stat="tokens"]')?.textContent).toContain("all time");
    expect(container.textContent).toContain("Token flow");
  });

  it("says nothing yet when a history range has no summary to read", () => {
    const { container } = overview({
      summary: null,
      range: { preset: "7d", from: 0, to: null },
      requests: [priced("claude-opus-5-5", NOW - 60_000)],
    });
    expect(container.querySelector('[data-stat="tokens"]')?.textContent).toContain("nothing yet");
  });

  it("joins the models past the top three into one other band", () => {
    const { container } = overview({
      summary: fixtures.summary,
      requests: [
        oldEdge(),
        priced("claude-opus-5-5", NOW - 3_000_000),
        priced("glm-5.3", NOW - 2_000_000, "https://api.z.ai/api/anthropic"),
        priced("claude-sonnet-5-5", NOW - 1_000_000),
        priced("claude-fable-5", NOW - 500_000),
      ],
    });
    const chart = container.querySelector('section[aria-labelledby="ovw-chart"]');
    const legend = [...(chart?.querySelectorAll("span.flex") ?? [])].map((el) => el.textContent?.trim());
    expect(legend).toHaveLength(4);
    expect(legend[3]).toContain("other");
    expect(chart?.querySelectorAll("rect")).toHaveLength(4);
  });

  it("offers the empty words while the lists have nothing: no providers, no tools, no sessions", () => {
    const { container } = overview(
      { summary: fixtures.summary, providers: [], sessions: [], models: null },
      [],
    );
    expect(container.textContent).toContain("No provider plugins yet");
    expect(container.textContent).toContain("No tool calls yet");
    expect(container.textContent).toContain("No sessions yet");
    expect(container.querySelector('[data-stat="sessions"]')?.textContent).toContain("none need you");
    expect(container.querySelector('[data-stat="routers"] .num')?.textContent).toBe("–");
  });

  it("marks an ended session absent from the registry with the hollow dot and its word", () => {
    const { container } = overview(
      {
        summary: fixtures.summary,
        sessions: [{ ...fixtures.sessionListItem, id: "s-gone", name: "gone", live: false, costUsd: 1 }],
      },
      [],
    );
    const dot = container.querySelector('[role="img"][aria-label="ended"]');
    expect(dot).not.toBeNull();
    expect(container.textContent).toContain("gone");
  });
});

describe("the Overview's plan limits card", () => {
  const quota = {
    session_id: "s-lead",
    five_hour: 42,
    five_hour_resets_at: NOW + (2 * 60 + 14) * 60_000,
    seven_day: 93,
    seven_day_resets_at: NOW + 5 * 86_400_000,
    at: NOW - 60_000,
  };

  it("shows both windows with their percent, their colour and their reset countdown", () => {
    const { container } = overview({ quota });
    const card = container.querySelector('[data-quota=""]');
    expect(card?.textContent).toContain("Plan limits");
    const windows = [...container.querySelectorAll("[data-quota-window]")];
    expect(windows.map((row) => row.getAttribute("data-quota-window"))).toEqual(["5-hour", "weekly"]);
    const five = windows[0]?.textContent ?? "";
    expect(five).toContain("42%");
    expect(five).toContain("resets in 2h 14m");
    const week = windows[1]?.textContent ?? "";
    expect(week).toContain("93%");
    // the colours follow the thresholds: ok under 70, danger past 90
    expect(windows[0]?.querySelector(".bar-seg")?.getAttribute("style")).toContain("var(--success)");
    expect(windows[1]?.querySelector(".bar-seg")?.getAttribute("style")).toContain("var(--danger)");
  });

  it("hides the card when the snapshot is older than six hours", () => {
    const { container } = overview({ quota: { ...quota, at: NOW - 7 * 3_600_000 } });
    expect(container.querySelector('[data-quota=""]')).toBeNull();
  });

  it("hides the card when nothing was reported, or the report is empty", () => {
    expect(overview({}).container.querySelector('[data-quota=""]')).toBeNull();
    expect(
      overview({
        quota: {
          session_id: "",
          five_hour: null,
          five_hour_resets_at: null,
          seven_day: null,
          seven_day_resets_at: null,
          at: 0,
        },
      }).container.querySelector('[data-quota=""]'),
    ).toBeNull();
  });

  it("keeps the meter readable when a window carries no number", () => {
    const { container } = overview({
      quota: { ...quota, seven_day: null, seven_day_resets_at: null },
    });
    const windows = [...container.querySelectorAll("[data-quota-window]")];
    expect(windows[1]?.textContent).toContain("–");
    expect(windows[1]?.querySelector(".bar-seg")).toBeNull();
  });
});
