// store-sync.test.ts — a stream that goes quiet without an error (a half-open socket after a
// phone resumes the page) is replaced: 40 s without a frame closes it, refetches the board and
// opens a fresh one with the live dot off meanwhile, and a return to visible after more than
// 30 s hidden does the same at once.
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { BoardStore, type Io, staticBoardState } from "../../src/kanban/store.ts";
import type { StreamHandlers } from "../../src/kanban/types.ts";
import { type PageVisibility, resumeOnReturn } from "../../src/shared/watchdog.ts";
import { clone, snapshot } from "./helpers.ts";

/** An Io over the fixture: fetches, opens and closes counted, the newest stream's handlers kept. */
function fakeIo() {
  const calls = { snapshot: 0, connect: 0, close: 0 };
  let handlers: StreamHandlers | null = null;
  const io: Io = {
    fetchSnapshot: async () => {
      calls.snapshot += 1;
      return clone(snapshot);
    },
    fetchTicket: async () => {
      throw new Error("no ticket in this test");
    },
    connectEvents: (h: StreamHandlers) => {
      calls.connect += 1;
      handlers = h;
      return { close: () => (calls.close += 1) };
    },
    setTimeout: (fn: () => void, ms: number) => setTimeout(fn, ms),
    clearTimeout: (handle: unknown) => clearTimeout(handle as ReturnType<typeof setTimeout>),
  };
  const stream = (): StreamHandlers => {
    if (handlers === null) throw new Error("no stream opened");
    return handlers;
  };
  return { io, calls, stream };
}

/** A page whose visibility the test sets, firing visibilitychange the way the browser does. */
function fakePage(): PageVisibility & { turn: (state: DocumentVisibilityState) => void } {
  const target = new EventTarget();
  let state: DocumentVisibilityState = "visible";
  return {
    get visibilityState() {
      return state;
    },
    addEventListener: (type, fn) => target.addEventListener(type, fn),
    removeEventListener: (type, fn) => target.removeEventListener(type, fn),
    turn: (next) => {
      state = next;
      target.dispatchEvent(new Event("visibilitychange"));
    },
  };
}

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("the silence watchdog", () => {
  it("replaces a stream silent for 40 s: closed, dot off, board refetched, stream reopened", async () => {
    const { io, calls, stream } = fakeIo();
    const store = new BoardStore(io);
    await store.start();
    stream().onOpen();
    expect(store.getState().connected).toBe(true);
    await vi.advanceTimersByTimeAsync(39_999);
    expect(calls).toEqual({ snapshot: 1, connect: 1, close: 0 });
    await vi.advanceTimersByTimeAsync(1);
    expect(calls.close).toBe(1);
    expect(store.getState().connected).toBe(false);
    await vi.waitFor(() => expect(calls.connect).toBe(2));
    expect(calls.snapshot).toBe(2);
    // the dot stays off until the fresh stream answers
    expect(store.getState().connected).toBe(false);
    stream().onOpen();
    expect(store.getState().connected).toBe(true);
    store.stop();
  });

  it("counts every frame, ping included, as a sign of life", async () => {
    const { io, calls, stream } = fakeIo();
    const store = new BoardStore(io);
    await store.start();
    stream().onOpen();
    await vi.advanceTimersByTimeAsync(30_000);
    stream().onPing();
    await vi.advanceTimersByTimeAsync(30_000);
    stream().onSnapshot(clone(snapshot));
    await vi.advanceTimersByTimeAsync(39_000);
    expect(calls.connect).toBe(1);
    await vi.advanceTimersByTimeAsync(1_000);
    await vi.waitFor(() => expect(calls.connect).toBe(2));
    store.stop();
  });

  it("opens one stream at start, even when the first board read fails", async () => {
    const { io, calls } = fakeIo();
    let first = true;
    const fetchSnapshot = io.fetchSnapshot;
    io.fetchSnapshot = async () => {
      if (first) {
        first = false;
        calls.snapshot += 1;
        throw new Error("hub down");
      }
      return fetchSnapshot();
    };
    const store = new BoardStore(io);
    await store.start();
    expect(calls.connect).toBe(0);
    await vi.advanceTimersByTimeAsync(1_000);
    expect(calls.connect).toBe(1);
    store.stop();
  });

  it("never fires once the store stops", async () => {
    const { io, calls } = fakeIo();
    const store = new BoardStore(io);
    await store.start();
    store.stop();
    await vi.advanceTimersByTimeAsync(120_000);
    expect(calls).toEqual({ snapshot: 1, connect: 1, close: 1 });
  });

  it("stays off on an exported board, which has no stream to watch", async () => {
    const { io, calls } = fakeIo();
    const store = new BoardStore(io, staticBoardState({ __KANBAN__: { snapshot, bodies: {} } }));
    await store.start();
    await vi.advanceTimersByTimeAsync(120_000);
    expect(calls.snapshot).toBe(1);
    expect(calls.connect).toBe(1);
    store.stop();
  });
});

describe("resume on return", () => {
  it("resnapshots and reopens when the page turns visible after more than 30 s hidden", async () => {
    const { io, calls, stream } = fakeIo();
    const store = new BoardStore(io);
    const page = fakePage();
    const release = resumeOnReturn(
      page,
      30_000,
      () => Date.now(),
      () => store.resume(),
    );
    await store.start();
    stream().onOpen();
    page.turn("hidden");
    await vi.advanceTimersByTimeAsync(30_001);
    page.turn("visible");
    expect(store.getState().connected).toBe(false);
    expect(calls.close).toBe(1);
    await vi.waitFor(() => expect(calls.connect).toBe(2));
    expect(calls.snapshot).toBe(2);
    release();
    store.stop();
  });

  it("leaves a stream alone after a short trip away", async () => {
    const { io, calls, stream } = fakeIo();
    const store = new BoardStore(io);
    const page = fakePage();
    const release = resumeOnReturn(
      page,
      30_000,
      () => Date.now(),
      () => store.resume(),
    );
    await store.start();
    stream().onOpen();
    page.turn("hidden");
    await vi.advanceTimersByTimeAsync(30_000);
    page.turn("visible");
    await vi.advanceTimersByTimeAsync(0);
    expect(calls).toEqual({ snapshot: 1, connect: 1, close: 0 });
    expect(store.getState().connected).toBe(true);
    release();
    store.stop();
  });

  it("stops listening once released", async () => {
    const page = fakePage();
    const onReturn = vi.fn();
    const release = resumeOnReturn(page, 30_000, () => Date.now(), onReturn);
    release();
    page.turn("hidden");
    await vi.advanceTimersByTimeAsync(60_000);
    page.turn("visible");
    expect(onReturn).not.toHaveBeenCalled();
  });
});
