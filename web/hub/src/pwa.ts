// pwa.ts — the installability glue: the app-shell service worker's registration, the app
// badge (SPEC 7.1), the push switch (SPEC 7.6) and the focus report that keeps the other devices
// quiet while this tab is visible and focused. All are optional browser features a page must
// live without: registration happens only on a secure context (localhost or https), the badge
// only where the Badging API exists, push only where the worker and the Push API exist, and a
// call that fails changes nothing. No DOM here; the browser is injected.

import type { ApiOptions } from "./api.ts";

/** The slice of window the registration needs. */
export type SWWindow = {
  /** True on https and on localhost, the only places a worker may register. */
  isSecureContext: boolean;
  navigator: { serviceWorker?: { register(scriptURL: string): Promise<unknown> } };
};

/** Registers /sw.js, the app-shell service worker, where the browser and the context allow it. */
export function registerServiceWorker(win: SWWindow, scriptURL = "/sw.js"): void {
  const worker = win.navigator.serviceWorker;
  if (!win.isSecureContext || !worker) return;
  worker.register(scriptURL).catch(() => {
    /* a worker that cannot register leaves the page as it is: the network serves everything */
  });
}

/** The slice of navigator the Badging API adds. */
export type BadgeNavigator = {
  /** Sets the count the launcher or browser bar shows. */
  setAppBadge?(count?: number): Promise<void>;
  /** Removes the badge. */
  clearAppBadge?(): Promise<void>;
};

/** Sets the app badge to the open needs-you count, or clears it once nothing needs the owner. */
export function syncAppBadge(nav: BadgeNavigator, count: number): void {
  if (count > 0) {
    if (nav.setAppBadge) nav.setAppBadge(count).catch(() => {});
    return;
  }
  if (nav.clearAppBadge) nav.clearAppBadge().catch(() => {});
}

/** The slice of a PushSubscription the switch reads. */
export type PushSubscriptionLike = {
  endpoint: string;
  toJSON(): { endpoint?: string; keys?: Record<string, string> };
  unsubscribe(): Promise<boolean>;
};

/** The slice of a worker registration's PushManager the switch uses. */
export type PushManagerLike = {
  getSubscription(): Promise<PushSubscriptionLike | null>;
  subscribe(o: {
    userVisibleOnly: boolean;
    applicationServerKey: Uint8Array<ArrayBuffer>;
  }): Promise<PushSubscriptionLike>;
};

/** The slice of the browser push needs: the worker's registration, the notification permission
 *  and whether the page runs as the installed app. A browser without push names neither. */
export type PushBrowser = {
  serviceWorker?: { ready: Promise<{ pushManager: PushManagerLike }> };
  notification?: {
    permission: NotificationPermission;
    requestPermission(): Promise<NotificationPermission>;
  };
  /** True when the page runs as the app added to the home screen; iOS pushes only there. */
  standalone: boolean;
};

/** What the switch shows: can this browser take push, the permission, is it subscribed, and is
 *  the page the installed app. */
export type PushStatus = {
  supported: boolean;
  permission: NotificationPermission;
  on: boolean;
  standalone: boolean;
};

/** The push switch: its status, and turning it on or off. */
export type PushSwitch = {
  status(): Promise<PushStatus>;
  enable(): Promise<void>;
  disable(): Promise<void>;
};

/** The bytes of one base64url string: the VAPID public key the hub answers. */
function base64UrlBytes(text: string): Uint8Array<ArrayBuffer> {
  const plain = atob(
    text
      .replace(/-/g, "+")
      .replace(/_/g, "/")
      .padEnd(Math.ceil(text.length / 4) * 4, "="),
  );
  return Uint8Array.from(plain, (c) => c.charCodeAt(0));
}

/** The push switch over one browser and the hub's API: on subscribes with the hub's VAPID key
 *  and hands the subscription to the hub; off ends the hub's copy, then the browser's own. */
