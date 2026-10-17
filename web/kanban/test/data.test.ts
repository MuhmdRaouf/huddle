import { describe, expect, it, vi } from "vite-plus/test";
import {
  applyDelta,
  BoardStore,
  backoffMs,
  changedIds,
  type Io,
  isRevGap,
  staticBoardState,
} from "../src/store.ts";
import type { StreamHandlers } from "../src/types.ts";
import { cardOf, clone, makeSnapshot, snapshot, ticketApi, ticketUi } from "./helpers.ts";

/** An Io over the fixtures: every call counted, the stream's handlers kept reachable. */
function fakeIo(snap = snapshot) {
  const calls = { snapshot: 0, connect: 0, ticket: 0 };
  let handlers: StreamHandlers | null = null;
  const io: Io = {
    fetchSnapshot: async () => {
      calls.snapshot += 1;
      return clone(snap);
    },
    fetchTicket: async (id: string) => {
      calls.ticket += 1;
      return clone(id === "KBN-UI" ? ticketUi : ticketApi);
    },
    connectEvents: (h: StreamHandlers) => {
      calls.connect += 1;
      handlers = h;
      return { close: () => undefined };
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

describe("applyDelta", () => {
  it("upserts a card in place, keeping the snapshot's order", () => {
    const before = makeSnapshot([cardOf("KBN-API"), cardOf("KBN-UI")]);
    const renamed = { ...cardOf("KBN-UI"), title: "renamed" };
    const after = applyDelta(before, { rev: before.rev + 1, upsert: [renamed], remove: [] });
    expect(after.cards.map((c) => c.id)).toEqual(["KBN-API", "KBN-UI"]);
    expect(after.cards[1]?.title).toBe("renamed");
    expect(after.rev).toBe(before.rev + 1);
  });

  it("appends a card the board did not have", () => {
    const before = makeSnapshot([cardOf("KBN-API")]);
    const after = applyDelta(before, { rev: before.rev + 1, upsert: [cardOf("KBN-SKILL")], remove: [] });
    expect(after.cards.map((c) => c.id)).toEqual(["KBN-API", "KBN-SKILL"]);
  });

  it("drops the ids the delta removes", () => {
    const before = makeSnapshot([cardOf("KBN-API"), cardOf("KBN-UI")]);
    const after = applyDelta(before, { rev: before.rev + 1, upsert: [], remove: ["KBN-API"] });
    expect(after.cards.map((c) => c.id)).toEqual(["KBN-UI"]);
  });

  it("replaces epoch, stop line and columns when the delta carries meta", () => {
    const stop = { epoch: 4, actor: "raouf", note: "broken main", ts: "2026-10-15T21:00:00+03:00" };
    const columns = [{ id: "backlog" as const, title: "Backlog", count: 0 }];
    const after = applyDelta(snapshot, {
      rev: snapshot.rev + 1,
      upsert: [],
      remove: [],
      meta: { epoch: 4, stopLine: stop, columns },
    });
    expect(after.epoch).toBe(4);
    expect(after.stopLine).toEqual(stop);
    expect(after.columns).toEqual(columns);
  });

  it("keeps the meta it was not given", () => {
    const after = applyDelta(snapshot, { rev: snapshot.rev + 1, upsert: [], remove: [] });
    expect(after.epoch).toBe(snapshot.epoch);
    expect(after.stopLine).toBe(snapshot.stopLine);
    expect(after.columns).toBe(snapshot.columns);
  });

  it("never mutates the snapshot it was handed", () => {
    const before = makeSnapshot([cardOf("KBN-API")]);
    applyDelta(before, { rev: before.rev + 1, upsert: [cardOf("KBN-UI")], remove: [] });
    expect(before.cards.map((c) => c.id)).toEqual(["KBN-API"]);
  });
});

describe("rev gaps", () => {
  it("flags a delta that skips ahead of snapshot.rev + 1", () => {
    expect(isRevGap(snapshot, { rev: snapshot.rev + 2, upsert: [], remove: [] })).toBe(true);
  });

  it("flags a stale delta repeating a rev already applied", () => {
    expect(isRevGap(snapshot, { rev: snapshot.rev, upsert: [], remove: [] })).toBe(true);
  });

  it("accepts the delta that follows the snapshot", () => {
    expect(isRevGap(snapshot, { rev: snapshot.rev + 1, upsert: [], remove: [] })).toBe(false);
  });
});

describe("changedIds", () => {
  it("names the cards whose updatedAt the delta changed", () => {
    const moved = { ...cardOf("KBN-UI"), updatedAt: "2026-10-15T21:00:00+03:00" };
    const untouched = cardOf("KBN-PLUGIN");
    const ids = changedIds(snapshot.cards, { rev: 13, upsert: [moved, untouched], remove: [] });
    expect(ids).toEqual(new Set(["KBN-UI"]));
  });

  it("does not flash a card the board first sees in this delta", () => {
    const born = { ...cardOf("KBN-SKILL"), id: "KBN-NEW" };
    expect(changedIds(snapshot.cards, { rev: 13, upsert: [born], remove: [] }).size).toBe(0);
  });
});

describe("backoffMs", () => {
  it("waits 1, 2, 4 then 8 s, capping at 8", () => {
    expect([0, 1, 2, 3, 4, 9].map(backoffMs)).toEqual([1000, 2000, 4000, 8000, 8000, 8000]);
  });
});

describe("BoardStore", () => {
  it("loads the snapshot, then follows the stream: open, delta, reload", async () => {
    const { io, calls, stream } = fakeIo();
    const store = new BoardStore(io);
    await store.start();
    expect(calls.snapshot).toBe(1);
    expect(calls.connect).toBe(1);
    expect(store.getState().snapshot?.rev).toBe(snapshot.rev);
    stream().onOpen();
    expect(store.getState().connected).toBe(true);
    stream().onDelta({
      rev: snapshot.rev + 1,
      upsert: [{ ...cardOf("KBN-UI"), holder: "glm-2", updatedAt: "2026-10-15T21:00:00+03:00" }],
      remove: [],
    });
    expect(store.getState().snapshot?.rev).toBe(snapshot.rev + 1);
    expect(store.getState().snapshot?.cards.find((c) => c.id === "KBN-UI")?.holder).toBe("glm-2");
    stream().onReload();
    await vi.waitFor(() => expect(calls.snapshot).toBe(2));
  });

  it("refetches the board instead of applying a delta with a rev gap", async () => {
    const { io, calls, stream } = fakeIo();
    const store = new BoardStore(io);
    await store.start();
    stream().onDelta({
      rev: snapshot.rev + 3,
      upsert: [{ ...cardOf("KBN-API"), title: "gap renamed" }],
      remove: [],
    });
    await vi.waitFor(() => expect(calls.snapshot).toBe(2));
    const api = store.getState().snapshot?.cards.find((c) => c.id === "KBN-API");
    expect(api?.title).toBe("kanban: HTTP API, SSE stream and watcher");
  });

  it("tells its listeners whenever the state moves", async () => {
    const { io } = fakeIo();
    const store = new BoardStore(io);
    const listener = vi.fn();
    store.subscribe(listener);
    await store.start();
    expect(listener).toHaveBeenCalled();
  });

  it("caches a ticket by updatedAt and refetches only after the card changed", async () => {
    const { io, calls, stream } = fakeIo();
    const store = new BoardStore(io);
    await store.start();
    const first = await store.ticket("KBN-UI");
    expect(calls.ticket).toBe(1);
    expect((await store.ticket("KBN-UI")).body).toBe(first.body);
    expect(calls.ticket).toBe(1);
    stream().onDelta({
      rev: snapshot.rev + 1,
      upsert: [{ ...cardOf("KBN-UI"), updatedAt: "2026-10-15T21:00:00+03:00" }],
      remove: [],
    });
    await store.ticket("KBN-UI");
    expect(calls.ticket).toBe(2);
  });

  it("reconnects after 1, 2, 4, 8, 8 s, and the ladder resets when a stream opens", async () => {
    vi.useFakeTimers();
    try {
      const { io, calls, stream } = fakeIo();
      const store = new BoardStore(io);
      await store.start();
      expect(calls.connect).toBe(1);
      stream().onError();
      expect(store.getState().connected).toBe(false);
      await vi.advanceTimersByTimeAsync(999);
      expect(calls.connect).toBe(1);
      await vi.advanceTimersByTimeAsync(1);
      expect(calls.connect).toBe(2);
      stream().onError();
      await vi.advanceTimersByTimeAsync(2000);
      expect(calls.connect).toBe(3);
      stream().onError();
      await vi.advanceTimersByTimeAsync(4000);
      expect(calls.connect).toBe(4);
      stream().onError();
      await vi.advanceTimersByTimeAsync(8000);
      expect(calls.connect).toBe(5);
      stream().onError();
      await vi.advanceTimersByTimeAsync(8000);
      expect(calls.connect).toBe(6);
      stream().onOpen();
      stream().onError();
      await vi.advanceTimersByTimeAsync(1000);
      expect(calls.connect).toBe(7);
      await vi.advanceTimersByTimeAsync(0);
      expect(calls.connect).toBe(7);
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("static mode", () => {
  it("reads the snapshot and the bodies from window.__KANBAN__ and reports static mode", () => {
    const state = staticBoardState({ __KANBAN__: { snapshot, bodies: { "KBN-UI": ticketUi } } });
    expect(state?.mode).toBe("static");
    expect(state?.snapshot).toEqual(snapshot);
  });

  it("returns null for a live window", () => {
    expect(staticBoardState({})).toBeNull();
    expect(staticBoardState(undefined)).toBeNull();
  });
});
