// Insights.test.tsx — the Insights card on Now: the tips GET /api/insights answers, each one
// drawn as its sentence with the text between backticks as code over its source line, a tip's
// session as a link into the Now list, the card hidden while the route refuses or holds no tip,
// and the once-every-five-minutes re-read.
import { act, cleanup, screen } from "@testing-library/preact";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { Api, ApiError } from "../../src/api.ts";
import { InsightsCard, InsightsView, readInsights, type Tip } from "../../src/app/Insights.tsx";
import { flush, makeCtx, makeState, renderIn } from "../helpers.tsx";

const refusal = (status: number): ApiError => Object.assign(new Error("refused"), { status }) as ApiError;

const ALLOW_TIP: Tip = {
  id: "always_allowed:Bash(go test:*)",
  rule: "always_allowed",
  sentence:
    "You allowed `Bash(go test:*)` 5 times in 7 days, waiting 2 min each time. An allow rule would skip the prompt.",
  count: 5,
  wait_ms: 120000,
};

/** A tip of one rule, over the always-allowed fixture's shape. */
const tipOf = (rule: string): Tip => ({
  ...ALLOW_TIP,
  id: `${rule}:x`,
  rule,
  sentence: `A ${rule} tip.`,
});

/** An API whose answers are a function of the path; a thrown Error is a refusal. */
function apiOf(answer: (path: string) => unknown): Api & { paths: string[] } {
  const paths: string[] = [];
  return {
    paths,
    api: (path) => {
      paths.push(path);
      const r = answer(path);
      return r instanceof Error ? Promise.reject(r) : Promise.resolve(r);
    },
    op: () => Promise.resolve({}),
    channelPath: (ch, p) => `/api/c/${ch}${p}`,
    channelHref: (ch, p) => `#/channels/${ch}${p}`,
  };
}

/** One item's full text, as its rule names it. */
const itemText = (rule: string): string => document.querySelector(`[data-tip="${rule}"]`)?.textContent ?? "";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

describe("readInsights", () => {
  it("answers the tips of a body that holds them", async () => {
    expect(await readInsights(apiOf(() => ({ tips: [ALLOW_TIP] })))).toEqual([ALLOW_TIP]);
  });

  it("reads a refusal, a body without tips and a tips field that is not a list as nothing", async () => {
    expect(await readInsights(apiOf(() => refusal(503)))).toBeNull();
    expect(await readInsights(apiOf(() => ({})))).toBeNull();
    expect(await readInsights(apiOf(() => ({ tips: "x" })))).toBeNull();
  });

  it("drops a tip without a sentence", async () => {
    const tips = await readInsights(
      apiOf(() => ({ tips: [ALLOW_TIP, { id: "x", rule: "same_command", count: 2 }] })),
    );
    expect(tips).toEqual([ALLOW_TIP]);
  });
});

describe("the Insights view", () => {
  it("shows one item per tip: the sentence's code, the count and the source", () => {
    renderIn(<InsightsView tips={[ALLOW_TIP]} />);
    expect(screen.getByRole("region", { name: "Insights" })).toBeDefined();
    expect(screen.getByText("1")).toBeDefined();
    const code = document.querySelector('[data-tip="always_allowed"] code');
    expect(code?.textContent).toBe("Bash(go test:*)");
    expect(code?.className).toContain("ui-mono");
    expect(itemText("always_allowed")).toContain("5 times in 7 days");
    expect(itemText("always_allowed")).not.toContain("`");
    expect(itemText("always_allowed")).toContain("From your permission asks, last 7 days");
  });

  it("names each rule's source", () => {
    renderIn(
      <InsightsView
        tips={[
          tipOf("longest_wait"),
          tipOf("same_command"),
          tipOf("tool_failing"),
          tipOf("lane_failing"),
          tipOf("mystery"),
        ]}
      />,
    );
    expect(itemText("longest_wait")).toContain("From Needs you, last 7 days");
    expect(itemText("same_command")).toContain("From your sessions' tool runs");
    expect(itemText("tool_failing")).toContain("From your sessions' tool runs");
    expect(itemText("lane_failing")).toContain("From the provider lanes, today");
    expect(itemText("mystery")).toContain("From huddle");
  });

  it("links a tip's session into the Now list", () => {
    renderIn(<InsightsView tips={[{ ...ALLOW_TIP, session: "s-1" }]} />);
    const link = document.querySelector('[data-tip="always_allowed"] a');
    expect(link?.getAttribute("href")).toBe("#/now/s-1");
    expect(link?.textContent).toBe("open the session");
    expect(itemText("always_allowed")).toContain("From your permission asks, last 7 days · ");
  });
});

describe("the Insights card", () => {
  it("draws the card for one tip, read through /api/insights", async () => {
    const api = apiOf(() => ({ tips: [ALLOW_TIP] }));
    renderIn(<InsightsCard />, makeCtx(makeState(), { api }));
    await act(async () => {
      await flush();
    });
    expect(api.paths).toEqual(["/api/insights"]);
    expect(screen.getByRole("region", { name: "Insights" })).toBeDefined();
  });

  it("draws nothing while the route refuses or holds no tip", async () => {
    for (const answer of [refusal(503), { tips: [] }]) {
      const view = renderIn(<InsightsCard />, makeCtx(makeState(), { api: apiOf(() => answer) }));
      await act(async () => {
        await flush();
      });
      expect(view.container.firstElementChild).toBeNull();
      cleanup();
    }
  });

  it("re-reads once every five minutes", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    const api = apiOf(() => ({ tips: [ALLOW_TIP] }));
    renderIn(<InsightsCard />, makeCtx(makeState(), { api }));
    await act(async () => {
      await flush();
    });
    const reads = api.paths.length;
    expect(reads).toBe(1);
    await act(async () => {
      vi.advanceTimersByTime(299_999);
      await flush();
    });
    expect(api.paths.length).toBe(reads);
    await act(async () => {
      vi.advanceTimersByTime(1);
      await flush();
    });
    expect(api.paths.length).toBe(reads + 1);
  });
});
