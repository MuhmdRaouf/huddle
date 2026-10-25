// watchdog.ts — the guards a live stream needs on a phone, where a resumed page can hold a
// socket that reads open but never delivers again: a silence watchdog that fires when no frame
// arrives in time, a return-to-visible check that fires after a long enough time away, and the
// watched source that puts both around one EventSource. Timers, the clock and the page are
// injected, so the tests drive them without a browser.

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

/** The slice of EventSource a hub stream uses: three handlers and a close. */
export type SourceLike = {
  onopen: (() => void) | null;
  onerror: (() => void) | null;
  onmessage: ((message: { data: string }) => void) | null;
  close: () => void;
};

/** What a watched source reads time and the page from. */
export type StreamWatch = { timers: Timers; now: () => number; page: PageVisibility | null };

/** No frame for this long means the source is dead: the hub pings every 15 s. */
export const STREAM_SILENCE_MS = 40_000;
/** A page back to visible replaces a source whose last frame is older than this: more than one
 *  ping went missing while the page was away. */
export const STREAM_STALE_MS = 20_000;
/** The hub's ping frame, exactly as it writes it. */
const PING = '{"type":"ping"}';

/** The browser's own timers, clock and document, each read at the call so fake timers apply. */
export function browserWatch(): StreamWatch {
  return {
    timers: {
      setTimeout: (fn, ms) => setTimeout(fn, ms),
      clearTimeout: (handle) => clearTimeout(handle as ReturnType<typeof setTimeout>),
    },
    now: () => Date.now(),
    page: typeof document === "undefined" ? null : document,
  };
}

/**
 * One hub stream behind both guards. `open` builds a fresh source; a source silent for
 * STREAM_SILENCE_MS, or one whose last frame is older than STREAM_STALE_MS when the page turns
 * visible, is closed and replaced. The replacement tells `onerror` first, so the page shows the
 * drop, and its `onopen` and first frame reach the handlers the way a reconnect's do. The hub's
 * ping frame feeds the watchdog and never reaches `onmessage`.
 */
export function watchedSource(open: () => SourceLike, watch: StreamWatch = browserWatch()): SourceLike {
  let lastFrameAt = watch.now();
  let closed = false;
  const outer: SourceLike = {
    onopen: null,
    onerror: null,
    onmessage: null,
    close: () => {
      closed = true;
      dog.stop();
      watch.page?.removeEventListener("visibilitychange", onVisibility);
      inner.close();
    },
  };
  const alive = (): void => {
    lastFrameAt = watch.now();
    dog.feed();
  };
  const attach = (): SourceLike => {
    const source = open();
    source.onopen = () => {
      alive();
      outer.onopen?.();
    };
    source.onerror = () => outer.onerror?.();
    source.onmessage = (message) => {
      alive();
      if (message.data !== PING) outer.onmessage?.(message);
    };
    return source;
  };
  const replace = (): void => {
    if (closed) return;
    inner.close();
    outer.onerror?.();
    inner = attach();
    alive();
  };
  const onVisibility = (): void => {
    if (watch.page?.visibilityState !== "visible") return;
    if (watch.now() - lastFrameAt > STREAM_STALE_MS) replace();
  };
  let inner = attach();
  const dog = silenceWatchdog(watch.timers, STREAM_SILENCE_MS, replace);
  watch.page?.addEventListener("visibilitychange", onVisibility);
  return outer;
}
