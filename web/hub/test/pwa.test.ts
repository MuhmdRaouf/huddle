// pwa.test.ts — the installability glue: the service worker registers only on a secure context
// (localhost or https), the app badge mirrors the open needs-you count where the Badging API
// exists, and the push switch subscribes this browser with the hub's VAPID key and hands the
// subscription to the hub, the focus report tells the hub when this tab is visible and
// focused, and the update watch offers a reload once a new worker or a new hub build arrives.
// All are optional: a browser without them, or a call that fails, changes nothing the
// app does.
import { describe, expect, it, vi } from "vite-plus/test";
import type { ApiOptions } from "../src/api.ts";
import {
  type BadgeNavigator,
  createPushSwitch,
  type FocusPage,
  type PushBrowser,
  type PushSubscriptionLike,
  registerServiceWorker,
  reportFocus,
  type SWWindow,
  syncAppBadge,
  type UpdatePage,
  watchForUpdate,
} from "../src/pwa.ts";

const swWindow = (secure: boolean, register?: (url: string) => Promise<unknown>): SWWindow => ({
  isSecureContext: secure,
  navigator: register ? { serviceWorker: { register } } : {},
});

describe("registerServiceWorker", () => {
  it("registers /sw.js where the context is secure", () => {
    const register = vi.fn(() => Promise.resolve({}));
    registerServiceWorker(swWindow(true, register));
    expect(register).toHaveBeenCalledWith("/sw.js");
  });

  it("registers nothing off a secure context", () => {
    const register = vi.fn(() => Promise.resolve({}));
    registerServiceWorker(swWindow(false, register));
    expect(register).not.toHaveBeenCalled();
  });

  it("registers nothing where the browser has no service worker", () => {
    expect(() => registerServiceWorker(swWindow(true, undefined))).not.toThrow();
  });

  it("swallows a registration that fails", () => {
    const register = vi.fn(() => Promise.reject(new Error("no document")));
    expect(() => registerServiceWorker(swWindow(true, register))).not.toThrow();
  });
});

describe("syncAppBadge", () => {
  const badge = (set?: (count: number) => Promise<void>, clear?: () => Promise<void>): BadgeNavigator => ({
    ...(set ? { setAppBadge: (n?: number) => set(n ?? 0) } : {}),
    ...(clear ? { clearAppBadge: clear } : {}),
  });

  it("sets the badge to the open needs-you count", () => {
    const set = vi.fn(() => Promise.resolve());
    syncAppBadge(badge(set), 3);
    expect(set).toHaveBeenCalledWith(3);
  });

  it("clears the badge once nothing needs the owner", () => {
    const clear = vi.fn(() => Promise.resolve());
    syncAppBadge(badge(undefined, clear), 0);
    expect(clear).toHaveBeenCalledTimes(1);
  });

  it("skips both where the browser has no Badging API", () => {
    expect(() => syncAppBadge(badge(), 3)).not.toThrow();
    expect(() => syncAppBadge(badge(), 0)).not.toThrow();
  });

  it("swallows a badge the browser refuses", () => {
    const set = vi.fn(() => Promise.reject(new Error("no badge")));
    const clear = vi.fn(() => Promise.reject(new Error("no badge")));
    expect(() => syncAppBadge(badge(set, clear), 1)).not.toThrow();
    expect(() => syncAppBadge(badge(set, clear), 0)).not.toThrow();
  });
});

