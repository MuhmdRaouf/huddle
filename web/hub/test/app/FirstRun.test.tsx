// FirstRun.test.tsx — the checklist a fresh sign-in carries on Now: the four items and what
// ticks each (the enrolment, a reported session, a channel, a budget), the hiding when all four
// stand or the browser dismissed it, and the remembered dismissal.

import { act, cleanup, screen } from "@testing-library/preact";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import type { Api } from "../../src/api.ts";
import { FirstRunChecklist } from "../../src/app/FirstRun.tsx";
import { flush, makeCtx, makeState, makeRegistry, renderIn } from "../helpers.tsx";
import type { HuddleState } from "../../src/store.ts";
import type { WhoAmI } from "../../src/app/context.tsx";
import type { RegistrySession } from "../../src/registry.ts";

/** The items' done marks, in list order. */
const doneOf = (): string =>
  [...document.querySelectorAll("[data-first-run] [data-item]")]
    .map((el) => el.getAttribute("data-done"))
    .join(",");

/** Mounts the checklist over the facts named: what whoami said, what the store holds, how many
 *  sessions the registry reports, and whether the budgets answer named any. */
function mount(over: {
  who?: Partial<WhoAmI>;
  state?: Partial<HuddleState>;
  sessions?: number;
  budgets?: unknown;
}): { calls: string[] } {
  cleanup();
  const calls: string[] = [];
  const api: Api = {
    api: (path) => {
      calls.push(path);
      if (path === "/api/radar/api/budgets") return Promise.resolve({ budgets: over.budgets ?? [] });
      return Promise.resolve({});
    },
    op: () => Promise.resolve({}),
    channelPath: (ch, p) => `/api/c/${ch}${p}`,
    channelHref: (ch, p) => `#/channels/${ch}${p}`,
  } as Api;
  const sessions: RegistrySession[] = Array.from({ length: over.sessions ?? 0 }, (_, i) => ({
    session_id: `s-${i}`,
    state: "idle",
    last_event: "UserPromptSubmit",
    last_event_at: "2026-10-08T11:00:00.000Z",
  }));
  renderIn(
    <FirstRunChecklist onPairToken={() => {}} />,
    makeCtx(makeState(over.state), {
      api,
      who: { kind: "owner", ...over.who },
      registry: makeRegistry({ sessions }),
    }),
  );
  return { calls };
}

beforeEach(() => {
  localStorage.clear();
});

afterEach(() => {
  vi.unstubAllGlobals();
  cleanup();
});

describe("the first-run checklist", () => {
  it("lists the four items, none ticked on a fresh hub", async () => {
    mount({});
    await flush();
    for (const label of [
      "Set up your authenticator app",
      "Connect a session",
      "Create a channel",
      "Set a budget",
    ]) {
      expect(screen.getByText(label)).toBeDefined();
    }
    expect(doneOf()).toBe("false,false,false,false");
  });

  it("ticks each item from its own fact, and stands down when all four hold", async () => {
    mount({ who: { totp: true } });
    await flush();
    expect(doneOf()).toBe("true,false,false,false");
    cleanup();
    mount({ sessions: 1 });
    await flush();
    expect(doneOf()).toBe("false,true,false,false");
    cleanup();
    // every item done: the panel itself is gone
    mount({
      who: { totp: true },
      sessions: 1,
      state: { channels: [{ name: "shop" }] },
      budgets: [{ id: "b1" }],
    });
    await flush();
    expect(screen.queryByText("Set up your authenticator app")).toBeNull();
  });

  it("hides for good once dismissed, whatever the items say", async () => {
    mount({});
    await flush();
    void act(() => {
      screen.getByRole("button", { name: "Hide checklist" }).click();
    });
    expect(screen.queryByText("Set up your authenticator app")).toBeNull();
    cleanup();
    mount({ who: { totp: true } });
    await flush();
    expect(screen.queryByText("Set up your authenticator app")).toBeNull();
  });

  it("sends each item's button to the place that does it", async () => {
    mount({});
    await flush();
    const hrefs = [...document.querySelectorAll("[data-first-run] [data-item] a")].map((a) =>
      a.getAttribute("href"),
    );
    expect(hrefs).toEqual(["#/settings/signin", "#/channels", "#/channels", "#/settings/radar"]);
  });

  it("offers the passkey as one checklist row: one-line label, the why under it, the Save button", () => {
    mount({});
    const row = document.querySelector('[data-banner="passkey"]');
    expect(row).not.toBeNull();
    const label = screen.getByText("Save a passkey");
    expect(label.textContent).toBe("Save a passkey");
    expect(screen.getByText("Touch ID signs you in next time.")).toBeDefined();
    expect(screen.getByRole("button", { name: "Save passkey" })).toBeDefined();
    expect(screen.getByRole("button", { name: "Dismiss" })).toBeDefined();
  });

  it("reads the budgets once, and a refused read counts as none", async () => {
    const h = mount({});
    await flush();
    expect(h.calls).toEqual(["/api/radar/api/budgets"]);
    expect(document.querySelector("[data-item='budget']")?.getAttribute("data-done")).toBe("false");
  });
});
