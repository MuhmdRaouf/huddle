// Now.test.tsx — the Now list and the Session screen: the groups and their counts from a
// fixture, the state marks as shape plus word, a registry.session row moving a session between
// groups live, the group-by toggle, the needs-you card, the working and idle rows, the session
// screen's header, chips, context bar and event stream, the desktop context pane, the desktop
// keys (j/k move, Enter opens, L links a pick of two), the pick bar's link sheet and the picks
// it clears, the linked session's brief and hand over with their step-up, and session text
// always rendered as text, never HTML.
import { act, cleanup, screen, within } from "@testing-library/preact";
import type { ComponentChildren, JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import { describe, expect, it, vi } from "vite-plus/test";
import type { Api } from "../../src/api.ts";
import type { Layout } from "../../src/app/layout.ts";
import { ContextBar, ModelChip, NowContext, NowPane, SessionScreen, StateMark } from "../../src/app/Now.tsx";
import type {
  NeedItem,
  RegistryDetail,
  RegistryEvent,
  RegistrySession,
  RegistryStore,
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
function followRender(make: () => ComponentChildren, registry: RegistryStore): ReturnType<typeof renderIn> {
  function Follow(): JSX.Element {
    const [, set] = useState(0);
    useEffect(() => registry.subscribe(() => set((n) => n + 1)), [registry]);
    return <>{make()}</>;
  }
  return renderIn(<Follow />, makeCtx(makeState(), { registry }));
}

/** The headings of the list's sections, as text. */
const headings = (): string[] =>
  [...document.querySelectorAll("[data-now-sections] h2")].map((h) => h.textContent ?? "");

/** The list mounted over a registry the test fills; the document is clean first. */
function mountNow(
  layout: Layout = "phone",
  sessions: RegistrySession[] | null = fixture(),
  needs: NeedItem[] | null = null,
) {
  cleanup();
  const registry = makeRegistry({ sessions, needs });
  return { registry, ...followRender(() => <NowPane layout={layout} />, registry) };
}

/** One keystroke at the window, the way the key layer hears it. */
const press = (key: string): void => {
  void act(() => {
    window.dispatchEvent(new KeyboardEvent("keydown", { key }));
  });
};

describe("the Now list", () => {
  it("groups the sessions and counts each group, hiding the empty ones", () => {
    stubMedia({ "(min-width: 640px)": false, "(min-width: 1024px)": false });
    mountNow("phone");
    expect(headings()).toEqual(["Needs you · 2", "Working · 3", "Idle · 2", "Stuck · 1", "Ended · 1"]);
    cleanup();
    const registry = makeRegistry({
      sessions: [row({ session_id: "s-1", title: "only" })],
    });
    followRender(() => <NowPane layout="phone" />, registry);
    expect(headings()).toEqual(["Working · 1"]);
  });

  it("shows a skeleton before the first read, the start-a-session card when nothing is registered, and the dashed box only behind the filter", () => {
    stubMedia({ "(min-width: 640px)": false, "(min-width: 1024px)": false });
    localStorage.clear(); // the filter's preference must not leak from this test
    mountNow("phone", null);
    expect(screen.getByRole("status", { name: "Reading the registry" })).toBeDefined();
    mountNow("phone", []);
    expect(screen.getByText("No sessions yet")).toBeDefined();
    expect(
      screen.getByText(
        "Bring a coding agent in: install, join, and it reports here with everything it does.",
      ),
    ).toBeDefined();
    expect(screen.getByRole("tab", { name: "Claude Code" })).toBeDefined();
    // the wide layouts carry the welcome in the main pane instead: the list drops its copy, so
    // the start-a-session card never shows twice
    mountNow("desktop", []);
    expect(screen.queryByText("No sessions yet")).toBeNull();
    expect(screen.queryByRole("tab", { name: "Claude Code" })).toBeNull();
    // the needs-you filter alone keeps the dashed box: sessions exist, none needs the owner
    mountNow("phone", [row({ session_id: "s-1", title: "only" })]);
    void act(() => {
      screen.getByRole("button", { name: "Needs you" }).click();
    });
    expect(screen.getByText("Nothing needs you")).toBeDefined();
    expect(screen.getByText("When a session needs you, it shows up here first.")).toBeDefined();
    // back off, so the preference this test flipped leaves nothing behind
    void act(() => {
      screen.getByRole("button", { name: "Needs you" }).click();
    });
    expect(screen.getByRole("heading", { name: "Working · 1" })).toBeDefined();
  });

  it("gives every state mark a shape and a word, never colour alone", () => {
    for (const [state, shape, word] of [
      ["needs-you", "ring", "needs you"],
      ["working", "dot", "working"],
      ["idle", "hollow", "idle"],
      ["stuck", "dash", "stuck"],
      ["ended", "cross", "ended"],
    ] as const) {
      const mark = renderIn(<StateMark state={state} />);
      const el = mark.getByRole("img");
      expect(el.getAttribute("aria-label"), state).toBe(word);
      expect(el.dataset.shape, state).toBe(shape);
      mark.unmount();
    }
  });

  it("draws the needs-you card: name, age, where, the summary, the ask, Deny beside Allow once, and the actions menu", () => {
    stubMedia({ "(min-width: 640px)": false, "(min-width: 1024px)": false });
    mountNow("phone", fixture(), [needItem()]);
    const card = screen.getByText("kanban-e2e").closest("article");
    expect(card).not.toBeNull();
    expect(within(card as HTMLElement).getByText("30 s")).toBeDefined();
    expect(
      within(card as HTMLElement).getByText("laptop · claude-code-plugins · port/KBN-E2E"),
    ).toBeDefined();
    expect(within(card as HTMLElement).getByText("Run the board export e2e")).toBeDefined();
    // the ask names its tool and the input it would run
    expect(within(card as HTMLElement).getByText("npm test -- --run e2e")).toBeDefined();
    // Deny and Allow once answer the session's open permission ask; the name opens the session
    expect(within(card as HTMLElement).getByRole("button", { name: "Deny" })).toBeDefined();
    expect(within(card as HTMLElement).getByRole("button", { name: "Allow once" })).toBeDefined();
    expect(within(card as HTMLElement).getByRole("button", { name: "More actions" })).toBeDefined();
    void act(() => {
      screen.getByText("kanban-e2e").closest("a")?.click();
    });
    expect(location.hash).toBe("#/now/s-need-1");
  });

  it("offers Review alone when the session has no permission ask to answer", () => {
    stubMedia({ "(min-width: 640px)": false, "(min-width: 1024px)": false });
    mountNow("phone", fixture(), [
      {
        id: 8,
        session: "s-need-1",
        kind: "question",
        summary: "Keep the timeout?",
        created_at: "2026-10-08T11:59:30Z",
        state: "open",
      },
    ]);
    expect(document.querySelector('[data-act="deny"]')).toBeNull();
    const card = document.querySelector('[data-row="s-need-1"]') as HTMLElement;
    expect(within(card).getByRole("link", { name: "Review" }).getAttribute("href")).toBe("#/now/s-need-1");
  });

  it("draws the working row: mark, name, model chip, machine · task, context", () => {
    stubMedia({ "(min-width: 640px)": false, "(min-width: 1024px)": false });
    mountNow("phone");
    const name = screen.getByText("lead").closest("[data-row]");
    expect(name).not.toBeNull();
    const rowEl = name as HTMLElement;
    expect(within(rowEl).getByLabelText("working")).toBeDefined();
    expect(within(rowEl).getByText("opus-4-6")).toBeDefined();
    expect(within(rowEl).getByText("studio · Release kanban 0.0.1")).toBeDefined();
    expect(within(rowEl).getByText("41%")).toBeDefined();
  });

  it("carries the state as a word on every row, never colour alone", () => {
    stubMedia({ "(min-width: 640px)": false, "(min-width: 1024px)": false });
    mountNow("phone");
    for (const [title, word, state] of [
      ["lead", "working", "working"],
      ["docs-sweep", "idle", "idle"],
      ["quiet-one", "stuck", "stuck"],
      ["finished", "ended", "ended"],
      ["kanban-e2e", "needs you", "needs-you"],
    ] as const) {
      const rowEl = screen.getByText(title).closest("[data-row]") as HTMLElement;
      const chip = within(rowEl).getByText(word);
      expect(chip.getAttribute("data-state"), title).toBe(state);
    }
  });

  it("filters the list down to the sessions that need the owner, and keeps the choice", () => {
    stubMedia({ "(min-width: 640px)": false, "(min-width: 1024px)": false });
    localStorage.clear();
    mountNow("phone");
    void act(() => {
      screen.getByRole("button", { name: "Needs you" }).click();
    });
    expect(headings()).toEqual(["Needs you · 2"]);
    expect(localStorage.getItem("huddle:nowneeds")).toBe("true");
    expect(screen.getByText("kanban-e2e")).toBeDefined();
    expect(screen.queryByText("lead")).toBeNull();
    void act(() => {
      screen.getByRole("button", { name: "Needs you" }).click();
    });
    expect(headings()).toEqual(["Needs you · 2", "Working · 3", "Idle · 2", "Stuck · 1", "Ended · 1"]);
    expect(localStorage.getItem("huddle:nowneeds")).toBe("false");
  });

  it("badges the harness a non-Claude session runs in, and none on a Claude one", () => {
    stubMedia({ "(min-width: 640px)": false, "(min-width: 1024px)": false });
    mountNow("phone", [
      row({
        session_id: "s-oc",
        title: "board-fix",
        machine: "studio",
        harness: "opencode",
        task: "Fix the board",
      }),
      row({ session_id: "s-cc", title: "lead", machine: "studio", task: "Release kanban 0.0.1" }),
      row({
        session_id: "s-idle-oc",
        state: "idle",
        title: "notes",
        machine: "studio",
        harness: "crush",
        last_event: "Stop",
      }),
      row({
        session_id: "s-need-oc",
        state: "needs-you",
        title: "gateway",
        machine: "studio",
        harness: "codex",
        task: "Asks: keep the timeout?",
      }),
    ]);
    for (const id of ["s-oc", "s-idle-oc", "s-need-oc"]) {
      const rowEl = document.querySelector(`[data-row="${id}"]`);
      expect(rowEl).not.toBeNull();
      expect(rowEl?.querySelector("[data-harness]")).not.toBeNull();
    }
    const claude = document.querySelector('[data-row="s-cc"]');
    expect(claude?.querySelector("[data-harness]")).toBeNull();
    // the badge names the agent in words, so colour never carries it alone
    expect(screen.getByText("opencode")).toBeDefined();
    expect(screen.getByText("crush")).toBeDefined();
    expect(screen.getByText("codex")).toBeDefined();
  });

  it("draws the idle row: hollow mark, name, machine, age", () => {
    stubMedia({ "(min-width: 640px)": false, "(min-width: 1024px)": false });
    mountNow("phone");
    const idle = screen.getByText("docs-sweep").closest("[data-row]");
    expect(idle).not.toBeNull();
    expect(within(idle as HTMLElement).getByLabelText("idle")).toBeDefined();
    expect(within(idle as HTMLElement).getByText(/· studio/)).toBeDefined();
    expect(within(idle as HTMLElement).getByText("12 min")).toBeDefined();
  });

  it("chips a session whose hooks stopped reporting on its working and its idle row, and no other", () => {
    stubMedia({ "(min-width: 640px)": false, "(min-width: 1024px)": false });
    mountNow("phone", [
      row({ session_id: "s-quiet", title: "quiet-hooks", hooks_stale: true }),
      row({ session_id: "s-rest", title: "resting", state: "idle", last_event: "Stop", hooks_stale: true }),
      row({ session_id: "s-fine", title: "reporting" }),
    ]);
    for (const name of ["quiet-hooks", "resting"]) {
      const r = screen.getByText(name).closest("[data-row]") as HTMLElement;
      const chip = r.querySelector("[data-hooks-stale]") as HTMLElement;
      expect(chip?.textContent, name).toBe("hooks stale");
      expect(chip.className, name).toContain("text-[var(--ui-warn-ink)]");
    }
    const fine = screen.getByText("reporting").closest("[data-row]") as HTMLElement;
    expect(fine.querySelector("[data-hooks-stale]")).toBeNull();
  });

  it("paints the list on the tokens, with no daisyUI or legacy class", async () => {
    // the parts other pages lend the list (the machines line, the snooze menu, the dialogs' frame)
    // answer to their own cards
    const lent = "[data-machines], [data-needs-actions] > span, dialog";
    for (const layout of ["phone", "tablet", "desktop"] as const) {
      stubMedia({
        "(min-width: 640px)": layout !== "phone",
        "(min-width: 1024px)": layout === "desktop",
      });
      mountNow(layout, fixture(), [needItem()]);
      expect(legacyClasses(document.body, lent), layout).toEqual([]);
    }
    // the desktop's picks open the pick bar, the filter its pressed state
    await act(async () => {
      screen.getByRole("checkbox", { name: "Select kanban-e2e" }).click();
    });
    await act(async () => {
      screen.getByRole("checkbox", { name: "Select lead" }).click();
    });
    expect(document.querySelector("[data-select-bar]")).not.toBeNull();
    expect(legacyClasses(document.body, lent)).toEqual([]);
    const filter = document.querySelector("[data-needs-filter]") as HTMLElement;
    await act(async () => {
      filter.click();
    });
    expect(filter.getAttribute("aria-pressed")).toBe("true");
    expect(legacyClasses(document.body, lent)).toEqual([]);
    // the filter keeps its choice: give the other cases their whole list back
    await act(async () => {
      filter.click();
    });
  });

  it("colours each row's mark and chip with its state's role token", () => {
    stubMedia({ "(min-width: 640px)": false, "(min-width: 1024px)": false });
    mountNow("phone", fixture(), [
      needItem(),
      {
        id: 9,
        session: "s-need-2",
        kind: "turn",
        summary: "The retry budget is in",
        created_at: "2026-10-08T11:59:30Z",
        state: "open",
      },
    ]);
    for (const [title, role] of [
      ["kanban-e2e", "needs-you"],
      ["api-gateway", "turn-done"],
      ["lead", "working"],
      ["docs-sweep", "idle"],
      ["quiet-one", "failed"],
      ["finished", "cancelled"],
    ] as const) {
      const rowEl = screen.getByText(title).closest("[data-row]") as HTMLElement;
      const chip = rowEl.querySelector("[data-state]") as HTMLElement;
      expect(chip.dataset.role, title).toBe(role);
      expect(chip.className, title).toContain(`text-[var(--ui-${role}-ink)]`);
      expect(chip.className, title).toContain(`bg-[var(--ui-${role}-soft)]`);
      // the needs-you card carries its chip alone; every other row leads with its mark
      const mark = rowEl.querySelector("svg[data-shape]");
      if (rowEl.tagName !== "ARTICLE") {
        expect(mark?.getAttribute("class"), title).toContain(`text-[var(--ui-${role})]`);
      }
    }
  });

  it("reads a session with an open item as Needs you, not idle", () => {
    stubMedia({ "(min-width: 640px)": false, "(min-width: 1024px)": false });
    mountNow("phone", fixture(), [
      {
        id: 10,
        session: "s-idle-1",
        kind: "question",
        summary: "Ship the docs?",
        created_at: "2026-10-08T11:59:30Z",
        state: "open",
      },
    ]);
    expect(headings()).toContain("Needs you · 3");
    expect(headings()).toContain("Idle · 1");
    const card = document.querySelector('article[data-row="s-idle-1"]') as HTMLElement;
    expect(card).not.toBeNull();
    const chip = card.querySelector("[data-state]") as HTMLElement;
    expect(chip.textContent).toBe("needs you");
    expect(chip.dataset.role).toBe("needs-you");
  });

  it("moves a row between groups the moment its registry.session row lands", () => {
    stubMedia({ "(min-width: 640px)": false, "(min-width: 1024px)": false });
    const { registry } = mountNow("phone");
    expect(headings()[1]).toBe("Working · 3");
    void act(() => {
      registry.applyRow(row({ session_id: "s-work-1", state: "idle", title: "lead", last_event: "Stop" }));
    });
    expect(headings()).toEqual(["Needs you · 2", "Working · 2", "Idle · 3", "Stuck · 1", "Ended · 1"]);
    // a session the list has never seen arrives without a reload
    void act(() => {
      registry.applyRow(row({ session_id: "s-fresh", title: "fresh" }));
    });
    expect(headings()[1]).toBe("Working · 3");
    expect(screen.getByText("fresh")).toBeDefined();
  });

  it("groups the rows by machine or by project, and keeps the choice", () => {
    stubMedia({ "(min-width: 640px)": false, "(min-width: 1024px)": false });
    localStorage.clear();
    mountNow("phone");
    expect(screen.queryByText("build-box")).toBeNull(); // machine rides each row, not a heading
    void act(() => {
      screen.getByRole("button", { name: "Project" }).click();
    });
    expect(screen.getByText("claude-code-plugins · 3")).toBeDefined(); // the working rows
    expect(screen.getAllByText(/^gateway · \d+$/).length).toBeGreaterThan(0);
    expect(localStorage.getItem("huddle:nowgroup")).toBe('"project"');
    void act(() => {
      screen.getByRole("button", { name: "Machine" }).click();
    });
    expect(screen.getAllByText(/^build-box · \d+$/).length).toBeGreaterThan(0);
    expect(localStorage.getItem("huddle:nowgroup")).toBe('"machine"');
  });

  it("opens a session from a row on the phone", () => {
    stubMedia({ "(min-width: 640px)": false, "(min-width: 1024px)": false });
    mountNow("phone");
    void act(() => {
      screen.getByText("lead").closest("a")?.click();
    });
    expect(location.hash).toBe("#/now/s-work-1");
  });

  it("moves the selection with j and k and opens it with Enter on the desktop", () => {
    stubMedia({ "(min-width: 640px)": true, "(min-width: 1024px)": true });
    localStorage.clear();
    mountNow("desktop");
    const rows = () => [...document.querySelectorAll("[data-row]")] as HTMLElement[];
    expect(rows()[0]?.getAttribute("aria-current")).toBe("true");
    press("j");
    expect(rows()[1]?.getAttribute("aria-current")).toBe("true");
    press("j");
    expect(rows()[2]?.getAttribute("aria-current")).toBe("true");
    press("Enter");
    expect(location.hash).toBe("#/now/s-work-1");
    press("k");
    expect(rows()[1]?.getAttribute("aria-current")).toBe("true");
    // the keys stay out of fields, and a wrapped selection stops at the ends
    const input = document.createElement("input");
    document.body.append(input);
    void act(() => {
      input.focus();
      input.dispatchEvent(new KeyboardEvent("keydown", { key: "j", bubbles: true }));
    });
    expect(rows()[1]?.getAttribute("aria-current")).toBe("true");
    input.remove();
    for (let i = 0; i < 10; i++) press("j");
    expect(rows()[rows().length - 1]?.getAttribute("aria-current")).toBe("true");
  });

  it("renders session text as text, never HTML", () => {
    stubMedia({ "(min-width: 640px)": false, "(min-width: 1024px)": false });
    mountNow("phone", [
      row({ session_id: "s-evil", title: "<img onerror=alert(1) src=x>", task: "<b>bold</b> move" }),
    ]);
    expect(screen.getByText("<img onerror=alert(1) src=x>")).toBeDefined();
    expect(document.querySelector("[data-now-sections] img")).toBeNull();
    expect(document.querySelector("[data-now-sections] b")).toBeNull();
  });
});

describe("the Session screen", () => {
  const mountSession = (
    layout: Layout = "phone",
    over: Partial<RegistryDetail> = {},
    needs: NeedItem[] | null = null,
  ): RegistryStore => {
    stubMedia({
      "(min-width: 640px)": layout !== "phone",
      "(min-width: 1024px)": layout === "desktop",
    });
    const registry = makeRegistry({ sessions: fixture(), open: detail(over), openId: "s-need-1", needs });
    followRender(() => <SessionScreen id="s-need-1" layout={layout} />, registry);
    return registry;
  };

  it("shows the header, the chips and the context bar, and goes back to Now", () => {
    mountSession("phone");
    expect(screen.getByRole("link", { name: "Back to Now" }).getAttribute("href")).toBe("#/now");
    expect(screen.getByText("kanban-e2e")).toBeDefined();
    expect(screen.getByText("laptop · claude-code-plugins · port/KBN-E2E")).toBeDefined();
    expect(screen.getByText("needs you")).toBeDefined();
    expect(screen.getByText("glm-5.3")).toBeDefined();
    expect(screen.getByText("#kanban-release")).toBeDefined();
    expect(screen.getByLabelText("Context 58 percent used")).toBeDefined();
    expect(screen.getByText("58% context")).toBeDefined();
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
      expect(pill.className, state).not.toContain("warning");
    }
  });

  it("reads a session with an open item as Needs you in its header, not idle", () => {
    mountSession("phone", { state: "idle" }, [needItem()]);
    const pill = document.querySelector("[data-session] header [data-state]") as HTMLElement;
    expect(pill.textContent).toBe("needs you");
    expect(pill.dataset.role).toBe("needs-you");
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

  it("follows new events while the reader is at the bottom, and leaves a reader who scrolled up", () => {
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
      more(51);
      expect(stream.scrollTop).toBe(stream.scrollHeight - stream.clientHeight);

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

  it("waits quietly while the detail loads, and says when there is no such session", () => {
    stubMedia({ "(min-width: 640px)": false, "(min-width: 1024px)": false });
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

  it("lays the same facts out for the tablet and the desktop widths", () => {
    for (const layout of ["tablet", "desktop"] as const) {
      cleanup();
      mountSession(layout);
      expect(screen.getByText("kanban-e2e"), layout).toBeDefined();
      expect(screen.getByLabelText("Context 58 percent used"), layout).toBeDefined();
      expect(screen.queryByRole("link", { name: "Back to Now" }), layout).toBeNull();
    }
  });
});

describe("the desktop context pane", () => {
  it("shows the linked sessions and the session's facts", () => {
    stubMedia({ "(min-width: 640px)": true, "(min-width: 1024px)": true });
    const registry = makeRegistry({ sessions: fixture(), open: detail() });
    followRender(() => <NowContext />, registry);
    expect(screen.getByText("Linked in #kanban-release")).toBeDefined();
    const linked = screen.getByText("lead").closest("a");
    expect(within(linked as HTMLElement).getByLabelText("working")).toBeDefined();
    expect(screen.getByText("Session")).toBeDefined();
    expect(screen.getByText("58% used")).toBeDefined();
    expect(screen.getByText(/\d\d:\d\d · 12 min/)).toBeDefined();
  });

  it("hides the linked list when the session is in no channel", () => {
    stubMedia({ "(min-width: 640px)": true, "(min-width: 1024px)": true });
    const registry = makeRegistry({ sessions: [], open: detail({ channel: "" }) });
    followRender(() => <NowContext />, registry);
    expect(screen.queryByText(/Linked in/)).toBeNull();
    expect(screen.getByText("Session")).toBeDefined();
  });
});

describe("the small pieces", () => {
  it("colors a model chip by its family and titles the raw id", () => {
    const chip = renderIn(<ModelChip model="claude-opus-4-6" />);
    const el = chip.getByText("opus-4-6"); // the chip span holds only its short name
    expect(el.getAttribute("title")).toBe("claude-opus-4-6");
    expect(el.getAttribute("style")).toContain("var(--model-opus)");
    chip.unmount();
  });

  it("fills the context bar to its percent and hides it at zero", () => {
    const bar = renderIn(<ContextBar pct={58} />);
    expect(bar.getByRole("img").getAttribute("aria-label")).toBe("Context 58 percent used");
    expect((bar.getByRole("img").firstElementChild as HTMLElement).style.width).toBe("58%");
    bar.unmount();
    const empty = renderIn(<ContextBar pct={0} />);
    expect(empty.container.firstElementChild).toBeNull();
    empty.unmount();
  });

  it("draws an unknown state as the hollow mark, and the header's pill chip", () => {
    const mark = renderIn(<StateMark state="confused" />);
    expect(mark.getByRole("img").getAttribute("aria-label")).toBe("confused");
    expect(mark.getByRole("img").dataset.shape).toBe("hollow");
    mark.unmount();
    const chip = renderIn(<ModelChip model="glm-5.3" pill />);
    const pill = chip.getByText("glm-5.3");
    expect(pill.className).toContain("rounded-full");
    // the pill carries the family's colour as a leading dot, never a border on a round chip
    expect(pill.getAttribute("style") ?? "").not.toContain("border-left");
    expect((pill.querySelector("[data-model-dot]") as HTMLElement).getAttribute("style")).toContain(
      "background",
    );
    expect(legacyClasses(chip.container)).toEqual([]);
    chip.unmount();
  });

  it("does not hear the keys through a dialog", () => {
    stubMedia({ "(min-width: 640px)": true, "(min-width: 1024px)": true });
    localStorage.clear();
    mountNow("desktop");
    const dlg = document.createElement("dialog");
    dlg.open = true;
    document.body.append(dlg);
    const first = [...document.querySelectorAll("[data-row]")][0] as HTMLElement;
    press("j");
    expect(first.getAttribute("aria-current")).toBe("true");
    dlg.remove();
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
    // the list's Radar card reads Radar's own routes; they are no act of the list's
    if (path.startsWith("/api/radar/")) return Promise.resolve({});
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

/** Mounts the Now list over a recording api, repainting on every registry change; the toast is
 *  a spy the link flows assert on. */
function mountNowOver(
  layout: Layout,
  answer?: (path: string, n: number) => unknown,
): {
  registry: RegistryStore;
  calls: { path: string; body: unknown }[];
  toast: ReturnType<typeof vi.fn>;
} {
  cleanup();
  stubMedia({
    "(min-width: 640px)": layout !== "phone",
    "(min-width: 1024px)": layout === "desktop",
  });
  const { api, calls } = apiFake(answer);
  const toast = vi.fn();
  const registry = makeRegistry({ sessions: fixture() });
  const ctx = makeCtx(makeState(), { registry, api, toast });
  function Follow(): JSX.Element {
    const [, set] = useState(0);
    useEffect(() => registry.subscribe(() => set((n) => n + 1)), [registry]);
    return <NowPane layout={layout} />;
  }
  renderIn(<Follow />, ctx);
  return { registry, calls, toast };
}

describe("the pick bar's link", () => {
  it("TestTheLinkButtonShowsFromTwoPicksAndOpensTheSheetWithThem", async () => {
    mountNowOver("desktop");
    await act(async () => {
      screen.getByRole("checkbox", { name: "Select kanban-e2e" }).click();
    });
    expect(screen.queryByRole("button", { name: "Link 1 session" })).toBeNull();
    expect(screen.queryByRole("button", { name: /^Link 1 session/ })).toBeNull();
    await act(async () => {
      screen.getByRole("checkbox", { name: "Select lead" }).click();
    });
    // the link stands before the send in the bar
    const bar = document.querySelector("[data-select-bar]") as HTMLElement;
    const names = [...bar.querySelectorAll("button")].map((b) => b.textContent ?? "");
    expect(names.indexOf("Link 2 sessions")).toBeGreaterThanOrEqual(0);
    expect(names.indexOf("Link 2 sessions")).toBeLessThan(names.findIndex((t) => t.startsWith("Send to")));
    await act(async () => {
      screen.getByRole("button", { name: "Link 2 sessions" }).click();
    });
    expect(screen.getByRole("heading", { name: "Link 2 sessions" })).toBeDefined();
  });

  it("TestALinkClearsThePicksAndToastsTheChannel", async () => {
    const h = mountNowOver("desktop", (path) => (path === "/api/links" ? { channel: "fresh-link" } : {}));
    await act(async () => {
      screen.getByRole("checkbox", { name: "Select kanban-e2e" }).click();
      screen.getByRole("checkbox", { name: "Select lead" }).click();
    });
    await act(async () => {
      screen.getByRole("button", { name: "Link 2 sessions" }).click();
    });
    const name = screen.getByLabelText("Channel name") as HTMLInputElement;
    await act(async () => {
      name.value = "fresh-link";
      name.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => {
      document.querySelector<HTMLButtonElement>("[data-link-submit]")?.click();
    });
    await flush();
    expect(h.calls[0]).toEqual({
      path: "/api/links",
      body: { sessions: ["s-need-1", "s-work-1"], channel: "fresh-link", create: true, level: "digest" },
    });
    expect(h.toast).toHaveBeenCalledWith("Linked 2 sessions into #fresh-link");
    // the link is done with the pick: the bar is gone
    expect(screen.queryByText("2 selected")).toBeNull();
  });

  it("TestTheDesktopKeyLOpensTheSheetForAPickOfTwoOnly", async () => {
    mountNowOver("desktop");
    await act(async () => {
      screen.getByRole("checkbox", { name: "Select kanban-e2e" }).click();
    });
    press("L");
    expect(screen.queryByRole("heading", { name: "Link 1 session" })).toBeNull();
    await act(async () => {
      screen.getByRole("checkbox", { name: "Select lead" }).click();
    });
    press("L");
    expect(screen.getByRole("heading", { name: "Link 2 sessions" })).toBeDefined();
  });
});

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
    stubMedia({ "(min-width: 640px)": false, "(min-width: 1024px)": false });
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
    const h = mountActions((path, n) => {
      if (path === "/api/links") return kanbanLinks;
      if (path === "/api/handover") {
        return n === 2
          ? { moved: ["t-1", "t-2"] }
          : Object.assign(new Error("hand over needs both sessions in one channel"), { status: 409 });
      }
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
