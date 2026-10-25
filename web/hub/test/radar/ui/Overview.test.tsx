// Overview.test.tsx — the Radar tab's front page: the stat cards over the data the shell and
// the controller hold, Anthropic's limits tile first, the phone's three tiles under one window label, the spend chart and its
// fallback, the sessions table that opens the Session screen, the alert card, the routers list and
// the top tools.

import { waitFor } from "@testing-library/preact";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { AppContext, type AppValue } from "../../../src/radar/app/context.ts";
import { OverviewView } from "../../../src/radar/app/views/Overview.tsx";
import { fmtUsd } from "../../../src/radar/fmt.ts";
import { type ClientState, initialClientState } from "../../../src/radar/state.ts";
import { legacyClasses, makeCtx, makeRegistry, makeState, renderIn, stubMedia } from "../../helpers.tsx";
import { uiFixtures } from "../helpers.ts";
import { expectTokensOnly } from "./tokens.ts";

const NOW = 1_790_000_000_000;
const fixtures = uiFixtures();

/** The registry rows the sessions card and the table's machine names read. */
const registrySessions = [
  { session_id: "s-lead", machine: "studio", state: "working", last_event: "Stop", last_event_at: "" },
  { session_id: "s-kanban", machine: "laptop", state: "needs-you", last_event: "Stop", last_event_at: "" },
];

/** Renders the Overview under both contexts it reads: radar's own and the huddle shell's. The
 *  shell's api answers the hub's /health with `health`, the router part the routers tile reads. */
function overview(state: Partial<ClientState>, sessions = registrySessions, health: unknown = {}) {
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
    makeCtx(makeState(), {
      registry: makeRegistry({ sessions }),
      api: {
        api: (path) => (path === "/health" ? Promise.resolve(health) : Promise.resolve({})),
        op: () => Promise.resolve({}),
        channelPath: (ch, p) => `/api/c/${ch}${p}`,
        channelHref: (ch, p) => `#/channels/${ch}${p}`,
      },
    }),
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
    ttfbMs: null,
    ttftMs: null,
    tokens: { input: 900_000, output: 50_000, cacheRead: 7_050_000, cacheWrite: 0 },
    stopReason: "end_turn" as const,
    provider: "Anthropic",
  };
}