export function createPushSwitch(
  browser: PushBrowser,
  api: (path: string, o?: ApiOptions) => Promise<unknown>,
): PushSwitch {
  const manager = async (): Promise<PushManagerLike | null> =>
    browser.serviceWorker ? (await browser.serviceWorker.ready).pushManager : null;
  return {
    async status() {
      const supported = browser.serviceWorker !== undefined && browser.notification !== undefined;
      const pm = supported ? await manager() : null;
      return {
        supported,
        permission: browser.notification?.permission ?? "default",
        on: (await pm?.getSubscription()) != null,
        standalone: browser.standalone,
      };
    },
    async enable() {
      const pm = await manager();
      if (!pm || !browser.notification) throw new Error("This browser cannot receive push.");
      if ((await browser.notification.requestPermission()) !== "granted") {
        throw new Error("The browser blocked notifications for this page. Allow them in the site settings.");
      }
      const { public_key: key } = (await api("/api/push/key")) as { public_key: string };
      const sub =
        (await pm.getSubscription()) ??
        (await pm.subscribe({ userVisibleOnly: true, applicationServerKey: base64UrlBytes(key) }));
      await api("/api/push/subscribe", { body: sub.toJSON() });
    },
    async disable() {
      const sub = await (await manager())?.getSubscription();
      if (!sub) return;
      try {
        await api(`/api/push/subscribe?endpoint=${encodeURIComponent(sub.endpoint)}`, { method: "DELETE" });
      } catch {
        // the hub holds no copy (already dropped, or another device's): the browser's own still ends
      }
      await sub.unsubscribe();
    },
  };
}

/** The slice of the page the focus report reads: is it visible and focused, and its changes. */
export type FocusPage = {
  /** True while the page is visible and holds the focus. */
  focused(): boolean;
  /** Calls fn on every visibility or focus change. */
  onChange(fn: () => void): void;
  /** Calls fn when the page goes away. */
  onLeave(fn: () => void): void;
};

/** The timer the focus report repeats on. */
export type FocusTimers = { interval(fn: () => void, ms: number): unknown };

/** How often a focused tab repeats its report; the hub holds each report for 60 s. */
const FOCUS_REPORT_MS = 30_000;

/** Tells the hub whether this tab is visible and focused: at once, on every change, every 30 s
 *  while focused, and hidden when a focused page goes away. While a tab is focused the hub keeps
 *  every other device's pushes quiet. A report the hub refuses changes nothing. */
export function reportFocus(
  page: FocusPage,
  post: (focused: boolean) => Promise<unknown>,
  timers: FocusTimers,
): void {
  let last: boolean | null = null;
  const report = (focused: boolean): void => {
    last = focused;
    post(focused).catch(() => {});
  };
  const onChange = (): void => {
    const focused = page.focused();
    if (focused !== last) report(focused);
  };
  page.onChange(onChange);
  page.onLeave(() => {
    if (last) report(false);
  });
  timers.interval(() => {
    if (page.focused()) report(true);
  }, FOCUS_REPORT_MS);
  onChange();
}

/** The slice of the page the update watch reads. */
export type UpdatePage = {
  /** True when a worker controlled the page at load; a first install starts with none. */
  controlled: boolean;
  /** Calls fn when another worker takes control of the page. */
  onControllerChange(fn: () => void): void;
  /** Calls fn when the page comes back into view. */
  onVisible(fn: () => void): void;
};

/** The part of the hub's /health answer that names the build it serves. */
export type HealthBuild = { version?: unknown; executable?: unknown };

/** How often an open page asks the hub which build it serves. */
const UPDATE_CHECK_MS = 60_000;

/** Offers a reload, once, when the page runs an old build: a new worker takes over a page a
 *  worker already controlled, or /health names another build than the first answer did. The
 *  build is the version and the executable path, which moves with every launcher build. A first
 *  install, or a check that fails, offers nothing. */
export function watchForUpdate(
  page: UpdatePage,
  health: () => Promise<HealthBuild>,
  timers: FocusTimers,
  offer: () => void,
): void {
  let offered = false;
  let loaded: string | null = null;
  const once = (): void => {
    if (offered) return;
    offered = true;
    offer();
  };
  const check = (): void => {
    health()
      .then((h) => {
        if (typeof h.version !== "string" || typeof h.executable !== "string") return;
        const build = `${h.version} ${h.executable}`;
        if (loaded === null) loaded = build;
        else if (build !== loaded) once();
      })
      .catch(() => {});
  };
  page.onControllerChange(() => {
    if (page.controlled) once();
  });
  page.onVisible(check);
  timers.interval(check, UPDATE_CHECK_MS);
  check();
}
