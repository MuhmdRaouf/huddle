// transcript.test.ts — the fold of a session's transcript into the blocks its screen paints:
// 13 tool calls between a reply and a prompt fold into one run, counts name every tool, the last
// target names the last call, a live run says it runs, and every block keys by its conversation
// item id so a view never keys by index.
import { describe, expect, it } from "vite-plus/test";
import { fold, type FoldCall, type FoldRun } from "../../src/session/transcript.ts";
import type { ConvoItem } from "../../src/session/types.ts";

/** One prompt line's item. */
const prompt = (id: string, text: string, at = "2026-10-31T14:02:00Z"): ConvoItem => ({
  id,
  kind: "user",
  at,
  text,
});

/** One assistant reply's item. */
const reply = (id: string, text: string, at = "2026-10-31T14:03:00Z"): ConvoItem => ({
  id,
  kind: "assistant",
  at,
  text,
});

/** One tool call's item, its input the compact JSON the wire carries. */
const call = (
  id: string,
  toolUseId: string,
  tool: string,
  input: string,
  at = "2026-10-31T14:04:00Z",
): ConvoItem => ({
  id,
  kind: "tool_use",
  at,
  tool,
  tool_use_id: toolUseId,
  input,
});

/** One tool answer's item, naming the call it closed. */
const result = (id: string, toolUseId: string, at = "2026-10-31T14:05:00Z"): ConvoItem => ({
  id,
  kind: "tool_result",
  at,
  tool_use_id: toolUseId,
});

/** One thinking line's item, which the fold drops. */
const thinking = (id: string): ConvoItem => ({
  id,
  kind: "thinking",
  at: "2026-10-31T14:03:30Z",
  text: "Let me check how the job runs today.",
});

/** The one call a run in these tests folds. */
const theCall = (run: FoldRun): FoldCall => {
  const call = run.calls[0];
  if (!call) throw new Error("the run holds no call");
  return call;
};

