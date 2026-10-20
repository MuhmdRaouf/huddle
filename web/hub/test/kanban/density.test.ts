// density.test.ts — the density preference: compact by default, remembered through
// localStorage, and a storage that throws never breaks the board.
import { describe, expect, it } from "vite-plus/test";
import { DENSITY_KEY, readDensity, writeDensity } from "../../src/kanban/store.ts";

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

describe("Feature: the density preference", () => {
  it("Scenario: compact is the default when nothing was stored", () => {
    // Given a fresh storage
    const storage = fakeStorage();
    // When the density is read
    // Then it is compact
    expect(readDensity(storage)).toBe("compact");
    expect(readDensity(null)).toBe("compact");
  });

  it("Scenario: comfortable survives a reload through localStorage", () => {
    // Given a storage the toggle wrote comfortable into
    const storage = fakeStorage();
    writeDensity(storage, "comfortable");
    expect(storage.getItem(DENSITY_KEY)).toBe("comfortable");
    // When a fresh read happens, the way a reload does
    // Then the comfortable density comes back
    expect(readDensity(storage)).toBe("comfortable");
  });

  it("Scenario: a stored value that is not a density reads as compact", () => {
    // Given a storage holding something else under the density key
    const storage = fakeStorage();
    storage.setItem(DENSITY_KEY, "cozy");
    // When the density is read
    // Then it falls back to compact
    expect(readDensity(storage)).toBe("compact");
  });

  it("Scenario: a throwing storage falls back to compact and never throws", () => {
    // Given a storage that throws on every read and write
    // When the density is read and written
    // Then neither throws and the read is compact
    expect(() => writeDensity(throwingStorage, "comfortable")).not.toThrow();
    expect(readDensity(throwingStorage)).toBe("compact");
  });
});
