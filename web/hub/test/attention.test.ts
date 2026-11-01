// attention.test.ts — the one attention rule: attentionOf decides which group a session reads
// as — Needs you, Working, Idle, Listening, Stuck, Ended — from the row and the needs-you items
// alone, never from a time. Ended wins over an open ask, an open ask wins over Listening, and
// the armed waiter wins over the server state.
import { describe, expect, it } from "vite-plus/test";
import { attentionOf } from "../src/attention.ts";
import type { NeedItem, RegistrySession } from "../src/registry.ts";

/** One registry row, with the fields the rule reads; the test overrides what it looks at. */
const row = (over: Partial<RegistrySession> & { session_id: string }): RegistrySession => ({
  state: "working",
  last_event: "UserPromptSubmit",
  last_event_at: "2026-10-16T11:59:30Z",
  ...over,
});

/** One needs-you item, as GET /api/needs answers it. */
const need = (over: Partial<NeedItem> & { id: number }): NeedItem => ({
  session: "s-1",
  kind: "permission",
  summary: "Bash: bun test",
  created_at: "2026-10-16T11:59:00Z",
  state: "open",
  ...over,
});

describe("the attention rule", () => {
  it("an ended session's ask is in Ended", () => {
    const needs = [need({ id: 1, session: "s-done", kind: "turn", summary: "Goal: fix the login" })];
    expect(attentionOf(row({ session_id: "s-done", state: "ended" }), needs)).toBe("ended");
    // a decided item does not hold the session either
    expect(
      attentionOf(row({ session_id: "s-done", state: "ended" }), [
        need({ id: 1, session: "s-done", kind: "turn", state: "answered" }),
      ]),
    ).toBe("ended");
  });

  it("a live idle session with a channel and an armed waiter is Listening", () => {
    // idle for three days and still itself: the rule reads no time
    const waiting = {
      session_id: "s-wait",
      state: "idle",
      channel: "homelab",
      last_event: "Stop",
      last_event_at: "2026-10-13T11:59:30Z",
    };
    const armed = row({ ...waiting, listening: true });
    expect(attentionOf(armed, null)).toBe("listening");
    // without the armed waiter the same row is Idle
    expect(attentionOf(row(waiting), null)).toBe("idle");
    // an ask pulls a listening session into Needs you
    expect(
      attentionOf(armed, [need({ id: 2, session: "s-wait", kind: "question", summary: "Keep it?" })]),
    ).toBe("needs-you");
    // a message wakes it: waiter gone, the server's working state reads
    expect(attentionOf(row({ ...waiting, state: "working" }), null)).toBe("working");
  });

  it("the server state carries the rest, an unknown one as idle", () => {
    const needs = [need({ id: 3, session: "s-other" })];
    // the server's own needs-you stands even when the needs list failed to load
    expect(attentionOf(row({ session_id: "s-n", state: "needs-you" }), null)).toBe("needs-you");
    expect(attentionOf(row({ session_id: "s-w", state: "working" }), needs)).toBe("working");
    expect(attentionOf(row({ session_id: "s-s", state: "stuck" }), needs)).toBe("stuck");
    expect(attentionOf(row({ session_id: "s-u", state: "confused" }), needs)).toBe("idle");
  });
});
