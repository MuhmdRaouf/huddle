// InsightsSettings.test.tsx — Settings › Tips › Insights: the two switches wait disabled
// until the route's answer, render from it, a flip PUTs its one field only, and a refusal flips
// the switch back and says the error in a toast.
import { act } from "@testing-library/preact";
import { describe, expect, it, vi } from "vite-plus/test";
import type { Api, ApiOptions } from "../../src/api.ts";
import { InsightsSettings } from "../../src/app/InsightsSettings.tsx";
import type { HuddleContextValue } from "../../src/app/context.tsx";
import { flush, makeCtx, makeState, renderIn } from "../helpers.tsx";

/** One call the fake api saw: the path and the options it went out with. */
type Call = { path: string; o?: ApiOptions | undefined };

/** What the route answers before anyone turns a switch. */
const DEFAULTS = { tips_for_me: true, tips_to_sessions: false };

/** An api that answers the route's switches and records every call; `answer` is what a PUT meets
 *  and `get` what the first read meets, a test holding it back to see the wait. */
function apiFake(
  answer: Promise<unknown>,
  get: Promise<unknown> = Promise.resolve(DEFAULTS),
): { api: Api; calls: Call[] } {
  answer.catch(() => {}); // the refusal belongs to the flip that meets it; this keeps it off
  // Node's unhandled-rejection list until then
  const calls: Call[] = [];
  const api: Api = {
    api: (path, o) => {
      calls.push({ path, o });
      return o?.method === "PUT" ? answer : get;
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