describe("createPushSwitch", () => {
  /** A browser whose push service hands out one subscription at a fixed endpoint. */
  const browser = (
    over: {
      permission?: NotificationPermission;
      granted?: NotificationPermission;
      standalone?: boolean;
    } = {},
  ) => {
    let current: PushSubscriptionLike | null = null;
    const unsubscribe = vi.fn(async () => {
      current = null;
      return true;
    });
    const subscribe = vi.fn(async (_o: { userVisibleOnly: boolean; applicationServerKey: Uint8Array }) => {
      current = {
        endpoint: "https://push.example.test/sub/1",
        toJSON: () => ({ endpoint: "https://push.example.test/sub/1", keys: { p256dh: "pk", auth: "au" } }),
        unsubscribe,
      };
      return current;
    });
    const notification = {
      permission: over.permission ?? "default",
      requestPermission: vi.fn(async () => {
        notification.permission = over.granted ?? "granted";
        return notification.permission;
      }),
    };
    const b: PushBrowser = {
      serviceWorker: {
        ready: Promise.resolve({ pushManager: { getSubscription: async () => current, subscribe } }),
      },
      notification,
      standalone: over.standalone ?? false,
    };
    return { b, subscribe, unsubscribe, notification };
  };
  const apiFake = () =>
    vi.fn(async (path: string, _o?: ApiOptions): Promise<unknown> =>
      path === "/api/push/key" ? { public_key: "AQID_-8" } : { ok: true },
    );

  it("reads a browser with no push support as unsupported", async () => {
    const sw = createPushSwitch({ standalone: false }, apiFake());
    expect(await sw.status()).toEqual({
      supported: false,
      permission: "default",
      on: false,
      standalone: false,
    });
  });

  it("subscribes with the hub's key and hands the subscription to the hub", async () => {
    const { b, subscribe } = browser({ standalone: true });
    const api = apiFake();
    const sw = createPushSwitch(b, api);
    expect(await sw.status()).toEqual({
      supported: true,
      permission: "default",
      on: false,
      standalone: true,
    });
    await sw.enable();
    const opts = subscribe.mock.calls[0]?.[0];
    expect(opts?.userVisibleOnly).toBe(true);
    expect([...(opts?.applicationServerKey ?? [])]).toEqual([1, 2, 3, 255, 239]);
    expect(api).toHaveBeenCalledWith("/api/push/subscribe", {
      body: { endpoint: "https://push.example.test/sub/1", keys: { p256dh: "pk", auth: "au" } },
    });
    expect(await sw.status()).toEqual({ supported: true, permission: "granted", on: true, standalone: true });
  });

  it("refuses when the browser blocks notifications, subscribing nothing", async () => {
    const { b, subscribe } = browser({ granted: "denied" });
    const sw = createPushSwitch(b, apiFake());
    await expect(sw.enable()).rejects.toThrow(/blocked notifications/);
    expect(subscribe).not.toHaveBeenCalled();
  });

  it("ends the hub's copy, then the browser's own subscription", async () => {
    const { b, unsubscribe } = browser({ permission: "granted" });
    const api = apiFake();
    const sw = createPushSwitch(b, api);
    await sw.enable();
    await sw.disable();
    expect(api).toHaveBeenCalledWith(
      `/api/push/subscribe?endpoint=${encodeURIComponent("https://push.example.test/sub/1")}`,
      { method: "DELETE" },
    );
    expect(unsubscribe).toHaveBeenCalledTimes(1);
    expect((await sw.status()).on).toBe(false);
  });

  it("still ends the browser's subscription when the hub has no copy of it", async () => {
    const { b, unsubscribe } = browser({ permission: "granted" });
    const api = apiFake();
    const sw = createPushSwitch(b, api);
    await sw.enable();
    api.mockRejectedValueOnce(new Error("no such subscription"));
    await sw.disable();
    expect(unsubscribe).toHaveBeenCalledTimes(1);
  });

  it("does nothing to disable when there is no subscription", async () => {
    const { b } = browser();
    const api = apiFake();
    await createPushSwitch(b, api).disable();
    expect(api).not.toHaveBeenCalled();
  });
});

