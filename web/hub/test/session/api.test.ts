// session/api.test.ts — the typed client for the hub's session screens: every method asks the
// exact route with its id parts encoded, and every answer is decoded at the edge into types that
// mirror the Go answers key for key — well-formed bodies come back whole, unknown fields are
// dropped, optional fields stay absent when the wire leaves them out, and a body that fails its
// decode throws one Error naming the route and the first bad field.
import { describe, expect, it } from "vite-plus/test";
import type { Api, ApiOptions } from "../../src/api.ts";
import { sessionApi } from "../../src/session/api.ts";
import type { ConvoItem, ConvoPage } from "../../src/session/types.ts";

/** One call the fake recorded. */
type Recorded = { path: string; options: ApiOptions | undefined };

/** A fake Api: it records the path of every call and answers it with the one fixture. */
function fakeApi(answer: unknown): Api & { calls: Recorded[] } {
  const calls: Recorded[] = [];
  return {
    calls,
    api: (path, options) => {
      calls.push({ path, options });
      return Promise.resolve(answer);
    },
    op: () => Promise.resolve({}),
    channelPath: (ch, p) => `/api/c/${encodeURIComponent(ch)}${p}`,
    channelHref: (ch, p) => `#/channels/${ch}${p}`,
  };
}

/** The path the one call the client made asked. The fake's empty body fails its decode; the test
 *  reads the path the call asked, not its answer. */
async function askedPath(run: (s: ReturnType<typeof sessionApi>) => Promise<unknown>): Promise<string> {
  const fake = fakeApi({});
  await run(sessionApi(fake)).catch(() => undefined);
  const [call] = fake.calls;
  if (!call) throw new Error("the client made no call");
  return call.path;
}

describe("sessionApi paths", () => {
  it("asks each route with its session, agent and shell ids encoded", async () => {
    expect(await askedPath((s) => s.agents("ses 1/x"))).toBe("/api/sessions/ses%201%2Fx/agents");
    expect(await askedPath((s) => s.conversation("s", "ag ent/2"))).toBe(
      "/api/sessions/s/agents/ag%20ent%2F2/conversation",
    );
    expect(await askedPath((s) => s.conversation("s", "a", 12))).toBe(
      "/api/sessions/s/agents/a/conversation?after=12",
    );
    expect(await askedPath((s) => s.shells("s", "a b"))).toBe("/api/sessions/s/agents/a%20b/shells");
    expect(await askedPath((s) => s.shellOutput("s", "a", "sh/1"))).toBe(
      "/api/sessions/s/agents/a/shells/sh%2F1/output",
    );
    expect(await askedPath((s) => s.shellOutput("s", "a", "sh", 3))).toBe(
      "/api/sessions/s/agents/a/shells/sh/output?after=3",
    );
    expect(await askedPath((s) => s.tasks("s"))).toBe("/api/sessions/s/tasks");
    expect(await askedPath((s) => s.turn("s"))).toBe("/api/sessions/s/turn");
    expect(await askedPath((s) => s.live("s"))).toBe("/api/sessions/s/live");
  });

  it("issues a plain GET with no options", async () => {
    const fake = fakeApi({ agents: [] });
    await sessionApi(fake).agents("s");
    const [call] = fake.calls;
    if (!call) throw new Error("the client made no call");
    expect(call.options).toBeUndefined();
  });
});

