// watchdog.ts — the two guards a live stream needs on a phone, where a resumed page can hold a
// socket that reads open but never delivers again: a silence watchdog that fires when no frame
// arrives in time, and a return-to-visible check that fires after a long enough time away.
// Timers, the clock and the page are injected, so the tests drive them without a browser.

/** The timer pair a watchdog arms and clears. */
export type Timers = {
  setTimeout: (fn: () => void, ms: number) => unknown;
  clearTimeout: (handle: unknown) => void;
};

/** A running watchdog: `feed` on every frame, `stop` when the stream closes. */
export type Watchdog = { feed: () => void; stop: () => void };

/** Calls `onSilent` once after `silenceMs` pass without a feed. It starts armed, every feed
 *  restarts the count, and a stopped watchdog never fires. */
export function silenceWatchdog(timers: Timers, silenceMs: number, onSilent: () => void): Watchdog {
  let handle: unknown = null;
  const disarm = (): void => {
    if (handle !== null) timers.clearTimeout(handle);
    handle = null;
  };
  const arm = (): void => {
    disarm();
    handle = timers.setTimeout(() => {
      handle = null;
      onSilent();
    }, silenceMs);
  };
  arm();
  return { feed: arm, stop: disarm };
}

/** The slice of `document` the return check reads. */
export type PageVisibility = {
  readonly visibilityState: DocumentVisibilityState;
  addEventListener: (type: "visibilitychange", fn: () => void) => void;
  removeEventListener: (type: "visibilitychange", fn: () => void) => void;
};

/** Calls `onReturn` when the page turns visible after more than `awayMs` hidden. Returns the
 *  release that stops listening. */
export function resumeOnReturn(
  page: PageVisibility,
  awayMs: number,
  now: () => number,
  onReturn: () => void,
): () => void {
  let hiddenAt: number | null = null;
  const onChange = (): void => {
    if (page.visibilityState === "hidden") {
      hiddenAt ??= now();
      return;
    }
    const away = hiddenAt === null ? 0 : now() - hiddenAt;
    hiddenAt = null;
    if (away > awayMs) onReturn();
  };
  page.addEventListener("visibilitychange", onChange);
  return () => page.removeEventListener("visibilitychange", onChange);
}
