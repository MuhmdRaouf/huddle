// health.test.ts — the /health answer decoded at the edge (a malformed field never throws, it
// falls back), the failing parts in the answer's order, the reader's name for each component,
// and the tiny store the chrome reads: healthy until the first answer lands, then every reader
// of useHealth repaints on setHealth.
import { act, renderHook } from "@testing-library/preact";
import { describe, expect, it } from "vite-plus/test";
import { decodeHealth, failingParts, partLabel, setHealth, useHealth } from "../src/health.ts";

/** The answer /health gives while one store fails, as the server spells it. */
const FAILING = {
  ok: true,
  version: "0.4.0",
  healthy: false,
  components: [
    {
      name: "history.db",
      kind: "store",
      state: "failing",
      lastOkAt: 0,
      lastErrAt: 1_793_229_000_000,
      lastError: "disk I/O error",
      errors: 3,
    },
  ],
};

describe("decodeHealth", () => {
  it("reads the failing answer field by field", () => {
    expect(decodeHealth(FAILING)).toEqual({
      healthy: false,
      parts: [
        {
          name: "history.db",
          kind: "store",
          state: "failing",
          lastOkAt: 0,
          lastErrAt: 1_793_229_000_000,
          lastError: "disk I/O error",
          errors: 3,
        },
      ],
    });
  });

  it("falls back to an all-clear view with no parts", () => {
    expect(decodeHealth({})).toEqual({ healthy: true, parts: [] });
    expect(decodeHealth(null)).toEqual({ healthy: true, parts: [] });
    expect(decodeHealth({ components: "no" })).toEqual({ healthy: true, parts: [] });
  });

  it("reads a state the table does not know as unknown", () => {
    const view = decodeHealth({ components: [{ name: "hub.db", kind: "store", state: "weird" }] });
    expect(view.parts[0]?.state).toBe("unknown");
    expect(view.parts[0]?.kind).toBe("store");
  });

  it("reads a malformed field as its zero value, never throwing", () => {
    const view = decodeHealth({
      components: [{ name: "ledger.db", kind: "loop", state: "stale", lastOkAt: "yesterday", errors: "2" }],
    });
    expect(view.parts[0]).toEqual({
      name: "ledger.db",
      kind: "loop",
      state: "stale",
      lastOkAt: 0,
      lastErrAt: 0,
      lastError: "",
      errors: 0,
    });
  });
});

describe("failingParts", () => {
  it("keeps the failing and stale parts in the answer's order", () => {
    const view = decodeHealth({
      components: [
        { name: "hub.db", kind: "store", state: "ok" },
        { name: "history.db", kind: "store", state: "failing" },
        { name: "radar.history-writer", kind: "loop", state: "stale" },
        { name: "ledger.db", kind: "store", state: "unknown" },
      ],
    });
    expect(failingParts(view).map((p) => p.name)).toEqual(["history.db", "radar.history-writer"]);
  });

  it("answers nothing while every part is well", () => {
    expect(failingParts(decodeHealth({}))).toEqual([]);
  });
});

describe("partLabel", () => {
  it("names each component the way its reader does", () => {
    expect(partLabel("hub.db")).toBe("Hub registry");
    expect(partLabel("knowledge.db")).toBe("Shared knowledge");
    expect(partLabel("router.db")).toBe("Request bodies");
    expect(partLabel("history.db")).toBe("Radar history");
    expect(partLabel("ledger.db")).toBe("Radar ledger");
    expect(partLabel("radar.history-prune")).toBe("History cleanup");
    expect(partLabel("radar.ledger-prune")).toBe("Ledger cleanup");
    expect(partLabel("radar.history-writer")).toBe("History writer");
  });

  it("names a channel's and a board's parts by what they carry", () => {
    expect(partLabel("channel:alpha")).toBe("Channel alpha");
    expect(partLabel("kanban:huddle")).toBe("Board huddle");
  });

  it("gives an unknown name back as it is", () => {
    expect(partLabel("mystery.db")).toBe("mystery.db");
  });
});

describe("the health store", () => {
  it("starts all-clear, so no dot shows before the first answer", () => {
    const { result } = renderHook(() => useHealth());
    expect(result.current).toEqual({ healthy: true, parts: [] });
  });

  it("repaints every reader on setHealth", () => {
    const { result } = renderHook(() => useHealth());
    expect(result.current).toEqual({ healthy: true, parts: [] });
    act(() => {
      setHealth(decodeHealth(FAILING));
    });
    expect(result.current).toEqual(decodeHealth(FAILING));
    act(() => {
      setHealth({ healthy: true, parts: [] });
    });
    expect(result.current).toEqual({ healthy: true, parts: [] });
  });
});
