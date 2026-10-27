// CaptureSetting.test.tsx — Settings › Hub › Request bodies: the switch waits disabled for the
// read, then shows it with the Kept-now line from the bodies read; a flip POSTs its one field,
// toasts from the answer and reads the bodies again; a refused flip puts the switch back and says
// the route's message as bad news; a failed read leaves the switch disabled and says so in a line.
import { act } from "@testing-library/preact";
import { describe, expect, it, vi } from "vite-plus/test";
import type { Api, ApiOptions } from "../../src/api.ts";
import { CaptureSetting } from "../../src/app/CaptureSetting.tsx";
import type { HuddleContextValue } from "../../src/app/context.tsx";
import { flush, makeCtx, makeState, renderIn } from "../helpers.tsx";

/** One call the fake api saw: the path and the options it went out with. */
type Call = { path: string; o?: ApiOptions | undefined };

/** What GET /api/settings answers while capture is on. */
const ON = { capture: true };

/** What GET /api/bodies answers when bodies are kept — the mock's numbers. */
const KEPT = { state: "kept", requests: 1284, bytes: 222298112 };

/** What GET /api/bodies answers when capture is off. */
const OFF = { state: "off", requests: 0, bytes: 0 };

/** An api that answers each "METHOD path" with its route's promise — or a fresh promise from its
 *  route's maker, for answers a test rewrites mid-flight — and records every call; a call with no
 *  route is refused, so a stray read can't pass unnoticed. */
function apiFake(routes: Record<string, Promise<unknown> | (() => Promise<unknown>)>): {
  api: Api;
  calls: Call[];
} {
  for (const p of Object.values(routes)) if (p instanceof Promise) p.catch(() => {}); // a refusal
  // belongs to the flip or read that meets it; this keeps it off Node's unhandled-rejection list
  const calls: Call[] = [];
  const api: Api = {
    api: (path, o) => {
      calls.push({ path, o });
      const method = o?.method ?? (o?.body !== undefined ? "POST" : "GET");
      const route = routes[`${method} ${path}`];
      if (route === undefined) return Promise.reject(new Error(`no canned answer for ${method} ${path}`));
      return typeof route === "function" ? route() : route;
    },
    op: () => Promise.resolve({}),
    channelPath: (ch, p) => `/api/c/${ch}${p}`,
    channelHref: (ch, p) => `#/channels/${ch}${p}`,
  };
  return { api, calls };
}

/** Renders the pane over the api and the toast the test hands in. */
function mount(api: Api, toast: HuddleContextValue["toast"] = vi.fn()): void {
  renderIn(<CaptureSetting />, makeCtx(makeState(), { api, toast }));
}

/** The one switch, by its row id. */
const sw = (): HTMLButtonElement => document.getElementById("set-capture") as HTMLButtonElement;

describe("the Request bodies pane", () => {
  it("both reads hold the pane: the switch waits disabled and no Kept-now line, then both land", async () => {
    let answerTheRead!: (v: unknown) => void;
    let answerTheBodies!: (v: unknown) => void;
    const settings = new Promise<unknown>((resolve) => {
      answerTheRead = resolve;
    });
    const bodies = new Promise<unknown>((resolve) => {
      answerTheBodies = resolve;
    });
    const { api } = apiFake({ "GET /api/settings": settings, "GET /api/bodies": bodies });
    mount(api);
    await flush();
    expect(sw().disabled).toBe(true);
    expect(document.body.textContent).not.toContain("Kept now");
    await act(async () => {
      answerTheRead(ON);
      answerTheBodies(KEPT);
    });
    await flush();
    expect(sw().disabled).toBe(false);
    expect(sw().getAttribute("aria-checked")).toBe("true");
    expect(document.body.textContent).toContain("Request bodies");
    expect(document.body.textContent).toContain("Keep request bodies");
    expect(document.body.textContent).toContain(
      "huddle-router keeps each request and answer for 7 days, redacted and sealed with a key in this Mac's keychain.",
    );
    expect(document.body.textContent).toContain("Kept now: 1,284 requests · 212 MB");
  });

  it("an off answer shows the switch off and no Kept-now line", async () => {
    const { api } = apiFake({
      "GET /api/settings": Promise.resolve({ capture: false }),
      "GET /api/bodies": Promise.resolve(OFF),
    });
    mount(api);
    await flush();
    expect(sw().disabled).toBe(false);
    expect(sw().getAttribute("aria-checked")).toBe("false");
    expect(document.body.textContent).not.toContain("Kept now");
  });

  it("a flip POSTs its one field, toasts, and reads the bodies again", async () => {
    const toast = vi.fn();
    let bodies: unknown = KEPT;
    const { api, calls } = apiFake({
      "GET /api/settings": Promise.resolve(ON),
      "GET /api/bodies": () => Promise.resolve(bodies),
      "POST /api/settings": Promise.resolve({ capture: false }),
    });
    mount(api, toast);
    await flush();
    bodies = OFF; // turning capture off empties the store, so the re-read meets the off answer
    await act(async () => {
      sw().click();
    });
    await flush();
    const posts = calls.filter((c) => c.o?.method === "POST");
    expect(posts).toHaveLength(1);
    expect(posts[0]?.path).toBe("/api/settings");
    expect(posts[0]?.o?.body).toEqual({ capture: false });
    expect(sw().getAttribute("aria-checked")).toBe("false");
    expect(toast).toHaveBeenCalledWith("Request bodies off. New requests are not kept.");
    expect(calls.filter((c) => c.path === "/api/bodies")).toHaveLength(2);
    expect(document.body.textContent).not.toContain("Kept now");
  });

  it("a flip to on toasts the short line", async () => {
    const toast = vi.fn();
    const { api } = apiFake({
      "GET /api/settings": Promise.resolve({ capture: false }),
      "GET /api/bodies": Promise.resolve(OFF),
      "POST /api/settings": Promise.resolve(ON),
    });
    mount(api, toast);
    await flush();
    await act(async () => {
      sw().click();
    });
    await flush();
    expect(sw().getAttribute("aria-checked")).toBe("true");
    expect(toast).toHaveBeenCalledWith("Request bodies on.");
  });

  it("a refused flip puts the switch back and says the route's message as bad news", async () => {
    const toast = vi.fn();
    const { api } = apiFake({
      "GET /api/settings": Promise.resolve(ON),
      "GET /api/bodies": Promise.resolve(KEPT),
      "POST /api/settings": Promise.reject(new Error("only the owner may change this")),
    });
    mount(api, toast);
    await flush();
    await act(async () => {
      sw().click();
    });
    await flush();
    expect(sw().getAttribute("aria-checked")).toBe("true");
    expect(toast).toHaveBeenCalledWith("only the owner may change this", { bad: true });
    expect(document.body.textContent).toContain("Kept now: 1,284 requests · 212 MB");
  });

  it("a failed read leaves the switch disabled and says so in a line", async () => {
    const toast = vi.fn();
    const { api } = apiFake({
      "GET /api/settings": Promise.reject(new Error("the hub is down")),
      "GET /api/bodies": Promise.resolve(KEPT),
    });
    mount(api, toast);
    await flush();
    expect(sw().disabled).toBe(true);
    expect(document.body.textContent).toContain("Could not read the setting.");
    expect(toast).not.toHaveBeenCalled();
  });
});