describe("the radar Overview", () => {
  it("shows the five cards: spend against its budget, requests with their failures, tokens with the cache share, sessions live with who needs the owner, routers with the stopped one named", async () => {
    const { container } = overview(
      {
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
        providers: ["zai", "kimi", "deepseek", "minimax", "qwen"],
        requests: [oldEdge(), priced("claude-opus-5-5", NOW - 60_000)],
      },
      registrySessions,
      { ok: true, router: { running: true, providers: ["zai", "kimi", "deepseek"] } },
    );
    expect(container.querySelector('[data-stat="spend today"] .num')?.textContent).toBe("$18.40");
    expect(container.querySelector('[data-stat="spend today"] .bar-seg')?.getAttribute("style")).toContain(
      "width: 46%",
    );
    expect(container.textContent).toContain("46% of $40.00");
    // the two range-scoped tiles name the window they read, the default hour included
    expect(container.querySelector('[data-stat="requests"]')?.textContent).toContain("Requests · last hour");
    expect(container.querySelector('[data-stat="tokens"]')?.textContent).toContain("Tokens · last hour");
    expect(container.querySelector('[data-stat="requests"] .num')?.textContent).toBe("1");
    expect(container.querySelector('[data-stat="requests"]')?.textContent).toContain("0 failed");
    expect(container.querySelector('[data-stat="tokens"] .num')?.textContent).toBe("8M");
    expect(container.querySelector('[data-stat="tokens"]')?.textContent).toContain("88% from cache");
    expect(container.querySelector('[data-stat="sessions"] .num')?.textContent).toBe("1");
    expect(container.querySelector('[data-stat="sessions"]')?.textContent).toContain("1 need you");
    await waitFor(() =>
      expect(container.querySelector('[data-stat="routers"] .num')?.textContent).toBe("2 of 3"),
    );
    expect(container.querySelector('[data-stat="routers"]')?.textContent).toContain("kimi budget stopped");
  });

  it("says how many of today's calls the spend tile could not price, and nothing when it priced them all", () => {
    const tile = (spendUnpriced: number) =>
      overview({
        summary: fixtures.summary,
        spendToday: 12.5,
        spendUnpriced,
      }).container.querySelector('[data-stat="spend today"] .num')?.textContent;
    expect(tile(3)).toBe("$12.50 · 3 unpriced");
    expect(tile(0)).toBe("$12.50");
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

  it("names the model whole: prose type in a column wide enough, with the raw id still hovering", () => {
    const { container } = overview({
      summary: fixtures.summary,
      sessions: [
        {
          ...fixtures.sessionListItem,
          id: "s-lead",
          name: "lead",
          costUsd: 9.8,
          requestCount: 412,
          model: "glm-5.3-flash",
          live: true,
        },
      ],
    });
    const table = container.querySelector('table[aria-label="Top sessions by spend"]') as HTMLTableElement;
    const header = [...table.querySelectorAll("thead th")].find((th) => th.textContent === "Model");
    expect(header?.classList.contains("w-40")).toBe(true);
    const cell = [...table.querySelectorAll("tbody td")].find((td) => td.textContent === "GLM 5.3 Flash");
    expect(cell).toBeDefined();
    expect(cell?.classList.contains("truncate")).toBe(true);
    expect(cell?.classList.contains("font-mono")).toBe(false);
    expect(cell?.classList.contains("text-[13px]")).toBe(false);
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

  it("names the enabled routers, the stopped one first with its word", async () => {
    const { container } = overview(
      {
        summary: fixtures.summary,
        providers: ["zai", "kimi", "qwen"],
        budgetStatus: { version: 1, updatedAt: NOW, stopped: ["provider:kimi"], spend: [] },
      },
      registrySessions,
      { ok: true, router: { running: true, providers: ["zai", "kimi"] } },
    );
    await waitFor(() => expect(container.querySelector('[data-router="zai"]')?.textContent).toContain("up"));
    const rows = [...container.querySelectorAll("[data-router]")];
    expect(rows.map((row) => row.getAttribute("data-router"))).toEqual(["kimi", "zai"]);
    expect(rows[0]?.textContent).toBe("kimibudget stopped");
    expect(rows[1]?.textContent).toBe("zaiup");
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

  it("counts the range's HTTP failures in the requests card's tone", () => {
    const { container } = overview({
      summary: fixtures.summary,
      requests: [
        oldEdge(),
        priced("claude-opus-5-5", NOW - 120_000),
        {
          ...priced("claude-opus-5-5", NOW - 60_000),
          id: "req-failed",
          stopReason: null,
          status: "529",
          error: "overloaded",
        },
      ],
    });
    expect(container.querySelector('[data-stat="requests"] .num')?.textContent).toBe("2");
    expect(container.querySelector('[data-stat="requests"]')?.textContent).toContain("1 failed");
  });

  it("names the Day window the memory-backed range reads", () => {
    // a request older than the day keeps the range off the history route, so the tiles read it
    const { container } = overview({
      summary: fixtures.summary,
      range: { preset: "24h", from: 0, to: null },
      requests: [
        priced("claude-opus-5-5", NOW - 25 * 3_600_000),
        priced("glm-5.3", NOW - 60_000, "https://api.z.ai/api/anthropic"),
      ],
    });
    expect(container.querySelector('[data-stat="requests"]')?.textContent).toContain(
      "Requests · last 24 hours",
    );
    expect(container.querySelector('[data-stat="tokens"]')?.textContent).toContain("Tokens · last 24 hours");
  });

  it("reads a history range's counts from the fetched flow, and names the window it fetched", () => {
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
    // the twelve are the week's own requests, so the tile says so; the tokens still come from the
    // all-time summary, and that tile says all time
    expect(container.querySelector('[data-stat="requests"]')?.textContent).toContain("last 7 days");
    expect(container.querySelector('[data-stat="tokens"]')?.textContent).toContain("all time");
    expect(container.textContent).toContain("Token flow");
  });

  it("says all time on the requests tile while a history range has no flow answer yet", () => {
    // the count falls back to the summary's all-time total, so the tile names all time
    const { container } = overview({
      summary: fixtures.summary,
      range: { preset: "7d", from: 0, to: null },
      requests: [priced("claude-opus-5-5", NOW - 60_000)],
    });
    expect(container.querySelector('[data-stat="requests"] .num')?.textContent).toBe("412");
    expect(container.querySelector('[data-stat="requests"]')?.textContent).toContain("Requests · all time");
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

/** The tokens guard over the stat row and the routers card, the parts these tiles paint. */
function expectTiles(container: ParentNode): void {
  for (const part of ["[data-stat-cards]", "[aria-labelledby='ovw-routers']"]) {
    const root = container.querySelector(part);
    expect(root, part).not.toBeNull();
    if (root !== null) expectTokensOnly(root);
  }
}

describe("the Overview tiles measure what they claim", () => {
  it("counts the live sessions only: three live beside sixty-three ended read 3", () => {
    const live = Array.from({ length: 3 }, (_, i) => ({
      ...fixtures.sessionListItem,
      id: `s-live-${i}`,
      live: true,
    }));
    const ended = Array.from({ length: 63 }, (_, i) => ({
      ...fixtures.sessionListItem,
      id: `s-ended-${i}`,
      live: false,
    }));
    const { container } = overview({ summary: fixtures.summary, sessions: [...live, ...ended] });
    const tile = container.querySelector('[data-stat="sessions"]');
    expect(tile?.querySelector(".num")?.textContent).toBe("3");
    expect(tile?.textContent).toContain("live now");
    expectTiles(container);
  });

  it("reads the enabled providers from the router: providers.json {zai} reads 1 of 1", async () => {
    const { container } = overview(
      { summary: fixtures.summary, providers: ["zai", "kimi", "deepseek", "minimax", "qwen"] },
      registrySessions,
      { ok: true, router: { running: true, pid: 4242, port: 18786, providers: ["zai"] } },
    );
    await waitFor(() =>
      expect(container.querySelector('[data-stat="routers"] .num')?.textContent).toBe("1 of 1"),
    );
    expect(container.querySelector('[data-stat="routers"]')?.textContent).toContain("all up");
    expect(
      [...container.querySelectorAll("[data-router]")].map((row) => row.getAttribute("data-router")),
    ).toEqual(["zai"]);
    expectTiles(container);
  });

  it("reads down when huddle-router is not running", async () => {
    const { container } = overview({ summary: fixtures.summary, providers: ["zai"] }, registrySessions, {
      ok: true,
      router: { running: false, providers: ["zai"] },
    });
    await waitFor(() =>
      expect(container.querySelector('[data-stat="routers"] .num')?.textContent).toBe("down"),
    );
    expect(container.querySelector('[data-router="zai"]')?.textContent).toContain("down");
    expectTiles(container);
  });

  it("counts a 502 with no stop reason as failed and leaves a cancelled 499 out", () => {
    const failed = { ...priced("claude-opus-5-5", NOW - 120_000), stopReason: null, status: "502" };
    const cancelled = {
      ...priced("claude-opus-5-5", NOW - 90_000),
      stopReason: null,
      status: "499",
    };
    const { container } = overview({
      summary: fixtures.summary,
      requests: [oldEdge(), priced("claude-opus-5-5", NOW - 60_000), failed, cancelled],
    });
    const tile = container.querySelector('[data-stat="requests"]');
    expect(tile?.textContent).toContain("1 failed");
    expect(tile?.textContent).toContain("1 cancelled");
    expectTiles(container);
  });
});

describe("the Overview on the tokens", () => {
  /** Every card the Overview can show at once: tiles with a budget, the chart, sessions of each
   *  state, an alert, the plan limits, routers with a stopped one and the tools. */
  function everything(alertKind: "budget" | "stuck") {
    return overview({
      summary: fixtures.summary,
      sessions: [
        { ...fixtures.sessionListItem, id: "s-lead", name: "lead", costUsd: 9.8, live: true },
        { ...fixtures.sessionListItem, id: "s-kanban", name: "kanban", costUsd: 2.1, live: false },
        { ...fixtures.sessionListItem, id: "s-live", name: "live", costUsd: 1.1, live: true },
        { ...fixtures.sessionListItem, id: "s-gone", name: "gone", costUsd: 0.5, live: false },
      ],
      spendToday: 18.4,
      spendUnpriced: 2,
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
      providers: ["zai", "kimi"],
      alerts: [{ ...fixtures.alert, kind: alertKind }],
      quota: {
        session_id: "s-lead",
        five_hour: 42,
        five_hour_resets_at: NOW + 60_000,
        seven_day: 93,
        seven_day_resets_at: NOW + 86_400_000,
        at: NOW - 60_000,
      },
      models: { models: [], upstreams: [], tools: [{ name: "Bash", count: 3, failures: 0 }] },
      requests: [oldEdge(), priced("claude-opus-5-5", NOW - 60_000)],
    }).container;
  }

  it("paints no daisyUI or legacy class on any card", () => {
    expect(legacyClasses(everything("budget"))).toEqual([]);
    expect(legacyClasses(everything("stuck"))).toEqual([]);
  });

  it("sets every card on the token surface and its actions on the token buttons", () => {
    const page = everything("budget");
    for (const name of ["spend today", "requests", "tokens", "sessions", "routers"])
      expect(page.querySelector(`[data-stat="${name}"]`)?.classList.contains("ui-card")).toBe(true);
    for (const id of ["ovw-chart", "ovw-sessions", "ovw-alert", "ovw-quota", "ovw-routers", "ovw-tools"])
      expect(page.querySelector(`section[aria-labelledby="${id}"]`)?.classList.contains("ui-card")).toBe(
        true,
      );
    const actions = [...page.querySelectorAll('[aria-labelledby="ovw-alert"] :is(a, button)')];
    expect(actions.map((el) => el.classList.contains("ui-btn"))).toEqual([true, true]);
    // the spend tile still says how much it could not price
    expect(page.querySelector('[data-stat="spend today"] .num')?.textContent).toBe("$18.40 · 2 unpriced");
  });

  it("colours the marks from the token roles, never a raw colour", () => {
    const page = everything("budget");
    const styled = [...page.querySelectorAll<HTMLElement>("[style]")].map(
      (el) => el.getAttribute("style") ?? "",
    );
    for (const style of styled)
      expect(style).not.toMatch(/#[0-9a-f]{3,8}\b|var\(--(series|success|danger|warning|muted|primary)/);
    const stopped = page.querySelector('[data-router="kimi"] [aria-label="budget stopped"]');
    expect(stopped?.getAttribute("style")).toContain("var(--ui-fail)");
  });
});

describe("the Overview on the phone", () => {
  afterEach(() => vi.unstubAllGlobals());

  /** Today's ledger as the live shot had it, and the hour's calls: one priced, one nothing prices. */
  const busyDay: Partial<ClientState> = {
    summary: fixtures.summary,
    spendToday: 1057.61,
    spendUnpriced: 9294,
    sessions: [
      { ...fixtures.sessionListItem, id: "s-lead", live: true },
      { ...fixtures.sessionListItem, id: "s-kanban", live: false },
    ],
    requests: [
      oldEdge(),
      priced("claude-opus-5-5", NOW - 60_000),
      { ...priced("mystery-model", NOW - 120_000), id: "req-unpriced" },
    ],
  };

  it("leads with three tiles under one window label, the spend on one line and its unpriced count below", () => {
    stubMedia({});
    const { container } = overview(busyDay);
    const tiles = container.querySelector("[data-phone-tiles]");
    expect(tiles).not.toBeNull();
    expect(container.querySelector("[data-stat-cards]")).toBeNull();
    // one label names the window every tile reads; no tile names its own
    expect(tiles?.querySelector("#ovw-window")?.textContent).toBe("Last hour");
    const stats = [...(tiles?.querySelectorAll("[data-stat]") ?? [])];
    expect(stats.map((el) => el.getAttribute("data-stat"))).toEqual(["spend", "requests", "sessions"]);
    for (const stat of stats) expect(stat.firstElementChild?.textContent).not.toContain("·");
    // the hour's spend is one figure that never wraps; what nothing priced sits on its own line
    const spend = tiles?.querySelector('[data-stat="spend"]');
    expect(spend?.querySelector(".num")?.textContent).toBe("$6.01");
    expect(spend?.querySelector(".num")?.classList.contains("whitespace-nowrap")).toBe(true);
    expect(spend?.textContent).toContain("1 unpriced");
    expect(tiles?.querySelector('[data-stat="requests"] .num')?.textContent).toBe("2");
    expect(tiles?.querySelector('[data-stat="sessions"] .num')?.textContent).toBe("1");
    // the rest of the page follows under them
    expect(container.querySelector('section[aria-labelledby="ovw-chart"]')).not.toBeNull();
    if (tiles !== null) expectTokensOnly(tiles);
  });

  it("names all time when only the summary answers the range, and every tile reads it", () => {
    stubMedia({});
    const { container } = overview({
      ...busyDay,
      range: { preset: "7d", from: 0, to: null },
      requests: [priced("claude-opus-5-5", NOW - 60_000)],
    });
    const tiles = container.querySelector("[data-phone-tiles]");
    expect(tiles?.querySelector("#ovw-window")?.textContent).toBe("All time");
    expect(tiles?.querySelector('[data-stat="requests"] .num')?.textContent).toBe("412");
    expect(tiles?.querySelector('[data-stat="spend"] .num')?.textContent).toBe(
      fmtUsd(fixtures.summary.costUsd),
    );
  });

  it("keeps the cards on a desktop, Anthropic's limits first, the spend tile with today's ledger as it reads", () => {
    stubMedia({ "(min-width: 640px)": true, "(min-width: 1024px)": true });
    const { container } = overview(busyDay);
    expect(container.querySelector("[data-phone-tiles]")).toBeNull();
    const stats = [...container.querySelectorAll("[data-stat-cards] [data-stat]")];
    expect(stats.map((el) => el.getAttribute("data-stat"))).toEqual([
      "limits",
      "spend today",
      "requests",
      "tokens",
      "sessions",
      "routers",
    ]);
    expect(container.querySelector('[data-stat="spend today"] .num')?.textContent).toBe(
      "$1,057.61 · 9294 unpriced",
    );
    // a subscription pays no dollars: the figure is what the calls cost at list price
    expect(container.querySelector('[data-stat="spend today"]')?.firstElementChild?.textContent).toBe(
      "Spend today · list price",
    );
  });

  /** Anthropic's unified windows as the newest claude-* answer's headers reported them: the share
   *  used 0 to 1 and the reset in unix seconds. */
  const limits = {
    five_hour: { used: 0.42, resets_at: (NOW + (2 * 60 + 14) * 60_000) / 1000 },
    seven_day: { used: 0.613, resets_at: (NOW + (3 * 24 + 5) * 3_600_000) / 1000 },
  };

  /** The limits tile's two windows as they read: label, percent, reset words. */
  function windowsOf(container: Element) {
    return [...container.querySelectorAll('[data-stat="limits"] [data-limit-window]')].map((el) => ({
      label: el.getAttribute("data-limit-window"),
      pct: el.querySelector(".num")?.textContent,
      text: el.textContent,
    }));
  }

  it("leads the desktop with 5 h and 7 d used as percentages and when each resets", () => {
    stubMedia({ "(min-width: 640px)": true, "(min-width: 1024px)": true });
    const { container } = overview({ ...busyDay, summary: { ...fixtures.summary, utilization: limits } });
    const first = container.querySelector("[data-stat-cards] [data-stat]");
    expect(first?.getAttribute("data-stat")).toBe("limits");
    const windows = windowsOf(container);
    expect(windows.map((w) => [w.label, w.pct])).toEqual([
      ["5 h", "42%"],
      ["7 d", "61%"],
    ]);
    expect(windows[0]?.text).toContain("resets in 2h 14m");
    expect(windows[1]?.text).toContain("resets in 3d 5h");
    expect(container.querySelector('[data-stat="limits"]')?.textContent).not.toContain(
      "no Anthropic answer yet",
    );
  });

  it("puts the limits tile at the top of the phone, above the window's three tiles, and says list price on spend", () => {
    stubMedia({});
    const { container } = overview({ ...busyDay, summary: { ...fixtures.summary, utilization: limits } });
    const limitsTile = container.querySelector('[data-stat="limits"]');
    const tiles = container.querySelector("[data-phone-tiles]");
    expect(limitsTile).not.toBeNull();
    expect(tiles?.contains(limitsTile ?? null)).toBe(false);
    expect(container.querySelector("[data-stat]")).toBe(limitsTile);
    expect(windowsOf(container).map((w) => [w.label, w.pct])).toEqual([
      ["5 h", "42%"],
      ["7 d", "61%"],
    ]);
    expect(tiles?.querySelector('[data-stat="spend"]')?.textContent).toContain("list price");
  });

  it("shows a dash and says no Anthropic answer yet while no claude-* answer carried the headers", () => {
    for (const media of [{}, { "(min-width: 640px)": true, "(min-width: 1024px)": true }]) {
      stubMedia(media);
      const { container } = overview(busyDay);
      const tile = container.querySelector('[data-stat="limits"]');
      expect(tile?.querySelector(".num")?.textContent).toBe("–");
      expect(tile?.textContent).toContain("no Anthropic answer yet");
      expect(windowsOf(container)).toEqual([]);
      container.remove();
    }
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
    expect(windows[0]?.querySelector(".bar-seg")?.getAttribute("style")).toContain("var(--ui-ok)");
    expect(windows[1]?.querySelector(".bar-seg")?.getAttribute("style")).toContain("var(--ui-fail)");
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