describe("reportFocus", () => {
  /** A page whose focus the case flips, with its change, leave and tick hooks in reach. */
  const focusPage = (focused: boolean) => {
    const state = { focused, change: () => {}, leave: () => {}, tick: () => {} };
    const page: FocusPage = {
      focused: () => state.focused,
      onChange: (fn) => {
        state.change = fn;
      },
      onLeave: (fn) => {
        state.leave = fn;
      },
    };
    const timers = {
      interval: (fn: () => void, ms: number) => {
        expect(ms).toBe(30_000);
        state.tick = fn;
      },
    };
    return { state, page, timers };
  };

  it("reports a focused tab at once and on every change", () => {
    const { state, page, timers } = focusPage(true);
    const post = vi.fn(() => Promise.resolve({}));
    reportFocus(page, post, timers);
    expect(post.mock.calls).toEqual([[true]]);
    state.focused = false;
    state.change();
    state.change();
    state.focused = true;
    state.change();
    expect(post.mock.calls).toEqual([[true], [false], [true]]);
  });

  it("repeats the focused report every 30 s, and stays still while hidden", () => {
    const { state, page, timers } = focusPage(false);
    const post = vi.fn(() => Promise.resolve({}));
    reportFocus(page, post, timers);
    expect(post.mock.calls).toEqual([[false]]);
    state.tick();
    expect(post.mock.calls).toEqual([[false]]);
    state.focused = true;
    state.tick();
    state.tick();
    expect(post.mock.calls).toEqual([[false], [true], [true]]);
  });

  it("reports hidden when a focused page goes away", () => {
    const { state, page, timers } = focusPage(true);
    const post = vi.fn(() => Promise.resolve({}));
    reportFocus(page, post, timers);
    state.leave();
    expect(post.mock.calls).toEqual([[true], [false]]);
  });

  it("swallows a report the hub refuses", () => {
    const { page, timers } = focusPage(true);
    expect(() => reportFocus(page, () => Promise.reject(new Error("401")), timers)).not.toThrow();
  });
});

describe("watchForUpdate", () => {
  /** A page whose worker change, return to view and repeat tick the case fires by hand, over a
   *  hub whose /health answers the builds the case queues (an Error fails that check). */
  const updatePage = (
    controlled: boolean,
    answers: readonly (Error | { version: string; executable: string })[],
  ) => {
    const state = { change: () => {}, visible: () => {}, tick: () => {} };
    const page: UpdatePage = {
      controlled,
      onControllerChange: (fn) => {
        state.change = fn;
      },
      onVisible: (fn) => {
        state.visible = fn;
      },
    };
    const queue = [...answers];
    const health = vi.fn(async () => {
      const next = queue.length > 1 ? queue.shift() : queue[0];
      if (next === undefined || next instanceof Error) throw next ?? new Error("no answer");
      return next;
    });
    const timers = {
      interval: (fn: () => void, ms: number) => {
        expect(ms).toBe(60_000);
        state.tick = fn;
      },
    };
    const offer = vi.fn();
    watchForUpdate(page, health, timers, offer);
    return { state, health, offer };
  };
  const OLD = { version: "0.1.0", executable: "/cache/huddle/builds/aaa/huddle" };
  const NEW = { version: "0.1.0", executable: "/cache/huddle/builds/bbb/huddle" };
  /** Lets the checks' promises settle. */
  const settle = () => new Promise((r) => setTimeout(r, 0));

  it("offers the reload when a new worker takes over a page a worker already controlled", async () => {
    const { state, offer } = updatePage(true, [OLD]);
    await settle();
    state.change();
    expect(offer).toHaveBeenCalledTimes(1);
  });

  it("stays silent on a first install, when no worker controlled the page at load", async () => {
    const { state, offer } = updatePage(false, [OLD]);
    await settle();
    state.change();
    expect(offer).not.toHaveBeenCalled();
  });

  it("offers the reload when /health names another build than the page loaded with", async () => {
    const { state, health, offer } = updatePage(false, [OLD, OLD, NEW]);
    await settle();
    state.visible();
    await settle();
    expect(offer).not.toHaveBeenCalled();
    state.tick();
    await settle();
    expect(health).toHaveBeenCalledTimes(3);
    expect(offer).toHaveBeenCalledTimes(1);
  });

  it("offers once, however many checks find the new build", async () => {
    const { state, offer } = updatePage(true, [OLD, NEW]);
    await settle();
    state.visible();
    await settle();
    state.tick();
    await settle();
    state.change();
    expect(offer).toHaveBeenCalledTimes(1);
  });

  it("takes the first answer that lands as the page's build, and a failed check changes nothing", async () => {
    const { state, offer } = updatePage(false, [new Error("hub restarting"), OLD, new Error("down"), OLD]);
    await settle();
    state.tick();
    await settle();
    state.tick();
    await settle();
    state.visible();
    await settle();
    expect(offer).not.toHaveBeenCalled();
  });
});
