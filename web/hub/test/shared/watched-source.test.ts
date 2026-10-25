// watched-source.test.ts — the one guard every hub stream runs behind: a source silent for 40 s
// is closed and a fresh one opened, with the error handler told so the page shows the drop; a
// return to visible replaces a source whose last frame is more than 20 s old; the hub's ping
// frame counts as a sign of life and never reaches the stream's own handler.
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import {
  type PageVisibility,
  type SourceLike,
  STREAM_SILENCE_MS,
  STREAM_STALE_MS,
  type StreamWatch,
  watchedSource,
} from "../../src/shared/watchdog.ts";

/** Every source the watched stream opened, in order, each with a closed flag. */
function fakeSources() {
  const opened: (SourceLike & { closed: boolean })[] = [];
  const open = (): SourceLike => {
    const source = {
      onopen: null,
      onerror: null,
      onmessage: null,
      closed: false,
      close() {
        source.closed = true;
      },
    } as SourceLike & { closed: boolean };
    opened.push(source);
    return source;
  };
  const last = (): SourceLike & { closed: boolean } => {
    const source = opened.at(-1);
    if (source === undefined) throw new Error("no source opened");
    return source;
  };
  return { opened, open, last };
}

/** A page whose visibility the test sets, firing visibilitychange the way the browser does. */
function fakePage(): PageVisibility & {
  turn: (state: DocumentVisibilityState) => void;
  listeners: () => number;
} {
  const fns = new Set<() => void>();
  let state: DocumentVisibilityState = "visible";
  return {
    get visibilityState() {
      return state;
    },
    addEventListener: (_type, fn) => fns.add(fn),
    removeEventListener: (_type, fn) => fns.delete(fn),
    turn: (next) => {
      state = next;
      for (const fn of fns) fn();
    },
    listeners: () => fns.size,
  };
}

function watchOver(page: PageVisibility | null): StreamWatch {
  return {
    timers: {
      setTimeout: (fn, ms) => setTimeout(fn, ms),
      clearTimeout: (handle) => clearTimeout(handle as ReturnType<typeof setTimeout>),
    },
    now: () => Date.now(),
    page,
  };
}

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("a watched source", () => {
  it("waits 40 s and 20 s: the silence and the staleness it acts on", () => {
    expect(STREAM_SILENCE_MS).toBe(40_000);
    expect(STREAM_STALE_MS).toBe(20_000);
  });

  it("hands open, message and error on, and keeps the hub's ping to itself", () => {
    const { open, last } = fakeSources();
    const stream = watchedSource(open, watchOver(null));
    const seen: string[] = [];
    stream.onopen = () => seen.push("open");
    stream.onerror = () => seen.push("error");
    stream.onmessage = (message) => seen.push(message.data);
    last().onopen?.();
    last().onmessage?.({ data: '{"type":"hello"}' });
    last().onmessage?.({ data: '{"type":"ping"}' });
    last().onerror?.();
    expect(seen).toEqual(["open", '{"type":"hello"}', "error"]);
    stream.close();
    expect(last().closed).toBe(true);
  });

  it("replaces a source silent for 40 s: closed, the error told, a fresh one opened", () => {
    const { opened, open, last } = fakeSources();
    const stream = watchedSource(open, watchOver(null));
    const errors = vi.fn();
    stream.onerror = errors;
    last().onopen?.();
    vi.advanceTimersByTime(39_999);
    expect(opened).toHaveLength(1);
    vi.advanceTimersByTime(1);
    expect(opened).toHaveLength(2);
    expect(opened[0]?.closed).toBe(true);
    expect(errors).toHaveBeenCalledTimes(1);
    // the fresh source's frames reach the stream's handlers as the first one's did
    const seen = vi.fn();
    stream.onmessage = seen;
    last().onmessage?.({ data: '{"type":"hello"}' });
    expect(seen).toHaveBeenCalledWith({ data: '{"type":"hello"}' });
    stream.close();
  });

  it("counts every frame, the ping included, as a sign of life", () => {
    const { opened, open, last } = fakeSources();
    const stream = watchedSource(open, watchOver(null));
    last().onopen?.();
    vi.advanceTimersByTime(30_000);
    last().onmessage?.({ data: '{"type":"ping"}' });
    vi.advanceTimersByTime(30_000);
    last().onmessage?.({ data: '{"type":"registry.session"}' });
    vi.advanceTimersByTime(39_000);
    expect(opened).toHaveLength(1);
    vi.advanceTimersByTime(1_000);
    expect(opened).toHaveLength(2);
    stream.close();
  });

  it("replaces a source on a return to visible when its last frame is more than 20 s old", () => {
    const { opened, open, last } = fakeSources();
    const page = fakePage();
    const stream = watchedSource(open, watchOver(page));
    last().onopen?.();
    // a short time away with a fresh frame keeps the source
    page.turn("hidden");
    vi.advanceTimersByTime(10_000);
    page.turn("visible");
    expect(opened).toHaveLength(1);
    // a frame 20 s old or less still keeps it
    vi.advanceTimersByTime(10_000);
    page.turn("hidden");
    page.turn("visible");
    expect(opened).toHaveLength(1);
    // more than 20 s since the last frame: the source is replaced at once
    vi.advanceTimersByTime(1);
    page.turn("hidden");
    page.turn("visible");
    expect(opened).toHaveLength(2);
    expect(opened[0]?.closed).toBe(true);
    stream.close();
  });

  it("stops every guard once closed", () => {
    const { opened, open } = fakeSources();
    const page = fakePage();
    const stream = watchedSource(open, watchOver(page));
    expect(page.listeners()).toBe(1);
    stream.close();
    expect(page.listeners()).toBe(0);
    vi.advanceTimersByTime(120_000);
    expect(opened).toHaveLength(1);
  });

  it("runs on the browser's own timers, clock and document when no watch is given", () => {
    const { opened, open } = fakeSources();
    const stream = watchedSource(open);
    vi.advanceTimersByTime(STREAM_SILENCE_MS);
    expect(opened).toHaveLength(2);
    stream.close();
  });
});
