// registry.test.ts — the registry the Now list runs on: the API types' pure reads (groups and
// counts, machines, names, ages, the stream items a session's events become, the needs-you items
// a session's actions decide) and the store that loads them, applies a registry.session row and
// a needs.item row, and follows the all-channels live stream.
import { describe, expect, it, vi } from "vite-plus/test";
import type { EventSourceLike } from "../src/live.ts";
import {
  ageText,
  createRegistry,
  groupSessions,
  machinesLine,
  machinesOf,
  type NeedItem,
  needsYouCount,
  openItemOf,
  openItemsOf,
  permissionOf,
  type RegistryEvent,
  type RegistrySession,
  sessionName,
  shortModel,
  stateWord,
  streamItems,
} from "../src/registry.ts";
import { NOW } from "./helpers.tsx";

/** One registry row, with the fields the list reads; the test overrides what it looks at. */
const row = (over: Partial<RegistrySession> & { session_id: string }): RegistrySession => ({
  state: "working",
  last_event: "UserPromptSubmit",
  last_event_at: "2026-10-16T11:59:30Z",
  ...over,
});

/** One needs-you item, as GET /api/needs answers it and a needs.item event carries it. */
const need = (over: Partial<NeedItem> & { id: number }): NeedItem => ({
  session: "s-need-1",
  kind: "permission",
  summary: "Bash: npm test -- --run e2e",
  created_at: "2026-10-16T11:59:00Z",
  state: "open",
  tool: "Bash",
  input: "npm test -- --run e2e",
  ...over,
});

/** The fixture the list's groups and counts are read from: two that need the owner, three at
 *  work, two idle, one stuck, one ended, over three machines and two projects. */
const fixture = (): RegistrySession[] => [
  row({ session_id: "s-need-1", state: "needs-you", title: "kanban-e2e", machine: "laptop", channel: "reg" }),
  row({ session_id: "s-need-2", state: "needs-you", title: "api-gateway", machine: "build-box" }),
  row({
    session_id: "s-work-1",
    title: "lead",
    machine: "studio",
    model: "claude-opus-4-6",
    task: "Release",
    context_percent: 41,
  }),
  row({
    session_id: "s-work-2",
    title: "hub-home",
    machine: "build-box",
    model: "glm-5.3",
    task: "Import",
    context_percent: 63,
  }),
  row({
    session_id: "s-work-3",
    title: "radar-ui",
    machine: "laptop",
    model: "claude-sonnet-4-5",
    task: "Budget",
  }),
  row({ session_id: "s-idle-1", state: "idle", title: "docs-sweep", machine: "studio", last_event: "Stop" }),
  row({
    session_id: "s-idle-2",
    state: "idle",
    title: "notes",
    machine: "laptop",
    last_event: "SubagentStop",
  }),
  row({ session_id: "s-stuck", state: "stuck", title: "quiet-one", machine: "studio" }),
  row({ session_id: "s-done", state: "ended", title: "finished", machine: "build-box" }),
];

