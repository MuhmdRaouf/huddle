// pwa.test.ts — the installability glue: the service worker registers only on a secure context
// (localhost or https), and the app badge mirrors the open needs-you count where the Badging
// API exists. Both are optional: a browser without them, or a call that fails, changes nothing
// the app does.
import { describe, expect, it, vi } from "vite-plus/test";
import { type BadgeNavigator, registerServiceWorker, type SWWindow, syncAppBadge } from "../src/pwa.ts";

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
