// Insights.test.tsx — the Insights card on Now: the tips GET /api/insights answers, each one
// drawn as its sentence with the text between backticks as code over its source line, a tip's
// session as a link into the Now list, the card hidden while the route refuses or holds no tip,
// the once-every-five-minutes re-read, and a tip's Not useful button: its name says the rule in
// words, a tap posts the rule to the mute route and the tip leaves on 200, a refusal keeps it.
import { act, cleanup, screen } from "@testing-library/preact";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { Api, ApiError, ApiOptions } from "../../src/api.ts";
import type { HuddleContextValue } from "../../src/app/context.tsx";
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

/** One call the fake api saw: the path and the options it went out with. */
type Call = { path: string; o?: ApiOptions | undefined };

/** An API whose answers are a function of the path and the options; a thrown Error is a refusal.
 *  Every call lands in `calls`, and `paths` keeps the paths alone for the reads older tests name. */
function apiOf(answer: (path: string, o?: ApiOptions) => unknown): Api & { paths: string[]; calls: Call[] } {
  const calls: Call[] = [];
  return {
    get paths() {
      return calls.map((c) => c.path);
    },
    calls,
    api: (path, o) => {
      calls.push({ path, o });
      const r = answer(path, o);
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
    expect(link?.getAttribute("href")).toBe("#/sessions/s-1");
    expect(link?.textContent).toBe("open the session");
    expect(itemText("always_allowed")).toContain("From your permission asks, last 7 days · ");
  });
});

describe("the Not useful button", () => {
  it("ends each tip's source line, a ghost button whose name says the rule in words", () => {
    renderIn(<InsightsView tips={[tipOf("same_command")]} onMute={() => {}} />);
    const b = document.querySelector('[data-tip="same_command"] button');
    expect(b?.getAttribute("aria-label")).toBe("Not useful: same command");
    expect(b?.className).toContain("ui-btn-ghost");
    expect(b?.textContent).toBe("Not useful");
  });

  it("reads a rule's underscores as spaces in its name", () => {
    renderIn(<InsightsView tips={[tipOf("tool_failing"), tipOf("lane_failing")]} onMute={() => {}} />);
    expect(screen.getByRole("button", { name: "Not useful: tool failing" })).toBeDefined();
    expect(screen.getByRole("button", { name: "Not useful: lane failing" })).toBeDefined();
  });
});

describe("muting a tip", () => {
  const TWO = { tips: [tipOf("same_command"), ALLOW_TIP] };

  it("a tap posts the rule to the mute route, then the tip leaves, the count drops and the toast says for how long", async () => {
    const toast: HuddleContextValue["toast"] = vi.fn();
    const api = apiOf((_path, o) => (o?.method === "POST" ? { muted: ["same_command"] } : TWO));
    renderIn(<InsightsCard />, makeCtx(makeState(), { api, toast }));
    await act(async () => {
      await flush();
    });
    await act(async () => {
      screen.getByRole("button", { name: "Not useful: same command" }).click();
      await flush();
    });
    expect(api.calls).toEqual([
      { path: "/api/insights" },
      { path: "/api/insights/mute", o: { method: "POST", body: { rule: "same_command" } } },
    ]);
    expect(document.querySelector('[data-tip="same_command"]')).toBeNull();
    expect(document.querySelector('[data-tip="always_allowed"]')).not.toBeNull();
    expect(screen.getByText("1")).toBeDefined();
    expect(toast).toHaveBeenCalledWith("No more tips like this for 30 days.");
  });

  it("a refusal keeps the tip and says the route's message as bad news", async () => {
    const toast: HuddleContextValue["toast"] = vi.fn();
    const api = apiOf((_path, o) =>
      o?.method === "POST"
        ? (Object.assign(new Error("only the owner changes Insights"), { status: 403 }) as ApiError)
        : TWO,
    );
    renderIn(<InsightsCard />, makeCtx(makeState(), { api, toast }));
    await act(async () => {
      await flush();
    });
    await act(async () => {
      screen.getByRole("button", { name: "Not useful: same command" }).click();
      await flush();
    });
    expect(document.querySelector('[data-tip="same_command"]')).not.toBeNull();
    expect(toast).toHaveBeenCalledWith("only the owner changes Insights", { bad: true });
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
