// Now.test.tsx — the Session screen: the header block (the phone's title with its back link and
// where line, the desktop's title and where line, the status line with its bypass word, the Now
// card), the transcript of a Claude Code session (its prompts, replies and folded runs, read once
// and again when the hub says it grew), the hook-event stream of a session whose transcript the
// hub cannot read (bubbles, tool rows, day dividers, opens at the end and never follows), the
// stale-hooks resume line, the desktop context pane, the small pieces (the mark), and the linked
// session's brief and hand over with their step-up, session text always rendered as text, never
// HTML.
import { act, cleanup, screen, within } from "@testing-library/preact";
import type { ComponentChildren, JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import { describe, expect, it, vi } from "vite-plus/test";
import type { Api } from "../../src/api.ts";
import type { Layout } from "../../src/app/layout.ts";
import { NowContext, SessionScreen } from "../../src/app/Now.tsx";
import { StateMark } from "../../src/app/session-parts.tsx";
import type { ConvoItem } from "../../src/session/types.ts";
import type { EventSourceLike } from "../../src/live.ts";
import {
  createRegistry,
  type NeedItem,
  type RegistryDetail,
  type RegistryEvent,
  type RegistrySession,
  type RegistryStore,
} from "../../src/registry.ts";
import { flush, legacyClasses, makeCtx, makeRegistry, makeState, renderIn, stubMedia } from "../helpers.tsx";

/** One registry row, with the fields the list reads; the test overrides what it looks at. */
const row = (over: Partial<RegistrySession> & { session_id: string }): RegistrySession => ({
  state: "working",
  last_event: "UserPromptSubmit",
  last_event_at: "2026-10-08T11:59:30Z",
  ...over,
});

/** The fixture the list reads: two that need the owner, three at work, two idle, one stuck, one
 *  ended, over three machines and two projects. */
const fixture = (): RegistrySession[] => [
  row({
    session_id: "s-need-1",
    state: "needs-you",
    title: "kanban-e2e",
    machine: "laptop",
    project: "/work/claude-code-plugins",
    branch: "port/KBN-E2E",
    task: "Run the board export e2e",
  }),
  row({
    session_id: "s-need-2",
    state: "needs-you",
    title: "api-gateway",
    machine: "build-box",
    project: "/work/gateway",
    branch: "feat/retry-budget",
    task: "Asks: keep the 30 s upstream timeout?",
  }),
  row({
    session_id: "s-work-1",
    title: "lead",
    machine: "studio",
    project: "/work/claude-code-plugins",
    model: "claude-opus-4-6",
    task: "Release kanban 0.0.1",
    context_percent: 41,
    channel: "kanban-release",
  }),
  row({
    session_id: "s-work-2",
    title: "hub-home",
    machine: "build-box",
    project: "/work/claude-code-plugins",
    model: "glm-5.3",
    task: "Import the per-repo homes",
    context_percent: 63,
    channel: "kanban-release",
  }),
  row({
    session_id: "s-work-3",
    title: "radar-ui",
    machine: "laptop",
    project: "/work/claude-code-plugins",
    model: "claude-sonnet-4-5",
    task: "Budget chart on the board",
    context_percent: 22,
  }),
  row({
    session_id: "s-idle-1",
    state: "idle",
    title: "docs-sweep",
    machine: "studio",
    project: "/work/claude-code-plugins",
    last_event: "Stop",
    last_event_at: "2026-10-08T11:48:00Z",
  }),
  row({
    session_id: "s-idle-2",
    state: "idle",
    title: "notes",
    machine: "laptop",
    project: "/work/gateway",
    last_event: "SubagentStop",
    last_event_at: "2026-10-08T11:00:00Z",
  }),
  row({
    session_id: "s-stuck",
    state: "stuck",
    title: "quiet-one",
    machine: "studio",
    project: "/work/gateway",
  }),
  row({
    session_id: "s-done",
    state: "ended",
    title: "finished",
    machine: "build-box",
    project: "/work/gateway",
  }),
];

/** One session detail, as GET /api/registry/sessions/<id> answers it. */
const detail = (over: Partial<RegistryDetail> = {}): RegistryDetail => ({
  session_id: "s-need-1",
  state: "needs-you",
  title: "kanban-e2e",
  machine: "laptop",
  project: "/work/claude-code-plugins",
  branch: "port/KBN-E2E",
  task: "Run the board export e2e",
  model: "glm-5.3",
  context_percent: 58,
  channel: "kanban-release",
  last_event: "UserPromptSubmit",
  last_event_at: "2026-10-08T11:59:30Z",
  events: [
    {
      seq: 1,
      report: { session_id: "s-need-1", last_event: "SessionStart", last_event_at: "2026-10-08T11:48:00Z" },
    },
    {
      seq: 2,
      report: {
        session_id: "s-need-1",
        last_event: "UserPromptSubmit",
        task: "Add the e2e test",
        last_event_at: "2026-10-08T11:50:00Z",
      },
    },
    {
      seq: 3,
      report: {
        session_id: "s-need-1",
        last_event: "PostToolUse",
        cwd: "/work/claude-code-plugins",
        tool: "Bash",
        tool_target: "npm test -- --run e2e",
        last_event_at: "2026-10-08T11:55:00Z",
      },
    },
    {
      seq: 4,
      report: {
        session_id: "s-need-1",
        last_event: "Stop",
        task: "The export inlines every ticket",
        last_event_at: "2026-10-08T11:58:00Z",
      },
    },
  ],
  ...over,
});

/** One needs-you item on s-need-1, as the actions read it. */
const needItem = (): NeedItem => ({
  id: 7,
  session: "s-need-1",
  kind: "permission",
  summary: "Bash: npm test -- --run e2e",
  created_at: "2026-10-08T11:59:30Z",
  state: "open",
  tool: "Bash",
  input: "npm test -- --run e2e",
});

/** Mounts what `make` returns under a context whose registry repaints it on every change, the
 *  way the app's boot does — so a live row moves a row the moment it lands. `make` runs again on
 *  every repaint: one element held by reference would keep Preact from re-rendering it. */
function followRender(
  make: () => ComponentChildren,
  registry: RegistryStore,
  api?: Api,
): ReturnType<typeof renderIn> {
  function Follow(): JSX.Element {
    const [, set] = useState(0);
    useEffect(() => registry.subscribe(() => set((n) => n + 1)), [registry]);
    return <>{make()}</>;
  }
  return renderIn(<Follow />, makeCtx(makeState(), api ? { registry, api } : { registry }));
}

describe("the Session screen", () => {
  /** Mounts the session. Its harness is Codex unless the test names one, so the page reads the
   *  stream of hook events a session without a transcript keeps; a Claude Code session reads its
   *  transcript through `api`. */
  const mountSession = (
    layout: Layout = "phone",
    over: Partial<RegistryDetail> = {},
    needs: NeedItem[] | null = null,
    api?: Api,
  ): RegistryStore => {
    stubMedia({
      "(min-width: 48rem)": layout !== "phone",
      "(min-width: 75rem)": layout === "desktop",
    });
    const registry = makeRegistry({
      sessions: fixture(),
      open: detail({ harness: "codex", ...over }),
      openId: "s-need-1",
      needs,
    });
    followRender(() => <SessionScreen id="s-need-1" layout={layout} />, registry, api);
    return registry;
  };

  it("matches the mock's phone header: back, title, state word and the where line, no chips", () => {
    mountSession("phone", { harness: "claude" });
    const back = screen.getByRole("link", { name: "Back to Sessions" }) as HTMLElement;
    expect(back.getAttribute("href")).toBe("#/sessions");
    // the mock's back control: the chevron in the accent ink
    expect(back.className).toContain("text-[var(--ui-accent)]");
    const header = document.querySelector("[data-session] header") as HTMLElement;
    expect(header.querySelector("[data-session-title]")?.textContent).toBe("kanban-e2e");
    const state = header.querySelector("[data-state]") as HTMLElement;
    expect(state.textContent).toBe("Needs you");
    expect(state.getAttribute("data-state")).toBe("needs-you");
    // the where line rides in the title's column at 12 px, the mock's size for it
    const where = screen.getByText("claude-code-plugins · port/KBN-E2E · laptop") as HTMLElement;
    expect(where.className).toContain("text-xs");
    expect(screen.getByText("claude-code-plugins · port/KBN-E2E · laptop").parentElement?.parentElement).toBe(
      header.querySelector("[data-session-title]")?.parentElement,
    );
    // the chips row is gone: no model chip, no harness pill, no channel chip, no context bar
    expect(header.querySelector("[data-model-dot]")).toBeNull();
    expect(header.querySelector("[data-harness]")).toBeNull();
    expect(header.textContent).not.toContain("#kanban-release");
    expect(screen.queryByLabelText("Context 58 percent used")).toBeNull();
  });

  it("reads the session's facts in the status line under the header, the foot carrying none", () => {
    mountSession("phone", { harness: "claude" });
    const status = document.querySelector("[data-session] header [data-status-line]") as HTMLElement;
    expect(status.textContent).toBe("Needs you· glm-5.3 · 58% context");
    expect(status.className).toContain("text-[13px]");
    expect(document.querySelector("[data-session-facts]")).toBeNull();
  });

  it("draws no model or context in the status line when the session carries none", () => {
    const { model: _model, context_percent: _pct, ...bare } = detail();
    stubMedia({ "(min-width: 48rem)": false, "(min-width: 75rem)": false });
    const registry = makeRegistry({ sessions: fixture(), open: bare, openId: "s-need-1" });
    followRender(() => <SessionScreen id="s-need-1" layout="phone" />, registry);
    const status = document.querySelector("[data-status-line]") as HTMLElement;
    expect(status.textContent).toBe("Needs you");
  });

  it("says bypass in the warn ink when the turn's permission mode runs without asking", async () => {
    const asks = (mode: string) =>
      apiFake((path) =>
        path === "/api/sessions/s-need-1/turn" ? { ended: false, permission_mode: mode } : {},
      ).api;
    mountSession("phone", { harness: "claude" }, null, asks("bypassPermissions"));
    await flush();
    const word = screen.getByText("bypass") as HTMLElement;
    expect(word.className).toContain("text-[var(--ui-warn-ink)]");
    expect(word.closest("[data-status-line]")?.textContent).toBe("Needs you· glm-5.3 · 58% context · bypass");
    cleanup();
    mountSession("phone", { harness: "claude" }, null, asks("default"));
    await flush();
    expect(screen.queryByText("bypass")).toBeNull();
  });

  it("names the session's harness: the desk header's badge always, the phone's status line for any but Claude Code", () => {
    for (const [harness, id, name] of [
      ["claude", "claude", "Claude Code"],
      ["codex", "codex", "Codex"],
    ] as const) {
      cleanup();
      mountSession("desktop", { harness });
      const chip = document.querySelector("[data-session] header [data-harness]") as HTMLElement | null;
      expect(chip, `desktop ${harness}`).not.toBeNull();
      expect(chip?.dataset.harness).toBe(id);
      expect(chip?.textContent).toContain(name);
      expect(chip?.querySelector("[data-harness-mark]")).not.toBeNull();
    }
    // a report that names no harness comes from Claude Code
    cleanup();
    const { harness: _harness, ...bare } = detail();
    stubMedia({ "(min-width: 48rem)": true, "(min-width: 75rem)": true });
    followRender(
      () => <SessionScreen id="s-need-1" layout="desktop" />,
      makeRegistry({ sessions: fixture(), open: bare, openId: "s-need-1" }),
    );
    expect((document.querySelector("[data-harness]") as HTMLElement).dataset.harness).toBe("claude");

    cleanup();
    mountSession("phone", { harness: "claude" });
    expect(document.querySelector("[data-session] header [data-harness]")).toBeNull();
    cleanup();
    mountSession("phone", { harness: "codex" });
    const chip = document.querySelector("[data-session] header [data-harness]") as HTMLElement | null;
    expect(chip?.textContent).toContain("Codex");
  });

  it("paints the header, the stream and the resume line on the tokens, with no daisyUI or legacy class", () => {
    for (const layout of ["phone", "tablet", "desktop"] as const) {
      cleanup();
      mountSession(layout, { hooks_stale: true });
      // the needs bar is the Needs page's own part
      expect(legacyClasses(document.body, "[data-needs-bar], [data-needs-sheet]"), layout).toEqual([]);
    }
  });

  it("gives the header's state pill its own state's tone, never orange for everything", () => {
    const turn: NeedItem = {
      id: 11,
      session: "s-need-1",
      kind: "turn",
      summary: "The export inlines every ticket",
      created_at: "2026-10-08T11:59:30Z",
      state: "open",
    };
    for (const [state, needs, role] of [
      ["idle", null, "idle"],
      ["working", null, "working"],
      ["stuck", null, "failed"],
      ["ended", null, "cancelled"],
      ["needs-you", [needItem()], "needs-you"],
      ["needs-you", [turn], "turn-done"],
    ] as const) {
      cleanup();
      mountSession("phone", { state }, needs === null ? null : [...needs]);
      const pill = document.querySelector("[data-session] header [data-state]") as HTMLElement;
      expect(pill.dataset.role, state).toBe(role);
      expect(pill.className, state).toContain(`text-[var(--ui-${role}-ink)]`);
      // the state reads at the mock's 13 px, not the title's size
      expect(pill.closest("[data-status-line]")?.className, state).toContain("text-[13px]");
      expect(pill.className, state).not.toContain("warning");
    }
  });

  it("reads a session with an open item as Needs you in its header, not idle", () => {
    mountSession("phone", { state: "idle" }, [needItem()]);
    const pill = document.querySelector("[data-session] header [data-state]") as HTMLElement;
    expect(pill.textContent).toBe("Needs you");
    expect(pill.dataset.role).toBe("needs-you");
  });

  it("places the open ask as the page's second block, right under the header, on every layout", () => {
    for (const layout of ["phone", "tablet", "desktop"] as const) {
      cleanup();
      mountSession(layout, {}, [needItem()]);
      const screenEl = document.querySelector("[data-item-content]") as HTMLElement;
      const kids = Array.from(screenEl.children);
      expect(kids[0]?.tagName, layout).toBe("HEADER");
      expect(kids[1]?.getAttribute("data-block"), layout).toBe("ask");
      expect(screenEl.querySelectorAll("[data-block='ask']").length, layout).toBe(1);
      expect(document.querySelector("dialog.ui-sheet"), layout).toBeNull();
    }
  });

  it("offers Reply instead on the wide layouts' ask and not on the phone's", () => {
    mountSession("phone", {}, [needItem()]);
    expect(screen.queryByRole("button", { name: "Reply instead" })).toBeNull();
    for (const layout of ["tablet", "desktop"] as const) {
      cleanup();
      mountSession(layout, {}, [needItem()]);
      expect(screen.getByRole("button", { name: "Reply instead" }), layout).toBeDefined();
    }
  });

  it("streams the events: prompts as bubbles, tool events as one line, notes as paragraphs", () => {
    mountSession("phone");
    const stream = document.querySelector("[data-stream]") as HTMLElement;
    expect(stream).not.toBeNull();
    const bubble = screen.getByText("Add the e2e test");
    expect(bubble.className).toContain("self-end");
    const tool = screen.getByText("npm test -- --run e2e");
    expect(tool.tagName).toBe("CODE");
    const row = tool.closest("[data-tool]") as HTMLElement;
    expect(row.textContent).toContain("Bash");
    expect(row.querySelector("svg")).not.toBeNull();
    expect(row.hasAttribute("data-running")).toBe(false);
    expect(stream.textContent).not.toContain("/work/claude-code-plugins");
    expect(screen.getByText("The export inlines every ticket").tagName).toBe("DIV");
    expect(stream.querySelector("img")).toBeNull();
    // a day change puts a divider between the items, spelled Today, Yesterday, then the date
    expect(screen.getByText(/^Today /)).toBeDefined();
  });

  it("marks a tool call that has not finished as running", () => {
    mountSession("phone", {
      events: [
        {
          seq: 1,
          report: {
            session_id: "s-need-1",
            last_event: "PreToolUse",
            cwd: "/work/claude-code-plugins",
            tool: "Bash",
            tool_target: "bun test",
            last_event_at: "2026-10-08T11:55:00Z",
          },
        },
      ],
    });
    const row = screen.getByText("bun test").closest("[data-tool]") as HTMLElement;
    expect(row.hasAttribute("data-running")).toBe(true);
    expect(row.textContent).toContain("running");
  });

  /** One event on s-need-1, at a fixed time on the fixture's day. */
  const evt = (
    seq: number,
    over: Partial<RegistryEvent["report"]> & { last_event: string },
  ): RegistryEvent => ({
    seq,
    report: { session_id: "s-need-1", last_event_at: "2026-10-08T11:50:00Z", cwd: "/work/p", ...over },
  });

  /** happy-dom lays nothing out: the stream pane gets 40 px per child of its column and a 300 px
   *  window, and its scrollTop clamps to the range a browser allows. */
  const stubStreamLayout = (): (() => void) => {
    const proto = HTMLElement.prototype;
    const tops = new WeakMap<HTMLElement, number>();
    const isStream = (el: HTMLElement): boolean => el.hasAttribute("data-stream");
    const height = (el: HTMLElement): number => (el.firstElementChild?.children.length ?? 0) * 40;
    const saved = ["scrollHeight", "clientHeight", "scrollTop"].map(
      (k) => [k, Object.getOwnPropertyDescriptor(proto, k)] as const,
    );
    Object.defineProperty(proto, "scrollHeight", {
      configurable: true,
      get(this: HTMLElement) {
        return isStream(this) ? height(this) : 0;
      },
    });
    Object.defineProperty(proto, "clientHeight", {
      configurable: true,
      get(this: HTMLElement) {
        return isStream(this) ? 300 : 0;
      },
    });
    Object.defineProperty(proto, "scrollTop", {
      configurable: true,
      get(this: HTMLElement) {
        return tops.get(this) ?? 0;
      },
      set(this: HTMLElement, v: number) {
        tops.set(this, Math.max(0, Math.min(v, this.scrollHeight - this.clientHeight)));
      },
    });
    return () => {
      for (const [k, d] of saved) {
        if (d) Object.defineProperty(proto, k, d);
        else delete (proto as unknown as Record<string, unknown>)[k];
      }
    };
  };

  /** Fifty prompts: fifty stream items, far taller than the pane. */
  const fifty = (): RegistryEvent[] =>
    Array.from({ length: 50 }, (_, i) =>
      evt(i + 1, { last_event: "UserPromptSubmit", task: `Prompt ${i + 1}` }),
    );

  it("opens the stream at its latest event", () => {
    const restore = stubStreamLayout();
    try {
      mountSession("phone", { events: fifty() });
      const stream = document.querySelector("[data-stream]") as HTMLElement;
      expect(stream.scrollHeight).toBeGreaterThan(stream.clientHeight);
      expect(stream.scrollTop).toBe(stream.scrollHeight - stream.clientHeight);
    } finally {
      restore();
    }
  });

  it("opens at the end once and never follows: new events leave the reader where they are", () => {
    const restore = stubStreamLayout();
    try {
      const registry = mountSession("phone", { events: fifty() });
      const stream = document.querySelector("[data-stream]") as HTMLElement;
      const more = (from: number): void => {
        const open = registry.getState().open as RegistryDetail;
        open.events = [
          ...open.events,
          ...Array.from({ length: 5 }, (_, i) =>
            evt(from + i, { last_event: "UserPromptSubmit", task: `Prompt ${from + i}` }),
          ),
        ];
        void act(() => registry.setNeeds(registry.getState().needs));
      };
      const end = stream.scrollHeight - stream.clientHeight;
      expect(stream.scrollTop).toBe(end);
      // at the bottom, five more events arrive: the page stays where it was
      more(51);
      expect(stream.scrollHeight - stream.clientHeight).toBeGreaterThan(end);
      expect(stream.scrollTop).toBe(end);

      stream.scrollTop = 100;
      void act(() => {
        stream.dispatchEvent(new Event("scroll"));
      });
      more(56);
      expect(stream.scrollTop).toBe(100);
    } finally {
      restore();
    }
  });

  it("folds repeated calls of one tool into one row with a count that a tap opens to each call", () => {
    mountSession("phone", {
      events: [
        evt(1, { last_event: "PostToolUse", tool: "Read", tool_target: "/work/p/a.ts" }),
        evt(2, { last_event: "PostToolUse", tool: "Read", tool_target: "/work/p/b.ts" }),
        evt(3, { last_event: "PreToolUse", tool: "Read", tool_target: "/work/p/c.ts" }),
        evt(4, { last_event: "PostToolUse", tool: "Bash", tool_target: "git status" }),
      ],
    });
    const rows = [...document.querySelectorAll<HTMLElement>("[data-stream] [data-tool]")];
    expect(rows).toHaveLength(2);
    const group = screen.getByRole("button", { name: /Read.*3 calls/ });
    expect(group.getAttribute("aria-expanded")).toBe("false");
    expect(group.closest("[data-tool]")?.hasAttribute("data-running")).toBe(true);
    expect(group.textContent).toContain("×3");
    expect(document.querySelector("[data-tool-calls]")).toBeNull();

    void act(() => {
      group.click();
    });
    expect(group.getAttribute("aria-expanded")).toBe("true");
    const calls = [...document.querySelectorAll<HTMLElement>("[data-tool-calls] > *")];
    expect(calls.map((c) => c.querySelector("code")?.textContent)).toEqual(["a.ts", "b.ts", "c.ts"]);
    for (const c of calls) expect(c.querySelector("code")?.className).toContain("ui-mono");
    expect(calls[2]?.hasAttribute("data-running")).toBe(true);
    expect(calls[2]?.textContent).toContain("running");
    expect(legacyClasses(document.body, "[data-needs-bar], [data-needs-sheet]")).toEqual([]);

    void act(() => {
      group.click();
    });
    expect(document.querySelector("[data-tool-calls]")).toBeNull();
  });

  it("marks a call whose PostToolUse never landed as ended once the turn stopped", () => {
    mountSession("phone", {
      events: [
        evt(1, { last_event: "PreToolUse", tool: "Bash", tool_target: "rm -rf build" }),
        evt(2, { last_event: "Stop" }),
      ],
    });
    const row = screen.getByText("rm -rf build").closest("[data-tool]") as HTMLElement;
    expect(row.hasAttribute("data-running")).toBe(false);
    expect(row.hasAttribute("data-ended")).toBe(true);
    expect(row.textContent).toContain("ended");
  });

  it("divides the stream by day: Yesterday, then the plain date", () => {
    mountSession("phone", {
      events: [
        {
          seq: 1,
          report: {
            session_id: "s-need-1",
            last_event: "UserPromptSubmit",
            task: "Older",
            last_event_at: "2026-10-06T10:00:00Z",
          },
        },
        {
          seq: 2,
          report: {
            session_id: "s-need-1",
            last_event: "UserPromptSubmit",
            task: "Recent",
            last_event_at: "2026-10-07T10:00:00Z",
          },
        },
        {
          seq: 3,
          report: {
            session_id: "s-need-1",
            last_event: "UserPromptSubmit",
            task: "Newest",
            last_event_at: "2026-10-08T10:00:00Z",
          },
        },
      ],
    });
    expect(screen.getByText(/^Yesterday /)).toBeDefined();
    expect(screen.getByText(/^Oct [67] /)).toBeDefined();
    expect(screen.getByText("Older")).toBeDefined();
  });

  it("renders event text as text, never HTML", () => {
    mountSession("phone", {
      task: '<img onerror="alert(1)">',
      events: [
        {
          seq: 1,
          report: {
            session_id: "s-need-1",
            last_event: "UserPromptSubmit",
            task: "<img onerror=alert(1)>",
            last_event_at: "2026-10-08T11:50:00Z",
          },
        },
      ],
    });
    expect(document.querySelector("[data-stream] img")).toBeNull();
    expect(screen.getByText("<img onerror=alert(1)>")).toBeDefined();
  });

  /** A hub that answers one transcript: `lines` is what the file holds, each line one item and 10
   *  bytes; a read from a byte cursor answers the lines from there on. */
  const transcriptHub = (lines: ConvoItem[]) => {
    const hub = { lines, reads: [] as string[] };
    const answer = (path: string): unknown => {
      if (!path.startsWith("/api/sessions/s-need-1/agents/main/conversation")) return {};
      hub.reads.push(path);
      const after = Number(path.split("after=")[1] ?? 0);
      return { items: hub.lines.slice(after / 10), next: hub.lines.length * 10 };
    };
    return { hub, api: apiFake(answer).api };
  };
  const said = (id: string, kind: "user" | "assistant", text: string): ConvoItem => ({
    id,
    kind,
    at: "2026-10-08T11:55:00Z",
    text,
  });

  it("paints a Claude Code session's transcript, not its hook events, once its first read answers", async () => {
    const { hub, api } = transcriptHub([
      said("p1", "user", "Move the backup job to pve2"),
      said("r1", "assistant", "Looking at how the job runs today."),
    ]);
    mountSession("phone", { harness: "claude" }, null, api);
    // before the first read answers the page paints neither pane's rows
    expect(document.querySelector("[data-stream]")).toBeNull();
    await flush();
    const pane = document.querySelector("[data-block='transcript']") as HTMLElement;
    expect(pane).not.toBeNull();
    expect(screen.getByText("Move the backup job to pve2")).toBeDefined();
    expect(screen.getByText("Looking at how the job runs today.")).toBeDefined();
    expect(document.querySelector("[data-stream]")).toBeNull();
    // the read followed its cursor until it stopped moving, and asked nothing again by itself
    expect(hub.reads).toEqual([
      "/api/sessions/s-need-1/agents/main/conversation?after=0",
      "/api/sessions/s-need-1/agents/main/conversation?after=20",
    ]);
  });

  it("reads on from its cursor when the hub says the conversation grew, and sets the news under a New pill", async () => {
    const { hub, api } = transcriptHub([said("p1", "user", "Move the backup job to pve2")]);
    const registry = mountSession("phone", { harness: "claude" }, null, api);
    await flush();
    expect(document.querySelector("[data-new]")).toBeNull();
    hub.reads.length = 0;

    hub.lines.push(said("r1", "assistant", "First run finished clean."));
    registry.getState().convoGrew = { "s-need-1": 1 };
    await act(async () => {
      registry.setNeeds(registry.getState().needs);
    });
    await flush();
    expect(hub.reads[0]).toBe("/api/sessions/s-need-1/agents/main/conversation?after=10");
    expect(screen.getByText("First run finished clean.")).toBeDefined();
    expect(screen.getByRole("button", { name: "New" })).toBeDefined();
    // another session's growth is no read of this one
    hub.reads.length = 0;
    registry.getState().convoGrew = { "s-need-1": 1, "s-other": 4 };
    await act(async () => {
      registry.setNeeds(registry.getState().needs);
    });
    await flush();
    expect(hub.reads).toEqual([]);
  });

  it("reads a transcript again after the registry stream reconnects: what grew while it was down appears", async () => {
    const { hub, api } = transcriptHub([said("p1", "user", "Move the backup job to pve2")]);
    stubMedia({ "(min-width: 48rem)": false, "(min-width: 75rem)": false });
    const source = { onopen: null, onerror: null, onmessage: null, close: () => {} } as EventSourceLike;
    const greet = (): void => source.onmessage?.({ data: JSON.stringify({ type: "hello" }) });
    const registry = createRegistry({
      api: apiFake((path) =>
        path === "/api/registry/sessions/s-need-1" ? detail({ harness: "claude" }) : {},
      ).api,
      EventSource: () => source,
    });
    Object.assign(registry.getState(), {
      sessions: fixture(),
      open: detail({ harness: "claude" }),
      openId: "s-need-1",
    });
    followRender(() => <SessionScreen id="s-need-1" layout="phone" />, registry, api);
    registry.connect();
    greet();
    await flush();
    expect(screen.getByText("Move the backup job to pve2")).toBeDefined();
    expect(screen.queryByText("First run finished clean.")).toBeNull();

    // the stream drops; the transcript grows with no event to say so; the stream greets again
    hub.lines.push(said("r1", "assistant", "First run finished clean."));
    greet();
    await flush();
    await flush();
    expect(screen.getByText("First run finished clean.")).toBeDefined();
    registry.disconnect();
  });

  it("keeps the hook-event stream for a Claude Code session whose transcript answers no items", async () => {
    const { api } = transcriptHub([]);
    mountSession("phone", { harness: "claude" }, null, api);
    await flush();
    expect(document.querySelector("[data-stream]")).not.toBeNull();
    expect(document.querySelector("[data-block='transcript']")).toBeNull();
    expect(screen.getByText("Add the e2e test")).toBeDefined();
  });

  it("shows the Now card with the ask's words and how long they have waited", () => {
    mountSession("phone", {}, [needItem()]);
    const card = document.querySelector("[data-session] header [data-now-card]") as HTMLElement;
    expect(card.textContent).toBe("NowWaiting for you to allow a command: npm test -- --run e2e30 s");
    const lead = within(card).getByText("Waiting for you");
    expect(lead.className).toContain("text-[var(--ui-warn-ink)]");
  });

  it("waits quietly while the detail loads, and says when there is no such session", () => {
    stubMedia({ "(min-width: 48rem)": false, "(min-width: 75rem)": false });
    const loading = makeRegistry({ sessions: [], open: null, openId: "s-x" });
    followRender(() => <SessionScreen id="s-x" layout="phone" />, loading);
    expect(screen.getByText("Reading the session…")).toBeDefined();
    const missing = makeRegistry({ sessions: [], open: null, openId: "s-x", openMissing: true });
    followRender(() => <SessionScreen id="s-x" layout="phone" />, missing);
    expect(screen.getByText(/no session s-x/)).toBeDefined();
  });

  it("says how to restart a session whose hooks stopped reporting, and offers nothing to press", () => {
    for (const layout of ["phone", "desktop"] as const) {
      cleanup();
      mountSession(layout, { hooks_stale: true });
      const header = document.querySelector("[data-session] header") as HTMLElement;
      expect(header.querySelector("[data-hooks-stale]")?.textContent, layout).toBe("hooks stale");
      const line = document.querySelector("[data-resume]") as HTMLElement;
      expect(line.textContent, layout).toBe(
        "Hooks stopped reporting: restart this session with claude --resume s-need-1",
      );
      expect(line.querySelector("code")?.textContent, layout).toBe("claude --resume s-need-1");
      expect(line.querySelectorAll("a, button").length, layout).toBe(0);
    }
    cleanup();
    mountSession("phone");
    expect(document.querySelector("[data-hooks-stale]")).toBeNull();
    expect(document.querySelector("[data-resume]")).toBeNull();
  });

  it("keeps the full name and no back link on the tablet and the desk, the back link on the phone alone", () => {
    for (const layout of ["tablet", "desktop"] as const) {
      cleanup();
      mountSession(layout);
      expect(screen.getByText("kanban-e2e"), layout).toBeDefined();
      expect(screen.queryByRole("link", { name: "Back to Sessions" }), layout).toBeNull();
    }
  });

  it("puts the desk title alone on its row and the facts in one status line under it", async () => {
    cleanup();
    stubMedia({ "(min-width: 48rem)": true, "(min-width: 75rem)": true });
    // the actions read the channel links before they render, so the api answers them
    const { api } = apiFake((path) =>
      path === "/api/links"
        ? {
            channels: [
              {
                channel: "kanban-release",
                links: [
                  {
                    channel: "kanban-release",
                    session_id: "s-need-1",
                    level: "digest",
                    linked_at: "2026-10-08T11:00:00Z",
                  },
                  {
                    channel: "kanban-release",
                    session_id: "s-work-1",
                    level: "digest",
                    linked_at: "2026-10-08T11:00:00Z",
                  },
                ],
              },
            ],
          }
        : {},
    );
    const registry = makeRegistry({
      sessions: fixture(),
      open: detail({ hooks_stale: true, teammates: [{ name: "scout", state: "working" }] }),
      openId: "s-need-1",
    });
    renderIn(<SessionScreen id="s-need-1" layout="desktop" />, makeCtx(makeState(), { registry, api }));
    await flush();
    const header = document.querySelector("[data-session] header") as HTMLElement;
    // the title stands alone on its row: its parent stacks the facts as a column
    const title = header.querySelector("[data-session-title]") as HTMLElement;
    expect(title.textContent).toBe("kanban-e2e");
    expect(title.parentElement?.classList.contains("flex-col")).toBe(true);
    // one status line under the title holds the state word, the model and the context
    const status = header.querySelector("[data-status-line]") as HTMLElement;
    expect(status).not.toBeNull();
    expect(status.textContent).toContain("Needs you");
    expect(status.textContent).toContain("glm-5.3");
    expect(status.textContent).toContain("58% context");
    expect(within(status).getByLabelText("needs you")).toBeDefined();
    // the chips leave the desk header with it; the stale-hooks chip and the harness stay
    expect(header.querySelector("[data-model-dot]")).toBeNull();
    expect(screen.queryByLabelText("Context 58 percent used")).toBeNull();
    expect(within(status).getByText("hooks stale")).toBeDefined();
    expect(header.querySelector("[data-harness]")).not.toBeNull();
    // the actions keep their place in the header
    expect(header.querySelector("[data-session-actions]")).not.toBeNull();
  });

  it("names the state as the mock does in the header's status line and the rail's Now row: first letter capitalised", () => {
    stubMedia({ "(min-width: 48rem)": true, "(min-width: 75rem)": true });
    const registry = makeRegistry({ sessions: fixture(), open: detail(), openId: "s-need-1" });
    followRender(
      () => (
        <>
          <SessionScreen id="s-need-1" layout="desktop" />
          <NowContext />
        </>
      ),
      registry,
    );
    const status = document.querySelector("[data-status-line]") as HTMLElement;
    const word = within(status).getByText("Needs you") as HTMLElement;
    // the word reads capitalised while its data attributes keep the raw state
    expect(word.getAttribute("data-state")).toBe("needs-you");
    expect(word.getAttribute("data-role")).toBe("needs-you");
    const rail = document.querySelector("[data-now-context]") as HTMLElement;
    expect(within(rail).getByText("Needs you")).toBeDefined();
    cleanup();
    const working = makeRegistry({
      sessions: fixture(),
      open: detail({ state: "working" }),
      openId: "s-need-1",
    });
    followRender(() => <NowContext />, working);
    expect(
      within(document.querySelector("[data-now-context]") as HTMLElement).getByText("Working"),
    ).toBeDefined();
  });
});

describe("the session rail", () => {
  const mountRail = (over: Partial<RegistryDetail> = {}, open: RegistryDetail = detail(over)): void => {
    stubMedia({ "(min-width: 48rem)": true, "(min-width: 75rem)": true });
    const registry = makeRegistry({ sessions: fixture(), open });
    followRender(() => <NowContext />, registry);
  };

  it("opens with the Now section: the state, its age and the facts the session carries", () => {
    mountRail({
      teammates: [
        { name: "scout", state: "working" },
        { name: "writer", state: "idle" },
      ],
    });
    const rail = document.querySelector("[data-now-context]") as HTMLElement;
    expect(within(rail).getByText("Now")).toBeDefined();
    // the state word on its row, with the age of the session's last event beside it
    expect(within(rail).getByText("Needs you")).toBeDefined();
    expect(rail.textContent).toContain("30 s");
    // the fact line holds the model, the context and the teammates
    const facts = rail.querySelector("[data-now-facts]") as HTMLElement;
    expect(facts.textContent).toContain("glm-5.3");
    expect(facts.textContent).toContain("58% context");
    expect(facts.textContent).toContain("2 agents");
    // the teammates run under their own head
    expect(within(rail).getByText("Running")).toBeDefined();
    expect(within(rail).getByText("scout")).toBeDefined();
    expect(within(rail).getByText("writer")).toBeDefined();
    // and nothing anywhere is a placeholder dash
    expect(rail.textContent).not.toContain("—");
  });

  it("leaves a fact out when the session does not carry it, and never draws a dash", () => {
    // the optional fields gone: this session carries no model, context, teammates or channel
    const { model: _model, context_percent: _pct, teammates: _mates, ...bare } = detail();
    mountRail({}, { ...bare, channel: "" });
    const rail = document.querySelector("[data-now-context]") as HTMLElement;
    // a session carrying nothing shows only its state and age: no fact row is drawn at all
    expect(rail.querySelector("[data-now-facts]")).toBeNull();
    // no teammates, no Running section; no channel, no linked list
    expect(within(rail).queryByText("Running")).toBeNull();
    expect(within(rail).queryByText(/Linked in/)).toBeNull();
    expect(rail.textContent).not.toContain("—");
  });

  it("keeps the linked sessions under their own head", () => {
    mountRail();
    expect(screen.getByText("Linked in #kanban-release")).toBeDefined();
    const linked = screen.getByText("lead").closest("a");
    expect(within(linked as HTMLElement).getByLabelText("working")).toBeDefined();
    expect(
      within(document.querySelector("[data-now-context]") as HTMLElement).getByText("Now"),
    ).toBeDefined();
  });
});

describe("the small pieces", () => {
  it("draws an unknown state as the hollow mark, saying its own name", () => {
    const mark = renderIn(<StateMark state="confused" />);
    expect(mark.getByRole("img").getAttribute("aria-label")).toBe("confused");
    expect(mark.getByRole("img").dataset.shape).toBe("hollow");
    mark.unmount();
  });
});

/** A recording api: every call kept with its body, the answer a function of the path. */
function apiFake(answer: (path: string, n: number) => unknown = () => ({})): {
  api: Api;
  calls: { path: string; body: unknown }[];
} {
  const calls: { path: string; body: unknown }[] = [];
  let n = 0;
  const call = (path: string, body?: unknown): Promise<unknown> => {
    // the list's Radar and Insights cards read their own routes; they are no act of the list's
    if (path.startsWith("/api/radar/") || path === "/api/insights") return Promise.resolve({});
    n += 1;
    calls.push({ path, body });
    const r = answer(path, n);
    return r instanceof Error ? Promise.reject(r) : Promise.resolve(r ?? {});
  };
  return {
    calls,
    api: {
      api: (path, o) => call(path, o?.body),
      op: () => Promise.resolve({}),
      channelPath: (ch, p) => `/api/c/${ch}${p}`,
      channelHref: (ch, p) => `#/channels/${ch}${p}`,
    } satisfies Api,
  };
}

describe("the session screen's link actions", () => {
  /** The passkey check the step-up sheet runs, as the browser would answer it. */
  const stubPasskey = (): void => {
    vi.stubGlobal("navigator", {
      ...navigator,
      credentials: {
        get: () =>
          Promise.resolve({
            id: "c",
            rawId: new Uint8Array([1]).buffer,
            type: "public-key",
            response: {
              authenticatorData: new Uint8Array([1]).buffer,
              clientDataJSON: new Uint8Array([1]).buffer,
              signature: new Uint8Array([1]).buffer,
              userHandle: null,
            },
          }),
      },
    });
  };
  const kanbanLinks = {
    channels: [
      {
        channel: "kanban-release",
        links: [
          {
            channel: "kanban-release",
            session_id: "s-need-1",
            level: "digest",
            linked_at: "2026-10-08T11:00:00Z",
          },
          {
            channel: "kanban-release",
            session_id: "s-work-1",
            level: "digest",
            linked_at: "2026-10-08T11:00:00Z",
          },
        ],
      },
    ],
  };
  const mountActions = (
    answer?: (path: string, n: number) => unknown,
  ): {
    calls: { path: string; body: unknown }[];
    toast: ReturnType<typeof vi.fn>;
  } => {
    cleanup();
    stubMedia({ "(min-width: 48rem)": false, "(min-width: 75rem)": false });
    const { api, calls } = apiFake(answer);
    const toast = vi.fn();
    const registry = makeRegistry({ sessions: fixture(), open: detail(), openId: "s-need-1" });
    renderIn(<SessionScreen id="s-need-1" layout="phone" />, makeCtx(makeState(), { registry, api, toast }));
    return { calls, toast };
  };

  it("TestTheLinkedSessionMenuOffersBriefAndHandOver", async () => {
    mountActions((path) => (path === "/api/links" ? kanbanLinks : {}));
    await flush();
    await act(async () => {
      screen.getByRole("button", { name: "More actions" }).click();
    });
    expect(screen.getByRole("menuitem", { name: "Brief another session…" })).toBeDefined();
    expect(screen.getByRole("menuitem", { name: "Hand over to…" })).toBeDefined();
    const actions = document.querySelector("[data-session-actions]") as HTMLElement;
    expect(legacyClasses(actions)).toEqual([]);
    for (const item of actions.querySelectorAll("[role=menuitem]")) {
      expect(item.className).toContain("min-h-[var(--ui-tap)]");
    }
    await act(async () => {
      screen.getByRole("menuitem", { name: "Hand over to…" }).click();
    });
    expect(legacyClasses(document.querySelector("[data-link-act]") as HTMLElement)).toEqual([]);
  });

  it("TestAnUnlinkedSessionCarriesNoLinkMenu", async () => {
    mountActions((path) =>
      path === "/api/links"
        ? {
            channels: [
              {
                channel: "other",
                links: [
                  { channel: "other", session_id: "s-x", level: "digest", linked_at: "2026-10-08T11:00:00Z" },
                ],
              },
            ],
          }
        : {},
    );
    await flush();
    expect(screen.queryByRole("button", { name: "More actions" })).toBeNull();
  });

  /** The brief's two answers: the first owes the step-up, the second goes through. */
  const briefAnswers = (): ((path: string) => unknown) => {
    let briefs = 0;
    return (path) => {
      if (path === "/api/links") return kanbanLinks;
      if (path === "/api/stepup/begin") return { options: { publicKey: { challenge: "aGk" } } };
      if (path !== "/api/brief") return {};
      briefs += 1;
      if (briefs > 1) return { route: "next_prompt" };
      return Object.assign(new Error("step-up required"), { status: 401 });
    };
  };

  it("TestBriefPostsFromToThroughTheStepUpAndToastsTheTarget", async () => {
    const h = mountActions(briefAnswers());
    stubPasskey();
    await flush();
    await act(async () => {
      screen.getByRole("button", { name: "More actions" }).click();
    });
    await act(async () => {
      screen.getByRole("menuitem", { name: "Brief another session…" }).click();
    });
    // the dialog picks among the sessions linked with this one, in any channel
    const select = screen.getByLabelText("Target session") as HTMLSelectElement;
    expect(select.value).toBe("s-work-1");
    await act(async () => {
      screen.getByRole("button", { name: "Brief" }).click();
    });
    await flush();
    expect(screen.getByText("Confirm with your passkey")).toBeDefined();
    await act(async () => {
      screen.getByRole("button", { name: "Use passkey" }).click();
    });
    await flush();
    expect(h.calls.filter((c) => c.path === "/api/brief")).toEqual([
      { path: "/api/brief", body: { from: "s-need-1", to: "s-work-1" } },
      { path: "/api/brief", body: { from: "s-need-1", to: "s-work-1" } },
    ]);
    expect(h.toast).toHaveBeenCalledWith("Briefed lead");
  });

  it("TestHandOverToastsTheMovedTasks", async () => {
    const h = mountActions((path) => {
      if (path === "/api/links") return kanbanLinks;
      if (path === "/api/handover") return { moved: ["t-1", "t-2"] };
      return {};
    });
    await flush();
    await act(async () => {
      screen.getByRole("button", { name: "More actions" }).click();
    });
    await act(async () => {
      screen.getByRole("menuitem", { name: "Hand over to…" }).click();
    });
    await act(async () => {
      screen.getByRole("button", { name: "Hand over" }).click();
    });
    await flush();
    expect(h.calls.find((c) => c.path === "/api/handover")?.body).toEqual({
      from: "s-need-1",
      to: "s-work-1",
    });
    expect(h.toast).toHaveBeenCalledWith("Handed over to lead: 2 tasks moved");
  });

  it("TestAHandOverRefusalIsToasted", async () => {
    const h = mountActions((path) =>
      path === "/api/links"
        ? kanbanLinks
        : Object.assign(new Error("hand over needs both sessions in one channel"), { status: 409 }),
    );
    await flush();
    await act(async () => {
      screen.getByRole("button", { name: "More actions" }).click();
    });
    await act(async () => {
      screen.getByRole("menuitem", { name: "Hand over to…" }).click();
    });
    await act(async () => {
      screen.getByRole("button", { name: "Hand over" }).click();
    });
    await flush();
    expect(h.toast).toHaveBeenCalledWith("hand over needs both sessions in one channel", { bad: true });
  });
});