describe("fold", () => {
  it("folds 13 tool calls between a reply and a prompt into one run", () => {
    const items: ConvoItem[] = [
      reply("l1:1", "I'll read how the job runs today, then point it at pve2."),
      ...Array.from({ length: 13 }, (_, n) => [
        call(`l${n + 2}:2`, `u${n + 2}`, "Bash", `{"command":"grep -n backup /etc/cron.d/job-${n + 1}"}`),
        result(`l${n + 2}:3`, `u${n + 2}`),
      ]).flat(),
      prompt("l15:0", "Now point it at pve2."),
    ];
    expect(fold(items)).toEqual([
      {
        kind: "reply",
        id: "l1:1",
        at: "2026-10-31T14:03:00Z",
        text: "I'll read how the job runs today, then point it at pve2.",
      },
      {
        kind: "run",
        id: "l2:2",
        at: "2026-10-31T14:04:00Z",
        counts: [{ tool: "Bash", count: 13 }],
        last: "grep -n backup /etc/cron.d/job-13",
        running: false,
        calls: Array.from({ length: 13 }, (_, n) => ({
          id: `l${n + 2}:2`,
          tool: "Bash",
          target: `grep -n backup /etc/cron.d/job-${n + 1}`,
          running: false,
          error: false,
        })),
      },
      { kind: "prompt", id: "l15:0", at: "2026-10-31T14:02:00Z", text: "Now point it at pve2." },
    ]);
  });

  it("counts a run's calls by tool in first-appearance order and names the last target", () => {
    const items: ConvoItem[] = [
      reply("l1:1", "I'll read how the job runs today."),
      call("l2:1", "u1", "Read", '{"file_path":"/etc/cron.d/backup-nightly"}'),
      result("l2:2", "u1"),
      call("l3:1", "u2", "Read", '{"file_path":"/etc/cron.d/backup-nightly.old"}'),
      result("l3:2", "u2"),
      call("l4:1", "u3", "Read", '{"file_path":"/mnt/tank/backups/config.yaml"}'),
      result("l4:2", "u3"),
      call("l5:1", "u4", "Read", '{"file_path":"/mnt/tank/backups/exclude.txt"}'),
      result("l5:2", "u4"),
      call("l6:1", "u5", "Bash", '{"command":"cat /etc/cron.d/backup-nightly"}'),
      result("l6:2", "u5"),
      reply("l7:1", "The job runs at 02:00 and pushes to nas1."),
    ];
    const run = fold(items)[1];
    if (!run || run.kind !== "run") throw new Error("the run is not the second block");
    expect(run.counts).toEqual([
      { tool: "Read", count: 4 },
      { tool: "Bash", count: 1 },
    ]);
    expect(run.last).toBe("cat /etc/cron.d/backup-nightly");
    expect(run.running).toBe(false);
  });

  it("says a run runs while its last call awaits its answer", () => {
    const items: ConvoItem[] = [
      call("l1:1", "u1", "Bash", '{"command":"rsync -a /mnt/tank/backups/ pve2:/backup/nightly/"}'),
      result("l1:2", "u1"),
      call("l2:1", "u2", "Bash", '{"command":"rsync -a --delete /mnt/tank/backups/ pve2:/backup/nightly/"}'),
    ];
    const run = fold(items)[0];
    if (!run || run.kind !== "run") throw new Error("the run is not the first block");
    expect(run.running).toBe(true);
    expect(run.calls.map((c) => c.running)).toEqual([false, true]);
  });

  it("keys every block and every call by its conversation item id", () => {
    const items: ConvoItem[] = [
      prompt("l1:0", "Move the nightly backup to pve2."),
      call("l2:1", "u1", "Read", '{"file_path":"/etc/cron.d/backup-nightly"}'),
      result("l2:2", "u1"),
      call("l3:1", "u2", "Bash", '{"command":"cat /etc/cron.d/backup-nightly"}'),
    ];
    const blocks = fold(items);
    expect(blocks.map((b) => b.id)).toEqual(["l1:0", "l2:1"]);
    const run = blocks[1];
    if (!run || run.kind !== "run") throw new Error("the run is not the second block");
    expect(run.calls.map((c) => c.id)).toEqual(["l2:1", "l3:1"]);
  });

  it("keeps one run across a thinking line between calls", () => {
    const items: ConvoItem[] = [
      call("l1:1", "u1", "Read", '{"file_path":"/etc/cron.d/backup-nightly"}'),
      result("l1:2", "u1"),
      thinking("l2:0"),
      call("l3:1", "u2", "Bash", '{"command":"cat /etc/cron.d/backup-nightly"}'),
      result("l3:2", "u2"),
    ];
    expect(fold(items)).toHaveLength(1);
  });

  it("paints a prompt and a reply with their text and time", () => {
    const blocks = fold([
      prompt("l1:0", "Move the nightly backup to pve2.", "2026-10-31T14:02:00Z"),
      reply("l2:1", "The job runs at 02:00 and pushes to nas1.", "2026-10-31T14:06:00Z"),
    ]);
    expect(blocks).toEqual([
      { kind: "prompt", id: "l1:0", at: "2026-10-31T14:02:00Z", text: "Move the nightly backup to pve2." },
      {
        kind: "reply",
        id: "l2:1",
        at: "2026-10-31T14:06:00Z",
        text: "The job runs at 02:00 and pushes to nas1.",
      },
    ]);
  });

  it("drops a textless line and a thinking line", () => {
    const items: ConvoItem[] = [
      { id: "l1:0", kind: "user", at: "2026-10-31T14:01:00Z" },
      { id: "l2:0", kind: "image", at: "2026-10-31T14:01:05Z", media_type: "image/png", bytes: 4 },
      thinking("l3:0"),
      prompt("l4:0", "Move the nightly backup to pve2."),
    ];
    expect(fold(items)).toEqual([
      { kind: "prompt", id: "l4:0", at: "2026-10-31T14:02:00Z", text: "Move the nightly backup to pve2." },
    ]);
  });

  it("drops a result whose call a page boundary left behind", () => {
    const items: ConvoItem[] = [result("l1:2", "u0"), prompt("l2:0", "Move the nightly backup to pve2.")];
    expect(fold(items)).toEqual([
      { kind: "prompt", id: "l2:0", at: "2026-10-31T14:02:00Z", text: "Move the nightly backup to pve2." },
    ]);
  });

  it("marks a call failed when its answer says so", () => {
    const items: ConvoItem[] = [
      call("l1:1", "u1", "Bash", '{"command":"rsync -a /mnt/tank/ pve2:/backup/"}'),
      { id: "l1:2", kind: "tool_result", at: "2026-10-31T14:05:00Z", tool_use_id: "u1", is_error: true },
    ];
    const run = fold(items)[0];
    if (!run || run.kind !== "run") throw new Error("the run is not the first block");
    expect(theCall(run).error).toBe(true);
    expect(run.running).toBe(false);
  });

  it("folds an empty transcript to nothing", () => {
    expect(fold([])).toEqual([]);
  });

  it("names no target for a call whose input names neither a command nor a file", () => {
    const items: ConvoItem[] = [call("l1:1", "u1", "Glob", '{"pattern":"**/*.go"}'), result("l1:2", "u1")];
    const run = fold(items)[0];
    if (!run || run.kind !== "run") throw new Error("the run is not the first block");
    expect(run.last).toBe("");
    expect(run.counts).toEqual([{ tool: "Glob", count: 1 }]);
  });

  it("names no target for a call whose input was cut", () => {
    const items: ConvoItem[] = [
      call("l1:1", "u1", "Bash", '{"command":"rsync -a /mnt/tank/backups/ pve2:/backup/nig'),
      result("l1:2", "u1"),
    ];
    const run = fold(items)[0];
    if (!run || run.kind !== "run") throw new Error("the run is not the first block");
    expect(run.last).toBe("");
    expect(theCall(run).running).toBe(false);
  });

  it("folds a call whose line omitted its name, id and input", () => {
    const items: ConvoItem[] = [
      { id: "l1:1", kind: "tool_use", at: "2026-10-31T14:04:00Z" },
      { id: "l2:0", kind: "tool_result", at: "2026-10-31T14:05:00Z" },
      prompt("l3:0", "Move the nightly backup to pve2."),
    ];
    const blocks = fold(items);
    expect(blocks).toHaveLength(2);
    const run = blocks[0];
    if (!run || run.kind !== "run") throw new Error("the run is not the first block");
    expect(run.calls).toEqual([{ id: "l1:1", tool: "", target: "", running: false, error: false }]);
    expect(run.counts).toEqual([{ tool: "", count: 1 }]);
    expect(run.running).toBe(false);
  });
});