describe("sessionApi decodes well-formed answers whole", () => {
  it("decodes the agents answer", async () => {
    const answer = {
      agents: [
        {
          id: "main",
          type: "main",
          depth: 0,
          running: true,
          description: "Lead",
          model: "claude-opus-4-6",
          side_chat: true,
          started_at: "2026-10-26T10:00:05.000Z",
        },
        { id: "f4", type: "fork", depth: 1, running: false },
      ],
    };
    const agents = await sessionApi(fakeApi(answer)).agents("s");
    expect(agents).toEqual(answer.agents);
  });

  it("decodes the conversation page: every kind of item, tool facts, hunks and answers", async () => {
    const answer = {
      items: [
        { id: "i1", kind: "user", at: "2026-10-26T10:00:01.000Z", text: "scan the files" },
        { id: "i2", kind: "assistant", at: "2026-10-26T10:00:02.000Z", text: "on it", parent: "i1" },
        { id: "i3", kind: "thinking", at: "2026-10-26T10:00:03.000Z", duration_ms: 1200 },
        {
          id: "i4",
          kind: "tool_use",
          at: "2026-10-26T10:00:04.000Z",
          tool: "Bash",
          tool_use_id: "tu1",
          input: '{"command":"ls"}',
        },
        {
          id: "i5",
          kind: "tool_result",
          at: "2026-10-26T10:00:05.000Z",
          tool_use_id: "tu1",
          is_error: true,
          cut: true,
          result: {
            exit_code: 1,
            stderr: "boom",
            interrupted: true,
            background_id: "bg1",
            file_path: "a/b.go",
            cut: true,
            hunks: [{ old_start: 1, old_lines: 2, new_start: 3, new_lines: 4, lines: ["-old", "+new"] }],
            answers: [{ header: "Proceed", question: "run it?", answer: "yes" }],
          },
        },
        { id: "i6", kind: "file", at: "2026-10-26T10:00:06.000Z", path: "a/b.go", lines: 12 },
        { id: "i7", kind: "image", at: "2026-10-26T10:00:07.000Z", media_type: "image/png", bytes: 99 },
      ],
      next: 4096,
    };
    const page: ConvoPage = await sessionApi(fakeApi(answer)).conversation("s", "a");
    expect(page).toEqual(answer);
    const kinds: ConvoItem["kind"][] = page.items.map((item) => item.kind);
    expect(kinds).toEqual(["user", "assistant", "thinking", "tool_use", "tool_result", "file", "image"]);
  });

  it("keeps the fields the conversation answer leaves out absent", async () => {
    const page = await sessionApi(
      fakeApi({ items: [{ id: "i1", kind: "assistant", at: "t" }], next: 0 }),
    ).conversation("s", "a");
    const [item] = page.items;
    if (!item) throw new Error("the page lost its one item");
    const optionalKeys = [
      "text",
      "duration_ms",
      "tool",
      "tool_use_id",
      "input",
      "is_error",
      "result",
      "cut",
      "parent",
      "path",
      "lines",
      "media_type",
      "bytes",
    ];
    for (const key of optionalKeys) expect(key in item).toBe(false);
  });

  it("decodes the shells answer", async () => {
    const answer = {
      shells: [
        {
          id: "b4io7vyqm",
          command: "go test ./...",
          description: "the suite",
          state: "ended",
          status: "completed",
          exit_code: 1,
          started_at: "2026-10-26T10:00:00.000Z",
          ended_at: "2026-10-26T10:04:00.000Z",
        },
        {
          id: "k7w2n9x4d1",
          command: "tail -f x.log",
          state: "running",
          started_at: "2026-10-26T10:05:00.000Z",
        },
      ],
    };
    const shells = await sessionApi(fakeApi(answer)).shells("s", "a");
    expect(shells).toEqual(answer.shells);
  });

  it("decodes the shell output page", async () => {
    const answer = { text: "line one\nline two\n", next: 28, size: 57 };
    const page = await sessionApi(fakeApi(answer)).shellOutput("s", "a", "b4io7vyqm");
    expect(page).toEqual(answer);
  });

  it("decodes the tasks answer: todos, channel tasks and kanban cards", async () => {
    const answer = {
      todos: [
        { id: "t1", content: "write the client", status: "in_progress", active_form: "Writing the client" },
        { id: "t2", content: "wire the screen", status: "pending" },
      ],
      tasks: [{ id: "j1", title: "Sessions screens", status: "open", channel: "eng" }],
      cards: [{ id: "c1", title: "Typed client", column: "doing", cluster: "swr", source: "held" }],
      board: "e2e-demo",
    };
    const tasks = await sessionApi(fakeApi(answer)).tasks("s");
    expect(tasks).toEqual(answer);
  });

  it("keeps the tasks answer's board and card facts absent when the wire leaves them out", async () => {
    const tasks = await sessionApi(
      fakeApi({ todos: [], tasks: [], cards: [{ id: "c1", title: "Typed client", column: "doing" }] }),
    ).tasks("s");
    expect("board" in tasks).toBe(false);
    const [card] = tasks.cards;
    if (!card) throw new Error("the answer lost its one card");
    for (const key of ["cluster", "source"]) expect(key in card).toBe(false);
  });

  it("decodes the turn answer with and without output_tokens", async () => {
    const full = {
      started_at: "2026-10-26T10:00:00.000Z",
      ended: true,
      duration_ms: 4200,
      permission_mode: "default",
      output_tokens: 321,
    };
    expect(await sessionApi(fakeApi(full)).turn("s")).toEqual(full);

    const bare = await sessionApi(fakeApi({ ended: false })).turn("s");
    expect(bare).toEqual({ ended: false });
    for (const key of ["started_at", "duration_ms", "permission_mode", "output_tokens"]) {
      expect(key in bare).toBe(false);
    }
  });

  it("decodes the live tray: an agent with radar facts, one without, and a running shell", async () => {
    const answer = {
      agents: [
        {
          id: "f1",
          type: "general-purpose",
          description: "Worker A",
          started_at: "2026-10-26T10:00:30.000Z",
          output_tokens: 812,
          last_activity: "2026-10-26T10:06:00.000Z",
          what: "reading api.ts",
        },
        { id: "f2", type: "fork" },
      ],
      shells: [
        {
          agent: "f1",
          id: "b4io7vyqm",
          command: "go vet ./...",
          description: "vet",
          started_at: "2026-10-26T10:01:00.000Z",
        },
        { agent: "main", id: "s9x2k1", command: "tail -f a.log", started_at: "2026-10-26T10:02:00.000Z" },
      ],
    };
    const tray = await sessionApi(fakeApi(answer)).live("s");
    expect(tray).toEqual(answer);
  });

  it("decodes an empty tray as empty arrays", async () => {
    const tray = await sessionApi(fakeApi({ agents: [], shells: [] })).live("s");
    expect(tray).toEqual({ agents: [], shells: [] });
  });

  it("drops fields the answer carries but the type does not know", async () => {
    const fake = fakeApi({
      items: [{ id: "i1", kind: "user", at: "t", text: "x", rogue: true }],
      next: 0,
    });
    const page = await sessionApi(fake).conversation("s", "a");
    expect(page).toEqual({ items: [{ id: "i1", kind: "user", at: "t", text: "x" }], next: 0 });
    for (const item of page.items) expect("rogue" in item).toBe(false);
  });
});

