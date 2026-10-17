// pwa.ts — the installability glue: the app-shell service worker's registration and the app
// badge (SPEC 7.1). Both are optional browser features a page must live without: registration
// happens only on a secure context (localhost or https), the badge only where the Badging API
// exists, and a call that fails changes nothing. No DOM here; the browser is injected.

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
