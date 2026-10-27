// browser-notify.test.ts — the page's desktop notifications over a stubbed Notification: the
// options carry the full-colour icon beside the body and tag, a click closes the notification
// and raises the page, and the permission calls read the same constructor the show uses.

import { describe, expect, it, vi } from "vite-plus/test";
import { browserNotifications } from "../../src/app/browser-notify.ts";

/** One constructed notification, as the page's show call left it. */
type Made = {
  title: string;
  options: NotificationOptions;
  onclick: (() => void) | null;
  closed: boolean;
};

/** Stubs the Notification constructor on globalThis and answers what it built. */
function stubNotification(permission: NotificationPermission = "granted"): Made[] {
  const made: Made[] = [];
  const Fake = class {
    title: string;
    options: NotificationOptions;
    onclick: (() => void) | null = null;
    closed = false;
    constructor(title: string, options: NotificationOptions) {
      this.title = title;
      this.options = options;
      made.push(this);
    }
    close(): void {
      this.closed = true;
    }
    static permission = permission;
    static requestPermission = () => Promise.resolve(permission);
  };
  (globalThis as { Notification: unknown }).Notification = Fake;
  return made;
}

describe("browserNotifications", () => {
  it("shows a notification with the body, tag and full-colour icon", () => {
    const made = stubNotification();
    browserNotifications().show("t", "b", "g", () => {});
    expect(made).toHaveLength(1);
    const n = made[0];
    if (!n) throw new Error("nothing constructed");
    expect(n.title).toBe("t");
    expect(n.options).toEqual({ body: "b", tag: "g", icon: "/icons/icon-192.png" });
    expect(n.closed).toBe(false);
  });

  it("closes the notification and calls back on a click", () => {
    const made = stubNotification();
    const onClick = vi.fn();
    browserNotifications().show("t", "b", "g", onClick);
    const n = made[0];
    if (!n) throw new Error("nothing constructed");
    n.onclick?.();
    expect(n.closed).toBe(true);
    expect(onClick).toHaveBeenCalledTimes(1);
  });

  it("reads the browser's permission through the same constructor", async () => {
    stubNotification("denied");
    const notify = browserNotifications();
    expect(notify.supported()).toBe(true);
    expect(notify.permission()).toBe("denied");
    await expect(notify.requestPermission()).resolves.toBe("denied");
  });
});
