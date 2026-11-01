// conversation.test.ts — the page's held conversation: a page of items joins what the page holds
// without repeating an item and keeps the newest cursor, and the items past the one the reader
// had at the first paint are the new ones.
import { describe, expect, it } from "vite-plus/test";
import { appendPage, emptyConversation, newSince } from "../../src/session/conversation.ts";
import type { ConvoItem } from "../../src/session/types.ts";

/** One prompt line's item. */
const item = (id: string): ConvoItem => ({ id, kind: "user", at: "2026-10-31T14:02:00Z", text: id });

describe("appendPage", () => {
  it("drops an item it already holds and keeps the newest cursor", () => {
    const first = appendPage(emptyConversation, { items: [item("a"), item("b")], next: 100 });
    expect(first.items.map((i) => i.id)).toEqual(["a", "b"]);
    expect(first.next).toBe(100);

    const second = appendPage(first, { items: [item("b"), item("c")], next: 180 });
    expect(second.items.map((i) => i.id)).toEqual(["a", "b", "c"]);
    expect(second.next).toBe(180);

    // an older answer arriving late never moves the cursor back
    const late = appendPage(second, { items: [item("a")], next: 100 });
    expect(late.items.map((i) => i.id)).toEqual(["a", "b", "c"]);
    expect(late.next).toBe(180);
  });

  it("leaves the state it was given as it was", () => {
    const first = appendPage(emptyConversation, { items: [item("a")], next: 10 });
    appendPage(first, { items: [item("b")], next: 20 });
    expect(first.items.map((i) => i.id)).toEqual(["a"]);
    expect(emptyConversation.items).toEqual([]);
  });
});

describe("newSince", () => {
  const held = appendPage(emptyConversation, { items: [item("a"), item("b"), item("c")], next: 30 });

  it("answers the items after the one seen last", () => {
    expect(newSince(held, "a").map((i) => i.id)).toEqual(["b", "c"]);
    expect(newSince(held, "c")).toEqual([]);
  });

  it("answers nothing when nothing was seen or the seen item is not held", () => {
    expect(newSince(held, null)).toEqual([]);
    expect(newSince(held, "zz")).toEqual([]);
  });
});
