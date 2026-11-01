// now-line.test.ts — the one Now line a session's row paints: the ask it waits on naming its
// tool, the work naming the last tool, the finished turn, the channel it listens on, the stuck
// session's silence, and the honest word each row falls back to when a fact is missing.

import { describe, expect, it } from "vite-plus/test";
import { nowLine, type NowLineRow } from "../../src/app/now-line.ts";
import type { NeedItem } from "../../src/registry.ts";
import type { TurnFacts } from "../../src/session/types.ts";
import { NOW } from "../helpers.tsx";

/** One registry row, with the fields the Now line reads; the test overrides what it looks at. */
const row = (over: Partial<NowLineRow> & { session_id: string }): NowLineRow => ({
  state: "working",
  last_event: "PreToolUse",
  last_event_at: "2026-10-08T11:59:30Z",
  ...over,
});

/** One needs-you item, as GET /api/needs answers it. */
const need = (over: Partial<NeedItem> & { id: number }): NeedItem => ({
  session: "s-1",
  kind: "permission",
  summary: "Bash: rsync -a --delete /mnt/tank/backups/ pve2:/backup/nightly/",
  created_at: "2026-10-08T11:57:00Z",
  state: "open",
  tool: "Bash",
  input: '{"command":"rsync -a --delete /mnt/tank/backups/ pve2:/backup/nightly/"}',
  ...over,
});

/** The turn facts GET /api/sessions/<id>/turn answers. */
const turn = (ended: boolean): TurnFacts => ({ ended });

describe("nowLine", () => {
  it("waits on a permission by naming the command", () => {
    expect(
      nowLine(
        row({
          session_id: "s-1",
          state: "needs-you",
          tool: "Bash",
          tool_target: "rsync -a --delete /mnt/tank/backups/ pve2:/backup/nightly/",
        }),
        need({ id: 1 }),
        undefined,
        NOW,
      ),
    ).toBe("Waiting for you to allow a command: rsync -a --delete /mnt/tank/backups/ pve2:/backup/nightly/");
  });

  it("waits on a permission whose command is plain text", () => {
    expect(
      nowLine(
        row({ session_id: "s-1", state: "needs-you", tool: "Bash" }),
        need({ id: 2, input: "npm test" }),
        undefined,
        NOW,
      ),
    ).toBe("Waiting for you to allow a command: npm test");
  });

  it("waits on a permission by naming the tool when it runs no command", () => {
    expect(
      nowLine(
        row({ session_id: "s-1", state: "needs-you", tool: "Edit" }),
        need({ id: 3, tool: "Edit", input: '{"file_path":"flake.lock"}', summary: "Edit: flake.lock" }),
        undefined,
        NOW,
      ),
    ).toBe("Waiting for you to allow Edit");
  });

  it("waits for the answer a question asks for", () => {
    expect(
      nowLine(
        row({ session_id: "s-1", state: "needs-you" }),
        need({ id: 4, kind: "question", summary: "How many VMs fit on pve2?" }),
        undefined,
        NOW,
      ),
    ).toBe("Waiting for your answer: How many VMs fit on pve2?");
  });

  it("waits on a blocked task by naming it", () => {
    expect(
      nowLine(
        row({ session_id: "s-1", state: "needs-you" }),
        need({ id: 5, kind: "blocked", summary: "Ship the release notes" }),
        undefined,
        NOW,
      ),
    ).toBe("Waiting on you: Ship the release notes");
  });

  it("reads a finished turn as finished, waiting for you", () => {
    expect(
      nowLine(
        row({ session_id: "s-1", state: "needs-you" }),
        need({ id: 6, kind: "turn", summary: "Turn done" }),
        undefined,
        NOW,
      ),
    ).toBe("Finished, waiting for you");
  });

  it("names the command a working session runs", () => {
    expect(
      nowLine(
        row({ session_id: "s-1", tool: "Bash", tool_target: "bun run e2e" }),
        undefined,
        undefined,
        NOW,
      ),
    ).toBe("Running bun run e2e");
  });

  it("names the file a working session edits", () => {
    expect(
      nowLine(row({ session_id: "s-1", tool: "Edit", tool_target: "flake.lock" }), undefined, undefined, NOW),
    ).toBe("Editing flake.lock");
  });

  it("names the tool and target no verb fits", () => {
    expect(
      nowLine(
        row({ session_id: "s-1", tool: "WebFetch", tool_target: "https://example.com" }),
        undefined,
        undefined,
        NOW,
      ),
    ).toBe("Running WebFetch https://example.com");
  });

  it("says a working session with nothing known works", () => {
    expect(nowLine(row({ session_id: "s-1" }), undefined, undefined, NOW)).toBe("Working");
  });

  it("reads a working row whose turn has ended as finished", () => {
    expect(
      nowLine(
        row({ session_id: "s-1", tool: "Bash", tool_target: "bun run e2e" }),
        undefined,
        turn(true),
        NOW,
      ),
    ).toBe("Finished, waiting for you");
  });

  it("says an idle session finished, waiting for you", () => {
    expect(nowLine(row({ session_id: "s-1", state: "idle" }), undefined, undefined, NOW)).toBe(
      "Finished, waiting for you",
    );
  });

  it("says an idle row whose turn still runs works", () => {
    expect(nowLine(row({ session_id: "s-1", state: "idle" }), undefined, turn(false), NOW)).toBe("Working");
  });

  it("names the channel a listening session waits on", () => {
    expect(
      nowLine(
        row({ session_id: "s-1", state: "idle", channel: "homelab", listening: true }),
        undefined,
        undefined,
        NOW,
      ),
    ).toBe("Listening on #homelab");
  });

  it("says a listening session without a channel listens", () => {
    expect(
      nowLine(row({ session_id: "s-1", state: "idle", listening: true }), undefined, undefined, NOW),
    ).toBe("Listening");
  });

  it("says how long a stuck session has been silent", () => {
    expect(
      nowLine(
        row({ session_id: "s-1", state: "stuck", last_event_at: "2026-10-08T11:46:00Z" }),
        undefined,
        undefined,
        NOW,
      ),
    ).toBe("No signal for 14 min");
  });

  it("says a stuck session with no last report gives no signal", () => {
    expect(
      nowLine(row({ session_id: "s-1", state: "stuck", last_event_at: "" }), undefined, undefined, NOW),
    ).toBe("No signal");
  });

  it("says an ended session is over", () => {
    expect(nowLine(row({ session_id: "s-1", state: "ended" }), undefined, undefined, NOW)).toBe(
      "Session ended",
    );
  });

  it("keeps an ended session ended when an old ask of its is still open", () => {
    expect(nowLine(row({ session_id: "s-1", state: "ended" }), need({ id: 7 }), undefined, NOW)).toBe(
      "Session ended",
    );
  });

  it("waits for you when a session needs you with no ask known", () => {
    expect(nowLine(row({ session_id: "s-1", state: "needs-you" }), undefined, undefined, NOW)).toBe(
      "Waiting for you",
    );
  });
});
