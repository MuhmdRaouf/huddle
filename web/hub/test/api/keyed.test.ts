// keyed.test.ts — the keyed store the stream's frames land in: an upsert replaces a row by id, a
// remove drops it, a snapshot replaces every row, a subscriber hears each applied message once,
// and a component reading through useKeyed repaints only when the value it selected changes.
import { act, renderHook } from "@testing-library/preact";
import { describe, expect, it } from "vite-plus/test";
import { createKeyed, useKeyed, type KeyedMessage } from "../../src/api/keyed.ts";

interface Row {
  name: string;
  count: number;
}

let seq = 0;

/** An upsert frame for a row, as the stream spells it. */
function upsert(id: string, data: Row): KeyedMessage<Row> {
  return { topic: "rows", seq: ++seq, op: "upsert", id, data };
}

function remove(id: string): KeyedMessage<Row> {
  return { topic: "rows", seq: ++seq, op: "remove", id };
}

function snapshot(rows: { id: string; data: Row }[]): KeyedMessage<Row> {
  return { topic: "rows", seq: ++seq, op: "snapshot", id: "", data: { rows } };
}

describe("createKeyed", () => {
  it("an upsert replaces by id", () => {
    const store = createKeyed<Row>();
    store.apply(upsert("a", { name: "first", count: 1 }));
    store.apply(upsert("b", { name: "other", count: 5 }));
    store.apply(upsert("a", { name: "second", count: 2 }));

    expect(store.get("a")).toEqual({ name: "second", count: 2 });
    expect(store.values()).toEqual([
      { name: "second", count: 2 },
      { name: "other", count: 5 },
    ]);
  });

  it("a remove drops it", () => {
    const store = createKeyed<Row>();
    store.apply(upsert("a", { name: "first", count: 1 }));
    store.apply(upsert("b", { name: "other", count: 5 }));
    store.apply(remove("a"));

    expect(store.get("a")).toBeUndefined();
    expect(store.values()).toEqual([{ name: "other", count: 5 }]);
  });

  it("a snapshot replaces everything", () => {
    const store = createKeyed<Row>();
    store.apply(upsert("a", { name: "first", count: 1 }));
    store.apply(upsert("b", { name: "other", count: 5 }));
    store.apply(
      snapshot([
        { id: "b", data: { name: "other", count: 6 } },
        { id: "c", data: { name: "third", count: 7 } },
      ]),
    );

    expect(store.get("a")).toBeUndefined();
    expect(store.values()).toEqual([
      { name: "other", count: 6 },
      { name: "third", count: 7 },
    ]);
  });

  it("a subscriber is called once per applied message", () => {
    const store = createKeyed<Row>();
    let calls = 0;
    const stop = store.subscribe(() => {
      calls++;
    });
    const before = store.version;

    store.apply(upsert("a", { name: "first", count: 1 }));
    store.apply(snapshot([{ id: "a", data: { name: "first", count: 2 } }]));
    store.apply(remove("a"));

    expect(calls).toBe(3);
    expect(store.version).toBe(before + 3);

    stop();
    store.apply(upsert("a", { name: "first", count: 3 }));
    expect(calls).toBe(3);
  });

  it("a touch and a remove of an absent id change nothing", () => {
    const store = createKeyed<Row>();
    store.apply(upsert("a", { name: "first", count: 1 }));
    let calls = 0;
    store.subscribe(() => {
      calls++;
    });
    const before = store.version;

    store.apply({ topic: "rows", seq: ++seq, op: "touch", id: "usage" });
    store.apply(remove("missing"));

    expect(calls).toBe(0);
    expect(store.version).toBe(before);
    expect(store.values()).toEqual([{ name: "first", count: 1 }]);
  });

  it("values keeps one array between changes", () => {
    const store = createKeyed<Row>();
    store.apply(upsert("a", { name: "first", count: 1 }));
    const once = store.values();

    expect(store.values()).toBe(once);
    store.apply(upsert("a", { name: "first", count: 2 }));
    expect(store.values()).not.toBe(once);
  });
});

describe("useKeyed", () => {
  it("an unrelated upsert does not re-render a selector that did not change", () => {
    const store = createKeyed<Row>();
    store.apply(upsert("a", { name: "first", count: 1 }));
    let renders = 0;
    const { result } = renderHook(() => {
      renders++;
      return useKeyed(store, (s) => s.get("a")?.count);
    });
    expect(result.current).toBe(1);
    const after = renders;

    void act(() => store.apply(upsert("b", { name: "other", count: 9 })));
    expect(renders).toBe(after);
    expect(result.current).toBe(1);

    void act(() => store.apply(upsert("a", { name: "first", count: 2 })));
    expect(renders).toBe(after + 1);
    expect(result.current).toBe(2);
  });

  it("reads a store that changed between the first render and the subscription", () => {
    const store = createKeyed<Row>();
    const { result } = renderHook(() => useKeyed(store, (s) => s.values().length));
    expect(result.current).toBe(0);

    void act(() => store.apply(upsert("a", { name: "first", count: 1 })));
    expect(result.current).toBe(1);
  });
});
