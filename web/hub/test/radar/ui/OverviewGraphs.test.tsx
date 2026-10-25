// OverviewGraphs.test.tsx — the Overview's four graphs: each root carries its series as JSON, the
// tokens graph keeps the last seven of the thirty days, the sessions graph names a session by the
// registry's title and machine, and nothing shows while a route cannot answer.

import { waitFor } from "@testing-library/preact";
import { describe, expect, it } from "vite-plus/test";
import { fetchGraphs, type GraphsData, OverviewGraphs } from "../../../src/radar/app/OverviewGraphs.tsx";
import type { UsageDay, UsageSession } from "../../../src/radar/shared/usage.ts";
import { TOKEN_KINDS } from "../../../src/radar/palette.ts";
import { legacyClasses, makeCtx, makeRegistry, makeState, renderIn } from "../../helpers.tsx";

const zero = { requests: 0, input: 0, output: 0, cacheRead: 0, cacheWrite: 0, costUsd: 0, savingsUsd: 0 };

/** Thirty UTC days ending 2026-10-25, the last two carrying usage. */
function days(): UsageDay[] {
  return Array.from({ length: 30 }, (_, i) => {
    const date = new Date(Date.UTC(2026, 8, 26 + i)).toISOString().slice(0, 10);
    if (i === 28) return { ...zero, date, requests: 1, input: 10, output: 5, costUsd: 0.25 };
    if (i === 29)
      return {
        ...zero,
        date,
        requests: 3,
        input: 2400,
        output: 600,
        cacheRead: 5000,
        cacheWrite: 2000,
        costUsd: 0.5,
      };
    return { ...zero, date };
  });
}

function session(sessionId: string, label: string, costUsd: number): UsageSession {
  return { ...zero, sessionId, label, project: "app", harness: "claude", lastTs: 0, requests: 1, costUsd };
}

function data(
  sessions: UsageSession[] = [session("s-lead", "Fix the build", 0.5), session("s-old", "", 0.1)],
): GraphsData {
  const hourlyRequests = Array.from({ length: 24 }, (_, hour) => (hour === 9 ? 3 : 0));
  return {
    usage: {
      from: "2026-09-26",
      to: "2026-10-25",
      days: days(),
      totals: zero,
      byModel: [],
      byProject: [],
      byKind: [],
      byHarness: [],
      topSessions: {},
      approximate: false,
      unpriced: 0,
      pricesRetrieved: "2026-10-08",
    },
    today: { date: "2026-10-25", hourlyRequests, sessions, approximate: false, unpriced: 0 },
  };
}

const registry = makeRegistry({
  sessions: [
    {
      session_id: "s-lead",
      title: "Lead session",
      machine: "studio",
      state: "working",
      last_event: "Stop",
      last_event_at: "",
    },
  ],
});

async function graphs(answer: GraphsData | null) {
  const view = renderIn(
    <OverviewGraphs read={() => Promise.resolve(answer)} />,
    makeCtx(makeState(), { registry }),
  );
  if (answer !== null)
    await waitFor(() => expect(view.container.querySelector("[data-graph]")).not.toBeNull());
  return view.container;
}

function seriesOf(root: Element, name: string): Record<string, number[]> {
  const graph = root.querySelector(`[data-graph='${name}']`);
  return JSON.parse(graph?.getAttribute("data-series") ?? "{}") as Record<string, number[]>;
}

describe("OverviewGraphs", () => {
  it("carries each graph's series on its root", async () => {
    const root = await graphs(data());
    expect(seriesOf(root, "requests").requests).toHaveLength(24);
    expect(seriesOf(root, "requests").requests?.[9]).toBe(3);
    const tokens = seriesOf(root, "tokens");
    expect(tokens.input).toEqual([0, 0, 0, 0, 0, 10, 2400]);
    expect(tokens.cacheRead?.at(-1)).toBe(5000);
    expect(tokens.cacheWrite?.at(-1)).toBe(2000);
    expect(seriesOf(root, "cost").cost).toHaveLength(30);
    expect(seriesOf(root, "cost").cost?.slice(-2)).toEqual([0.25, 0.5]);
    expect(seriesOf(root, "sessions").cost).toEqual([0.5, 0.1]);
    expect(root.querySelector("[data-graph='requests']")?.textContent).toContain("Requests per hour");
    expect(root.querySelector("[data-graph='cost']")?.textContent).toContain("$0.75");
  });

  it("names a session by the registry's title and machine, else its label or id", async () => {
    const root = await graphs(data());
    const rows = [...root.querySelectorAll("[data-graph='sessions'] li")].map((li) => li.textContent);
    expect(rows[0]).toContain("Lead session · studio");
    expect(rows[0]).toContain("$0.50");
    expect(rows[1]).toContain("s-old");
  });

  it("paints on the tokens, each token kind in its palette colour", async () => {
    const root = await graphs(data());
    expect(legacyClasses(root)).toEqual([]);
    const swatches = [...root.querySelectorAll<HTMLElement>("[data-graph='tokens'] li > span:first-child")];
    expect(swatches.map((el) => el.style.background)).toEqual(TOKEN_KINDS.map((kind) => kind.color));
    const lines = [...root.querySelectorAll("[data-graph='tokens'] path.hr-chart-line")];
    expect(lines.map((el) => el.getAttribute("style"))).toEqual(
      TOKEN_KINDS.map((kind) => expect.stringContaining(kind.color)),
    );
    for (const el of root.querySelectorAll("[style]"))
      expect(el.getAttribute("style")).not.toMatch(/var\(--(series|primary|panel|muted)/);
  });

  it("paints the empty sessions graph on the tokens too", async () => {
    expect(legacyClasses(await graphs(data([])))).toEqual([]);
  });

  it("says so when no session spent today", async () => {
    const root = await graphs(data([]));
    expect(root.querySelector("[data-graph='sessions']")?.textContent).toContain("No sessions today");
  });

  it("shows nothing while a route cannot answer", async () => {
    const root = await graphs(null);
    await Promise.resolve();
    expect(root.querySelector("[data-graph]")).toBeNull();
  });

  it("reads both routes and answers null when either fails", async () => {
    const body = data();
    const asked: string[] = [];
    const read = await fetchGraphs((input) => {
      const path = input instanceof Request ? input.url : input instanceof URL ? input.href : input;
      asked.push(path);
      return Promise.resolve(new Response(JSON.stringify(path.endsWith("/today") ? body.today : body.usage)));
    });
    expect(read).toEqual(body);
    expect(asked.sort()).toEqual(["/api/radar/api/usage/today", "/api/radar/api/usage?days=30"]);
    expect(await fetchGraphs(() => Promise.resolve(new Response("{}", { status: 503 })))).toBeNull();
    expect(await fetchGraphs(() => Promise.reject(new Error("offline")))).toBeNull();
  });
});
