// groups.test.ts — the phone's backlog grouping: priority with P0 open by default, remembered per
// board through localStorage, and a storage that throws or holds junk never breaks the board; and
// groupCards, the pure grouping the backlog's headers are drawn from.
import { describe, expect, it } from "vite-plus/test";
import { GROUPS_KEY, groupCards, readGroups, writeGroups } from "../../src/kanban/store.ts";
import { makeCard } from "./helpers.ts";

/** A storage that remembers, the way localStorage does. */
function fakeStorage(): Storage {
  const map = new Map<string, string>();
  return {
    get length() {
      return map.size;
    },
    key: () => null,
    clear: () => map.clear(),
    getItem: (k: string) => map.get(k) ?? null,
    setItem: (k: string, v: string) => void map.set(k, v),
    removeItem: (k: string) => void map.delete(k),
  };
}

/** A storage whose every method throws, the way a blocked partition does. */
const throwingStorage = {
  getItem: () => {
    throw new Error("denied");
  },
  setItem: () => {
    throw new Error("denied");
  },
} as unknown as Storage;

describe("Feature: the backlog grouping is remembered per board", () => {
  it("Scenario: a board with nothing stored groups by priority with P0 open", () => {
    expect(readGroups(fakeStorage(), "a1a1")).toEqual({ grouping: "priority", open: ["P0"] });
    expect(readGroups(null, "a1a1")).toEqual({ grouping: "priority", open: ["P0"] });
  });

  it("Scenario: a written grouping and its open groups read back for that board only", () => {
    const storage = fakeStorage();
    writeGroups(storage, "a1a1", { grouping: "cluster", open: ["web"] });
    expect(readGroups(storage, "a1a1")).toEqual({ grouping: "cluster", open: ["web"] });
    expect(readGroups(storage, "b2b2")).toEqual({ grouping: "priority", open: ["P0"] });
    expect(storage.getItem(`${GROUPS_KEY}.a1a1`)).not.toBeNull();
  });

  it("Scenario: junk in the storage reads as the default", () => {
    const storage = fakeStorage();
    storage.setItem(`${GROUPS_KEY}.a1a1`, "{not json");
    expect(readGroups(storage, "a1a1")).toEqual({ grouping: "priority", open: ["P0"] });
    storage.setItem(`${GROUPS_KEY}.a1a1`, JSON.stringify({ grouping: "size", open: [3] }));
    expect(readGroups(storage, "a1a1")).toEqual({ grouping: "priority", open: ["P0"] });
  });

  it("Scenario: a storage that throws reads as the default and takes a write quietly", () => {
    expect(readGroups(throwingStorage, "a1a1")).toEqual({ grouping: "priority", open: ["P0"] });
    expect(() => writeGroups(throwingStorage, "a1a1", { grouping: "cluster", open: [] })).not.toThrow();
  });
});

describe("Feature: groupCards", () => {
  const cards = [
    makeCard({ id: "A", priority: "P2", cluster: "web" }),
    makeCard({ id: "B", priority: "P0", cluster: "" }),
    makeCard({ id: "C", priority: "P2", cluster: "hub" }),
    makeCard({ id: "D", priority: "P1", cluster: "web" }),
  ];
  const shape = (grouping: "priority" | "cluster") =>
    groupCards(cards, grouping).map((group) => [group.key, group.label, group.cards.map((card) => card.id)]);

  it("Scenario: by priority, the groups run P0 first and keep the cards' order", () => {
    expect(shape("priority")).toEqual([
      ["P0", "P0", ["B"]],
      ["P1", "P1", ["D"]],
      ["P2", "P2", ["A", "C"]],
    ]);
  });

  it("Scenario: by cluster, a card with no cluster groups under No cluster", () => {
    expect(shape("cluster")).toEqual([
      ["", "No cluster", ["B"]],
      ["hub", "hub", ["C"]],
      ["web", "web", ["A", "D"]],
    ]);
  });

  it("Scenario: no cards give no groups", () => {
    expect(groupCards([], "priority")).toEqual([]);
  });
});