describe("the list's pure reads", () => {
  it("groups the sessions and counts each group", () => {
    const g = groupSessions(fixture());
    expect(g.needsYou.map((s) => s.session_id)).toEqual(["s-need-1", "s-need-2"]);
    expect(g.working.map((s) => s.session_id)).toEqual(["s-work-1", "s-work-2", "s-work-3"]);
    expect(g.idle.map((s) => s.session_id)).toEqual(["s-idle-1", "s-idle-2"]);
    expect(g.stuck.map((s) => s.session_id)).toEqual(["s-stuck"]);
    expect(g.ended.map((s) => s.session_id)).toEqual(["s-done"]);
    expect(needsYouCount(fixture())).toBe(2);
    expect(needsYouCount(null)).toBe(0);
    expect(groupSessions(null)).toEqual({
      needsYou: [],
      working: [],
      idle: [],
      stuck: [],
      ended: [],
    });
  });

  it("lists the machines once, sorted", () => {
    expect(machinesOf(fixture())).toEqual(["build-box", "laptop", "studio"]);
    expect(machinesOf(null)).toEqual([]);
  });

  it("shapes the machines line per layout", () => {
    const names = ["build-box", "laptop", "studio"];
    expect(machinesLine("phone", names)).toBe("3 machines");
    expect(machinesLine("tablet", names)).toBe("build-box · laptop · studio");
    expect(machinesLine("desktop", names)).toBe("build-box · laptop · studio");
    expect(machinesLine("phone", [])).toBe("This machine");
  });

  it("pluralises the phone's count only when there is more than one", () => {
    expect(machinesLine("phone", ["laptop"])).toBe("1 machine");
    expect(machinesLine("phone", ["laptop", "studio"])).toBe("2 machines");
  });

  it("names a session by its title, falling back to its id", () => {
    expect(sessionName(row({ session_id: "abcdefgh-1", title: "kanban-e2e" }))).toBe("kanban-e2e");
    expect(sessionName(row({ session_id: "abcdefgh-1" }))).toBe("abcdefgh");
  });

  it("ages an event the way the rows show it", () => {
    expect(ageText("2026-10-08T11:59:30Z", NOW)).toBe("30 s");
    expect(ageText("2026-10-08T11:57:00Z", NOW)).toBe("3 min");
    expect(ageText("2026-10-08T11:00:00Z", NOW)).toBe("1 h");
    expect(ageText("2026-10-06T12:00:00Z", NOW)).toBe("2 d");
    expect(ageText(undefined, NOW)).toBe("");
    expect(ageText("not a time", NOW)).toBe("");
  });

  it("shortens a model id by its vendor prefix only", () => {
    expect(shortModel("claude-opus-4-6")).toBe("opus-4-6");
    expect(shortModel("Claude-Sonnet-4-5")).toBe("sonnet-4-5");
    expect(shortModel("glm-5.3")).toBe("glm-5.3");
    expect(shortModel("")).toBe("");
  });

  it("gives every state its word", () => {
    expect(stateWord("needs-you")).toBe("needs you");
    expect(stateWord("working")).toBe("working");
    expect(stateWord("idle")).toBe("idle");
    expect(stateWord("stuck")).toBe("stuck");
    expect(stateWord("ended")).toBe("ended");
  });
});

describe("the session's stream items", () => {
  const ev = (
    seq: number,
    over: Partial<RegistryEvent["report"]> & { last_event: string },
  ): RegistryEvent => ({
    seq,
    report: { session_id: "s-1", last_event_at: "2026-10-16T11:00:00Z", ...over },
  });

  it("renders prompts as bubbles, tool calls as one named line, other text as notes", () => {
    const items = streamItems([
      ev(1, { last_event: "SessionStart" }),
      ev(2, { last_event: "UserPromptSubmit", task: "Add the e2e test" }),
      ev(3, { last_event: "PostToolUse", cwd: "/repo", tool: "Bash", tool_target: "git status" }),
      ev(4, { last_event: "Stop", task: "The test exports every ticket" }),
      ev(5, { last_event: "Notification" }),
    ]);
    expect(items).toEqual([
      { kind: "prompt", text: "Add the e2e test", at: "2026-10-16T11:00:00Z" },
      { kind: "tool", tool: "Bash", target: "git status", running: false, at: "2026-10-16T11:00:00Z" },
      { kind: "note", text: "The test exports every ticket", at: "2026-10-16T11:00:00Z" },
    ]);
  });

  it("folds a PreToolUse into its PostToolUse: one row per call, at the call's start", () => {
    const items = streamItems([
      ev(1, {
        last_event: "PreToolUse",
        cwd: "/repo",
        tool: "Bash",
        tool_target: "git status",
        last_event_at: "2026-10-16T11:00:01Z",
      }),
      ev(2, {
        last_event: "PostToolUse",
        cwd: "/repo",
        tool: "Bash",
        tool_target: "git status",
        last_event_at: "2026-10-16T11:00:02Z",
      }),
    ]);
    expect(items).toEqual([
      { kind: "tool", tool: "Bash", target: "git status", running: false, at: "2026-10-16T11:00:01Z" },
    ]);
  });

  it("pairs calls that overlap by tool and target", () => {
    const items = streamItems([
      ev(1, { last_event: "PreToolUse", tool: "Read", tool_target: "/repo/a.ts", cwd: "/repo" }),
      ev(2, { last_event: "PreToolUse", tool: "Read", tool_target: "/repo/b.ts", cwd: "/repo" }),
      ev(3, { last_event: "PostToolUse", tool: "Read", tool_target: "/repo/b.ts", cwd: "/repo" }),
    ]);
    expect(items.map((i) => (i.kind === "tool" ? [i.target, i.running] : null))).toEqual([
      ["a.ts", true],
      ["b.ts", false],
    ]);
  });

  it("marks a PreToolUse without its PostToolUse as running", () => {
    const items = streamItems([ev(1, { last_event: "PreToolUse", tool: "Bash", tool_target: "bun test" })]);
    expect(items).toEqual([
      { kind: "tool", tool: "Bash", target: "bun test", running: true, at: "2026-10-16T11:00:00Z" },
    ]);
  });

  it("shows a file under the session's directory by its path inside it", () => {
    const items = streamItems([
      ev(1, {
        last_event: "PostToolUse",
        cwd: "/Users/raouf/Playground/huddle",
        tool: "Edit",
        tool_target: "/Users/raouf/Playground/huddle/web/hub/src/app/Now.tsx",
      }),
      ev(2, {
        last_event: "PostToolUse",
        cwd: "/Users/raouf/Playground/huddle",
        tool: "Read",
        tool_target: "/etc/hosts",
      }),
    ]);
    expect(items.map((i) => (i.kind === "tool" ? i.target : ""))).toEqual([
      "web/hub/src/app/Now.tsx",
      "/etc/hosts",
    ]);
  });

  it("reads a report from an older hook as a plain Tool row, and never shows the cwd", () => {
    const items = streamItems([
      ev(1, { last_event: "PreToolUse", cwd: "/repo" }),
      ev(2, { last_event: "PostToolUse", cwd: "/repo" }),
    ]);
    expect(items).toEqual([
      { kind: "tool", tool: "Tool", target: "", running: false, at: "2026-10-16T11:00:00Z" },
    ]);
    expect(JSON.stringify(items)).not.toContain("/repo");
  });

  it("shows the prompt's first line, else the task, else the title", () => {
    const items = streamItems([
      ev(1, { last_event: "UserPromptSubmit", prompt_line: "Fix the stream rows", task: "Fix the" }),
      ev(2, { last_event: "UserPromptSubmit", title: "First prompt" }),
    ]);
    expect(items.map((i) => (i.kind === "prompt" ? i.text : ""))).toEqual([
      "Fix the stream rows",
      "First prompt",
    ]);
  });

  it("says the owner sent a prompt when the prompt carries no text", () => {
    const items = streamItems([ev(1, { last_event: "UserPromptSubmit" })]);
    expect(items).toEqual([{ kind: "prompt", text: "You sent a prompt", at: "2026-10-16T11:00:00Z" }]);
  });
});

