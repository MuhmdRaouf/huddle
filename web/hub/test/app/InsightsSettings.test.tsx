// InsightsSettings.test.tsx — Settings › Tips › Insights: the two switches wait disabled
// until the route's answer, render from it, a flip PUTs its one field only, and a refusal flips
// the switch back and says the error in a toast. Start over: the forget button swaps for the
// confirm line and two buttons, Cancel brings it back, Forget posts to the forget route, re-reads
// the switches and says it forgot, and a refusal says the error and keeps the confirm up.
import { act, screen } from "@testing-library/preact";
import { describe, expect, it, vi } from "vite-plus/test";
import type { Api, ApiOptions } from "../../src/api.ts";
import { InsightsSettings } from "../../src/app/InsightsSettings.tsx";
import type { HuddleContextValue } from "../../src/app/context.tsx";
import { flush, makeCtx, makeState, renderIn } from "../helpers.tsx";

/** One call the fake api saw: the path and the options it went out with. */
type Call = { path: string; o?: ApiOptions | undefined };

/** What the route answers before anyone turns a switch. */
const DEFAULTS = { tips_for_me: true, tips_to_sessions: false };

/** An api that answers the route's switches and records every call; `answer` is what a change
 *  (a PUT or a POST) meets and `get` what the first read meets, a test holding it back to see the wait. */
function apiFake(
  answer: Promise<unknown>,
  get: Promise<unknown> = Promise.resolve(DEFAULTS),
): { api: Api; calls: Call[] } {
  answer.catch(() => {}); // the refusal belongs to the change that meets it; this keeps it off
  // Node's unhandled-rejection list until then
  const calls: Call[] = [];
  const api: Api = {
    api: (path, o) => {
      calls.push({ path, o });
      return o?.method === "GET" || o === undefined ? get : answer;
    },
    op: () => Promise.resolve({}),
    channelPath: (ch, p) => `/api/c/${ch}${p}`,
    channelHref: (ch, p) => `#/channels/${ch}${p}`,
  };
  return { api, calls };
}

/** Renders the pane over the api and the toast the test hands in. */
function mount(api: Api, toast: HuddleContextValue["toast"] = vi.fn()): void {
  renderIn(<InsightsSettings />, makeCtx(makeState(), { api, toast }));
}

/** One switch by its row id. */
const sw = (id: string): HTMLInputElement => document.getElementById(`set-${id}`) as HTMLInputElement;

describe("the Insights switches", () => {
  it("both switches wait disabled for the route's answer, then show it", async () => {
    let answerTheRead!: (v: unknown) => void;
    const held = new Promise<unknown>((resolve) => {
      answerTheRead = resolve;
    });
    const { api } = apiFake(Promise.resolve(DEFAULTS), held);
    mount(api);
    await flush();
    expect(sw("tips-for-me").disabled).toBe(true);
    expect(sw("tips-to-sessions").disabled).toBe(true);
    await act(async () => {
      answerTheRead(DEFAULTS);
    });
    await flush();
    expect(sw("tips-for-me").disabled).toBe(false);
    expect(sw("tips-to-sessions").disabled).toBe(false);
    expect(sw("tips-for-me").getAttribute("aria-checked")).toBe("true");
    expect(sw("tips-to-sessions").getAttribute("aria-checked")).toBe("false");
  });

  it("renders both switches from the route's answer", async () => {
    const { api } = apiFake(Promise.resolve(DEFAULTS));
    mount(api);
    await flush();
    expect(sw("tips-for-me").getAttribute("aria-checked")).toBe("true");
    expect(sw("tips-to-sessions").getAttribute("aria-checked")).toBe("false");
    expect(document.body.textContent).toContain("Tips for me");
    expect(document.body.textContent).toContain("Tips to my sessions");
  });

  it("a flip PUTs its one field only", async () => {
    const { api, calls } = apiFake(Promise.resolve({ tips_for_me: true, tips_to_sessions: true }));
    mount(api);
    await flush();
    await act(async () => {
      sw("tips-to-sessions").click();
    });
    await flush();
    const puts = calls.filter((c) => c.o?.method === "PUT");
    expect(puts).toHaveLength(1);
    expect(puts[0]?.path).toBe("/api/insights/settings");
    expect(puts[0]?.o?.body).toEqual({ tips_to_sessions: true });
    expect(sw("tips-to-sessions").getAttribute("aria-checked")).toBe("true");
  });

  it("a refusal flips the switch back and says the error", async () => {
    const toast = vi.fn();
    const { api } = apiFake(Promise.reject(new Error("the switches' file does not take the change")));
    mount(api, toast);
    await flush();
    await act(async () => {
      sw("tips-to-sessions").click();
    });
    await flush();
    expect(sw("tips-to-sessions").getAttribute("aria-checked")).toBe("false");
    expect(toast).toHaveBeenCalledWith("the switches' file does not take the change", { bad: true });
  });
});