describe("sessionApi throws on answers that fail their decode", () => {
  it("names the route when the agents answer has no agents array", async () => {
    await expect(sessionApi(fakeApi({})).agents("s")).rejects.toThrow(
      "/api/sessions/s/agents: agents must be an array",
    );
  });

  it("names the route when the conversation answer has no items array", async () => {
    await expect(sessionApi(fakeApi({})).conversation("s", "a")).rejects.toThrow(
      "/api/sessions/s/agents/a/conversation: items must be an array",
    );
  });

  it("names the item whose kind is not one of the kinds", async () => {
    const fake = fakeApi({
      items: [
        { id: "i1", kind: "user", at: "t" },
        { id: "i2", kind: 7, at: "t" },
      ],
      next: 0,
    });
    await expect(sessionApi(fake).conversation("s", "a")).rejects.toThrow(
      "/api/sessions/s/agents/a/conversation: items[1].kind must be one of user, assistant, thinking, tool_use, tool_result, file, image",
    );
  });

  it("names the first field of an item whose type is wrong", async () => {
    const fake = fakeApi({ items: [{ id: 5, kind: "user", at: "t" }], next: 0 });
    await expect(sessionApi(fake).conversation("s", "a")).rejects.toThrow(
      "/api/sessions/s/agents/a/conversation: items[0].id must be a string",
    );
  });

  it("names the field of an optional whose type is wrong", async () => {
    const fake = fakeApi({
      items: [{ id: "i1", kind: "thinking", at: "t", duration_ms: "1200" }],
      next: 0,
    });
    await expect(sessionApi(fake).conversation("s", "a")).rejects.toThrow(
      "/api/sessions/s/agents/a/conversation: items[0].duration_ms must be a number",
    );
  });

  it("names the hunk whose lines are not an array", async () => {
    const fake = fakeApi({
      items: [
        {
          id: "i1",
          kind: "tool_result",
          at: "t",
          result: { hunks: [{ old_start: 1, old_lines: 1, new_start: 1, new_lines: 1, lines: "no" }] },
        },
      ],
      next: 0,
    });
    await expect(sessionApi(fake).conversation("s", "a")).rejects.toThrow(
      "/api/sessions/s/agents/a/conversation: items[0].result.hunks[0].lines must be an array",
    );
  });

  it("names the route when the shell output page has no next", async () => {
    await expect(sessionApi(fakeApi({ text: "x" })).shellOutput("s", "a", "sh")).rejects.toThrow(
      "/api/sessions/s/agents/a/shells/sh/output: next must be a number",
    );
  });

  it("names the route when the tasks answer has no tasks array", async () => {
    await expect(sessionApi(fakeApi({ todos: [] })).tasks("s")).rejects.toThrow(
      "/api/sessions/s/tasks: tasks must be an array",
    );
  });

  it("names the route when the turn answer is not an object", async () => {
    await expect(sessionApi(fakeApi(null)).turn("s")).rejects.toThrow(
      "/api/sessions/s/turn: answer must be an object",
    );
  });

  it("names the field when the turn's ended is not a boolean", async () => {
    await expect(sessionApi(fakeApi({ ended: "yes" })).turn("s")).rejects.toThrow(
      "/api/sessions/s/turn: ended must be a boolean",
    );
  });

  it("names the route when the tray has no shells array", async () => {
    await expect(sessionApi(fakeApi({ agents: [] })).live("s")).rejects.toThrow(
      "/api/sessions/s/live: shells must be an array",
    );
  });
});
