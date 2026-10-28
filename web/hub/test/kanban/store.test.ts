// store.test.ts — the store keeps the board's closed features from a snapshot and from a delta's
// meta, a snapshot without the field reads as none closed, and the picked feature scopes the
// cards and re-derives the column counts.
import { describe, expect, it } from "vite-plus/test";
import {
  BoardStore,
  NO_FEATURE,
  closedOf,
  featureLabel,
  featureView,
  scopeCards,
  scopeSnapshot,
  staticBoardState,
  type Io,
} from "../../src/kanban/store.ts";
import type { Snapshot, StreamHandlers } from "../../src/kanban/types.ts";
import { clone, makeCard, makeSnapshot, snapshot } from "./helpers.ts";

/** An Io over the given snapshot: one fetch, one stream whose handlers the test keeps. */
function fakeIo(snap: Snapshot) {
  let handlers: StreamHandlers | null = null;
  const io: Io = {
    fetchSnapshot: async () => clone(snap),
    fetchTicket: async () => {
      throw new Error("no ticket in this test");
    },
    connectEvents: (h: StreamHandlers) => {
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
  return { io, stream };
}

const CLOSED = [{ name: "gamma", closedAt: "2026-10-26T10:00:00Z", cards: 7 }];

const CARDS = [
  makeCard({ id: "S-A1", cluster: "alpha", column: "ready" }),
  makeCard({ id: "S-A2", cluster: "alpha", column: "ready" }),
  makeCard({ id: "S-B1", cluster: "beta", column: "backlog" }),
  makeCard({ id: "S-N", cluster: "", column: "backlog" }),
];

describe("Feature: the board's closed features, held by the store", () => {
  it("Scenario: a snapshot's features are kept", async () => {
    // Given the fetch serves a snapshot that carries features
    const { io, stream } = fakeIo(makeSnapshot(CARDS, { features: { closed: CLOSED } }));
    const store = new BoardStore(io);
    await store.start();
    stream().onOpen();
    // Then the state's snapshot keeps them
    expect(store.getState().snapshot?.features?.closed).toEqual(CLOSED);
  });

  it("Scenario: a snapshot without the field reads as none closed", async () => {
    // Given the fetch serves the legacy fixture, which carries no features
    const { io, stream } = fakeIo(snapshot);
    const store = new BoardStore(io);
    await store.start();
    stream().onOpen();
    // Then the closed list reads as empty
    expect(store.getState().snapshot?.features?.closed).toEqual([]);
  });

  it("Scenario: a delta's meta carries the features", async () => {
    // Given the store started from a snapshot without features
    const snap = makeSnapshot(CARDS);
    const { io, stream } = fakeIo(snap);
    const store = new BoardStore(io);
    await store.start();
    stream().onOpen();
    // When a delta arrives whose meta names the closed features
    stream().onDelta({
      rev: snap.rev + 1,
      upsert: [],
      remove: [],
      meta: { epoch: snap.epoch, stopLine: null, columns: snap.columns, features: { closed: CLOSED } },
    });
    // Then the state carries them
    expect(store.getState().snapshot?.features?.closed).toEqual(CLOSED);
  });

  it("Scenario: a delta without meta keeps the features it had", async () => {
    // Given the store started from a snapshot with features
    const snap = makeSnapshot(CARDS, { features: { closed: CLOSED } });
    const { io, stream } = fakeIo(snap);
    const store = new BoardStore(io);
    await store.start();
    stream().onOpen();
    // When a bare delta arrives
    stream().onDelta({ rev: snap.rev + 1, upsert: [], remove: [] });
    // Then the features are still there
    expect(store.getState().snapshot?.features?.closed).toEqual(CLOSED);
  });

  it("Scenario: a static board keeps its data as the page carried it, and its absence reads as none closed", () => {
    // Given the page's own kanban data carries a plain snapshot without the field
    const state = staticBoardState({ __KANBAN__: { snapshot, bodies: {} } });
    // Then the field stays as it came, and the reader reads none closed from its absence
    expect(state?.snapshot?.features).toBeUndefined();
    expect(closedOf(state?.snapshot?.features)).toEqual({ closed: [] });
  });
});

describe("Feature: the picked feature scopes the board", () => {
  it("Scenario: scopeCards keeps the feature's cards, or the ones with none, or all", () => {
    // Given the board's cards over two features and one with none
    // Then All keeps every card
    expect(scopeCards(CARDS, null).map((c) => c.id)).toEqual(["S-A1", "S-A2", "S-B1", "S-N"]);
    // And alpha keeps its two
    expect(scopeCards(CARDS, "alpha").map((c) => c.id)).toEqual(["S-A1", "S-A2"]);
    // And No feature keeps the one without a cluster
    expect(scopeCards(CARDS, NO_FEATURE).map((c) => c.id)).toEqual(["S-N"]);
  });

  it("Scenario: scopeSnapshot re-derives the column counts", () => {
    // Given the board scoped to alpha
    const scoped = scopeSnapshot(makeSnapshot(CARDS), "alpha");
    // Then the cards are the two alphas and the counts follow them
    expect(scoped.cards.map((c) => c.id)).toEqual(["S-A1", "S-A2"]);
    expect(scoped.columns.find((c) => c.id === "ready")?.count).toBe(2);
    expect(scoped.columns.find((c) => c.id === "backlog")?.count).toBe(0);
    expect(scoped.columns.find((c) => c.id === "done")?.count).toBe(0);
  });

  it("Scenario: featureLabel names All, No feature, and the feature itself", () => {
    // Given the three words the address holds
    expect(featureLabel(null)).toBe("All features");
    expect(featureLabel(NO_FEATURE)).toBe("No feature");
    expect(featureLabel("alpha")).toBe("alpha");
  });

  it("Scenario: featureView scopes the board, and a word it does not know reads its own way", () => {
    const snap = makeSnapshot(CARDS, { features: { closed: CLOSED } });
    // Then All features is the board as it came
    expect(featureView(snap, null)).toEqual({ snapshot: snap, cards: CARDS, note: null });
    // And alpha is the scoped board with its two cards
    const scoped = featureView(snap, "alpha");
    expect(scoped.cards.map((c) => c.id)).toEqual(["S-A1", "S-A2"]);
    expect(scoped.note).toBeNull();
    // And a closed feature shows the scoped, empty board and says so
    expect(featureView(snap, "gamma")).toEqual({
      snapshot: scopeSnapshot(snap, "gamma"),
      cards: [],
      note: "closed",
    });
    // And a name no card carries shows the whole board and says so
    const unknown = featureView(snap, "no-such");
    expect(unknown.snapshot).toBe(snap);
    expect(unknown.cards).toEqual(CARDS);
    expect(unknown.note).toBe("unknown");
  });
});