/** One canned API: the paths it saw, and the answer each path carries. */
function fakeApi(answers: Record<string, unknown> = {}) {
  const calls: string[] = [];
  return {
    calls,
    api: (path: string): Promise<unknown> => {
      calls.push(path);
      const answer = answers[path.split("?")[0] ?? ""];
      if (answer === undefined) return Promise.reject(new Error(`no canned answer for ${path}`));
      if (answer instanceof Error) return Promise.reject(answer);
      return Promise.resolve(answer);
    },
  };
}

/** One SSE stream the test drives: the frames it got, and whether it was closed. */
function fakeSource() {
  const src: EventSourceLike = {
    onopen: null,
    onerror: null,
    onmessage: null,
    close: () => {
      closed = true;
    },
  };
  let closed = false;
  return {
    src,
    get closed(): boolean {
      return closed;
    },
    open(): void {
      src.onopen?.();
    },
    error(): void {
      src.onerror?.();
    },
    send(data: unknown): void {
      src.onmessage?.({ data: JSON.stringify(data) });
    },
  };
}

describe("the registry store", () => {
  it("loads the list and the needs-you items once, and tells its subscribers", async () => {
    const api = fakeApi({
      "/api/registry/sessions": { sessions: fixture() },
      "/api/needs": { items: [need({ id: 7 })] },
    });
    const reg = createRegistry({ api });
    const heard = vi.fn();
    reg.subscribe(heard);
    await reg.load();
    expect(api.calls).toEqual(["/api/registry/sessions", "/api/needs"]);
    expect(reg.getState().sessions?.length).toBe(9);
    expect(reg.getState().needs?.map((n) => n.id)).toEqual([7]);
    expect(reg.needs()).toBe(2);
    expect(heard).toHaveBeenCalled();
  });

  it("keeps a failed load quiet: both lists stay as they were", async () => {
    const api = fakeApi({});
    const reg = createRegistry({ api });
    await reg.load();
    expect(reg.getState().sessions).toBeNull();
    expect(reg.getState().needs).toBeNull();
  });

  it("loads each list on its own: one failed read leaves the other standing", async () => {
    const api = fakeApi({
      "/api/registry/sessions": { sessions: fixture() },
      "/api/needs": Object.assign(new Error("down"), { status: 500 }),
    });
    const reg = createRegistry({ api });
    await reg.load();
    expect(reg.getState().sessions?.length).toBe(9);
    expect(reg.getState().needs).toBeNull();
    const other = createRegistry({
      api: fakeApi({
        "/api/registry/sessions": new Error("down"),
        "/api/needs": { items: [need({ id: 3 })] },
      }),
    });
    await other.load();
    expect(other.getState().sessions).toBeNull();
    expect(other.getState().needs?.map((n) => n.id)).toEqual([3]);
  });

  it("opens one session's detail, and says when there is no such session", async () => {
    const detail = { session_id: "s-1", state: "working", events: [] };
    const api = fakeApi({
      "/api/registry/sessions/s-1": detail,
      "/api/registry/sessions/nope": Object.assign(new Error("no session"), { status: 404 }),
    });
    const reg = createRegistry({ api });
    reg.setOpenId("s-1");
    await reg.loadSession("s-1");
    expect(reg.getState().open).toEqual(detail);
    reg.setOpenId("nope");
    await reg.loadSession("nope");
    expect(reg.getState().open).toBeNull();
    expect(reg.getState().openMissing).toBe(true);
    reg.setOpenId(null);
    expect(reg.getState().openId).toBeNull();
    expect(reg.getState().openMissing).toBe(false);
  });

  it("applies a registry.session row: the row moves between groups and lands first", async () => {
    const api = fakeApi({ "/api/registry/sessions": { sessions: fixture() } });
    const reg = createRegistry({ api });
    await reg.load();
    const before = groupSessions(reg.getState().sessions);
    expect(before.idle.map((s) => s.session_id)).not.toContain("s-work-1");
    reg.applyRow(row({ session_id: "s-work-1", state: "idle", title: "lead", last_event: "Stop" }));
    const st = reg.getState();
    const after = groupSessions(st.sessions);
    expect(after.idle.map((s) => s.session_id)).toContain("s-work-1");
    expect(after.working.map((s) => s.session_id)).not.toContain("s-work-1");
    // the refreshed row is the newest activity, so it leads the list
    expect(st.sessions?.[0]?.session_id).toBe("s-work-1");
    // a session the list has not seen arrives without waiting for a reload
    reg.applyRow(row({ session_id: "s-new", title: "fresh" }));
    expect(st.sessions?.find((s) => s.session_id === "s-new")).toBeDefined();
  });

  it("reloads the open session's detail when its own row changes", async () => {
    const api = fakeApi({
      "/api/registry/sessions": { sessions: [] },
      "/api/registry/sessions/s-1": { session_id: "s-1", events: [] },
    });
    const reg = createRegistry({ api });
    reg.setOpenId("s-1");
    await new Promise((r) => setTimeout(r, 0)); // setOpenId loads the detail on its own
    expect(api.calls.filter((c) => c === "/api/registry/sessions/s-1")).toHaveLength(1);
    reg.applyRow(row({ session_id: "s-1" }));
    await new Promise((r) => setTimeout(r, 0));
    expect(api.calls.filter((c) => c === "/api/registry/sessions/s-1")).toHaveLength(2);
  });

  it("hears a dead sign-in on the stream's error and hands the page over", async () => {
    const api = fakeApi({ "/api/registry/sessions": { sessions: [] } });
    const onSignedOut = vi.fn();
    const streams: ReturnType<typeof fakeSource>[] = [];
    const reg = createRegistry({
      api,
      EventSource: () => {
        const s = fakeSource();
        streams.push(s);
        return s.src;
      },
      fetch: () =>
        Promise.resolve({ status: 401, json: () => Promise.resolve({ signin: true }) } as Response),
      onSignedOut,
    });
    reg.connect();
    streams[0]?.error();
    await new Promise((r) => setTimeout(r, 0));
    expect(onSignedOut).toHaveBeenCalled();
    // a stream that merely dropped (a 200 on the whoami check) says nothing
    const quiet = createRegistry({
      api,
      EventSource: () => {
        const s = fakeSource();
        streams.push(s);
        return s.src;
      },
      fetch: () => Promise.resolve({ status: 200, json: () => Promise.resolve({}) } as Response),
      onSignedOut,
    });
    quiet.connect();
    streams[1]?.error();
    await new Promise((r) => setTimeout(r, 0));
    expect(onSignedOut).toHaveBeenCalledTimes(1);
  });

  it("follows the all-channels stream: hello loads, a row applies, a reconnect reloads", async () => {
    const api = fakeApi({
      "/api/registry/sessions": { sessions: fixture() },
      "/api/needs": { items: [need({ id: 7 })] },
    });
    const streams: ReturnType<typeof fakeSource>[] = [];
    const reg = createRegistry({
      api,
      EventSource: (url) => {
        expect(url).toBe("/api/registry/live");
        const s = fakeSource();
        streams.push(s);
        return s.src;
      },
    });
    reg.connect();
    expect(reg.getState().live).toBe("connecting");
    streams[0]?.open();
    expect(reg.getState().live).toBe("live");
    streams[0]?.send({ type: "hello" });
    await new Promise((r) => setTimeout(r, 0));
    expect(api.calls).toEqual(["/api/registry/sessions", "/api/needs"]);
    // a live row for a session the list knows moves it without any reload
    streams[0]?.send({ type: "registry.session", data: row({ session_id: "s-idle-1", state: "working" }) });
    expect(groupSessions(reg.getState().sessions).working.map((s) => s.session_id)).toContain("s-idle-1");
    // a frame that is not JSON changes nothing
    streams[0]?.src.onmessage?.({ data: "{" });
    // a reconnect's hello refills the list: what went by while away was not pushed
    streams[0]?.send({ type: "hello" });
    await new Promise((r) => setTimeout(r, 0));
    expect(api.calls).toEqual([
      "/api/registry/sessions",
      "/api/needs",
      "/api/registry/sessions",
      "/api/needs",
    ]);
    streams[0]?.error();
    expect(reg.getState().live).toBe("offline");
    reg.disconnect();
    expect(streams[0]?.closed).toBe(true);
    reg.connect();
    expect(streams[1]).toBeDefined();
    reg.disconnect();
  });

  it("applies a needs.item row the stream pushes: an open one lands, a decided one leaves", async () => {
    const api = fakeApi({ "/api/registry/sessions": { sessions: [] }, "/api/needs": { items: [] } });
    const streams: ReturnType<typeof fakeSource>[] = [];
    const reg = createRegistry({
      api,
      EventSource: () => {
        const s = fakeSource();
        streams.push(s);
        return s.src;
      },
    });
    reg.connect();
    streams[0]?.open();
    streams[0]?.send({ type: "hello" });
    await new Promise((r) => setTimeout(r, 0));
    streams[0]?.send({ type: "needs.item", data: need({ id: 7 }) });
    expect(reg.getState().needs?.map((n) => n.id)).toEqual([7]);
    // the same item again replaces its row instead of doubling it
    streams[0]?.send({ type: "needs.item", data: need({ id: 7, state: "snoozed" }) });
    expect(reg.getState().needs?.map((n) => n.id)).toEqual([7]);
    expect(reg.getState().needs?.[0]?.state).toBe("snoozed");
    // a decided (or withdrawn, expired) item is no longer waiting: it leaves the list
    streams[0]?.send({ type: "needs.item", data: need({ id: 7, state: "allowed" }) });
    expect(reg.getState().needs).toEqual([]);
    // a frame that is no item changes nothing
    streams[0]?.send({ type: "needs.item", data: { id: "x" } });
    streams[0]?.send({ type: "registry.session", data: { nope: true } });
    expect(reg.getState().needs).toEqual([]);
  });
});

describe("the needs-you items' pure reads", () => {
  it("answers a session's open items, its permission ask first", () => {
    const needs = [
      need({ id: 1, session: "s-1", kind: "question", summary: "Keep the timeout?" }),
      need({ id: 2, session: "s-1" }),
      need({ id: 3, session: "s-2" }),
      need({ id: 4, session: "s-1", state: "snoozed", snoozed_until: "2026-10-16T13:00:00Z" }),
      need({ id: 5, session: "s-1", state: "denied" }),
    ];
    expect(openItemsOf(needs, "s-1").map((n) => n.id)).toEqual([1, 2]);
    expect(permissionOf(needs, "s-1")?.id).toBe(2);
    expect(permissionOf(needs, "s-2")?.id).toBe(3);
    expect(permissionOf(needs, "s-none")).toBeUndefined();
    expect(openItemOf(needs, "s-1")?.id).toBe(2);
    // a session with no permission ask answers its first open item
    expect(
      openItemOf(
        needs.filter((n) => n.id !== 2),
        "s-1",
      )?.id,
    ).toBe(1);
    expect(openItemOf(null, "s-1")).toBeUndefined();
    expect(permissionOf(null, "s-1")).toBeUndefined();
  });
});