describe("Start over", () => {
  it("the forget button swaps for the confirm line and two buttons, and Cancel brings the button back", async () => {
    const { api, calls } = apiFake(Promise.resolve(DEFAULTS));
    mount(api);
    await flush();
    expect(screen.getByRole("button", { name: "Forget what you learned" })).toBeDefined();
    await act(async () => {
      screen.getByRole("button", { name: "Forget what you learned" }).click();
    });
    expect(document.body.textContent).toContain(
      "Huddle forgets the muted tips and turns both switches back to their defaults.",
    );
    expect(screen.queryByRole("button", { name: "Forget what you learned" })).toBeNull();
    await act(async () => {
      screen.getByRole("button", { name: "Cancel" }).click();
    });
    expect(screen.getByRole("button", { name: "Forget what you learned" })).toBeDefined();
    expect(calls).toHaveLength(1); // only the mount's read went out
  });

  it("Forget posts to the forget route, re-reads the switches, puts the button back and says it forgot", async () => {
    const toast: HuddleContextValue["toast"] = vi.fn();
    // the hub holds the switches in a file: the forget resets them, and the re-read after it
    // answers what the hub now holds, the defaults
    const calls: Call[] = [];
    let held = { tips_for_me: true, tips_to_sessions: true };
    const api: Api = {
      api: (path, o) => {
        calls.push({ path, o });
        if (o?.method === "POST") held = DEFAULTS;
        return Promise.resolve(held);
      },
      op: () => Promise.resolve({}),
      channelPath: (ch, p) => `/api/c/${ch}${p}`,
      channelHref: (ch, p) => `#/channels/${ch}${p}`,
    };
    mount(api, toast);
    await flush();
    await act(async () => {
      screen.getByRole("button", { name: "Forget what you learned" }).click();
    });
    await act(async () => {
      screen.getByRole("button", { name: "Forget", exact: true }).click();
      await flush();
      await flush();
    });
    expect(calls.map((c) => `${c.o?.method ?? "GET"} ${c.path}`)).toEqual([
      "GET /api/insights/settings",
      "POST /api/insights/forget",
      "GET /api/insights/settings",
    ]);
    expect(sw("tips-for-me").getAttribute("aria-checked")).toBe("true");
    expect(sw("tips-to-sessions").getAttribute("aria-checked")).toBe("false");
    expect(screen.getByRole("button", { name: "Forget what you learned" })).toBeDefined();
    expect(toast).toHaveBeenCalledWith("Insights forgot what it learned.");
  });

  it("a refusal of the forget says the route's message and keeps the confirm up", async () => {
    const toast: HuddleContextValue["toast"] = vi.fn();
    const { api } = apiFake(Promise.reject(new Error("only the owner changes Insights")));
    mount(api, toast);
    await flush();
    await act(async () => {
      screen.getByRole("button", { name: "Forget what you learned" }).click();
    });
    await act(async () => {
      screen.getByRole("button", { name: "Forget", exact: true }).click();
      await flush();
      await flush();
    });
    expect(toast).toHaveBeenCalledWith("only the owner changes Insights", { bad: true });
    expect(document.body.textContent).toContain("turns both switches back to their defaults");
  });
});
