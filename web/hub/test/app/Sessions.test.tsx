// Sessions.test.tsx — the Sessions list: the groups and their counts from a fixture, the state
// marks as shape plus word, a registry.session row moving a session between groups live, the
// group-by toggle, the needs-you card, the working and idle rows, the desktop keys (j/k move,
// Enter opens, L links a pick of two), the pick bar's link sheet and the picks it clears, and
// session text always rendered as text, never HTML.
import { act, cleanup, screen, within } from "@testing-library/preact";
import type { ComponentChildren, JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import { describe, expect, it, vi } from "vite-plus/test";
import type { Api } from "../../src/api.ts";
import type { Layout } from "../../src/app/layout.ts";
import { StateMark } from "../../src/app/session-parts.tsx";
import { SessionsPane } from "../../src/app/Sessions.tsx";
import type { NeedItem, RegistrySession, RegistryStore } from "../../src/registry.ts";
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
  [...document.querySelectorAll("[data-sessions-sections] h2")].map((h) => h.textContent ?? "");

/** The list mounted over a registry the test fills; the document is clean first. */
function mountNow(
  layout: Layout = "phone",
  sessions: RegistrySession[] | null = fixture(),
  needs: NeedItem[] | null = null,
) {
  cleanup();
  const registry = makeRegistry({ sessions, needs });
  return { registry, ...followRender(() => <SessionsPane layout={layout} />, registry) };
}

/** One keystroke at the window, the way the key layer hears it. */
const press = (key: string): void => {
  void act(() => {
    window.dispatchEvent(new KeyboardEvent("keydown", { key }));
  });
};

describe("the Sessions list", () => {
  it("groups the sessions and counts each group, hiding the empty ones", () => {
    stubMedia({ "(min-width: 640px)": false, "(min-width: 1024px)": false });
    mountNow("phone");
    expect(headings()).toEqual(["Needs you · 2", "Working · 3", "Idle · 2", "Stuck · 1", "Ended · 1"]);
    cleanup();
    const registry = makeRegistry({
      sessions: [row({ session_id: "s-1", title: "only" })],
    });
    followRender(() => <SessionsPane layout="phone" />, registry);
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
    expect(location.hash).toBe("#/sessions/s-need-1");
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
    expect(within(card).getByRole("link", { name: "Review" }).getAttribute("href")).toBe(
      "#/sessions/s-need-1",
    );
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

  it("leads with Needs you and sets the checklist after it when asks exist, and leads with the checklist when none do", () => {
    stubMedia({ "(min-width: 640px)": false, "(min-width: 1024px)": false });
    localStorage.clear();
    /** The list's top-level blocks in order: the checklist stand-in, then each section's heading. */
    const order = (): string[] =>
      [...document.querySelectorAll("[data-checklist], [data-sessions-sections] h2")].map((el) =>
        el.hasAttribute("data-checklist") ? "checklist" : (el.textContent ?? ""),
      );
    const checklist = <div data-checklist="">Get set up</div>;
    const mountWith = (sessions: RegistrySession[]): void => {
      cleanup();
      followRender(() => <SessionsPane layout="phone" above={checklist} />, makeRegistry({ sessions }));
    };
    mountWith(fixture());
    expect(order()).toEqual([
      "Needs you · 2",
      "checklist",
      "Working · 3",
      "Idle · 2",
      "Stuck · 1",
      "Ended · 1",
    ]);
    mountWith(fixture().filter((s) => s.state !== "needs-you"));
    expect(order()).toEqual(["checklist", "Working · 3", "Idle · 2", "Stuck · 1", "Ended · 1"]);
  });

  it("names every session's harness on its row and its card, Claude Code included", () => {
    stubMedia({ "(min-width: 640px)": false, "(min-width: 1024px)": false });
    mountNow("phone", [
      row({ session_id: "s-cc", title: "lead", machine: "studio", harness: "claude", task: "Release" }),
      row({ session_id: "s-cx", title: "board-fix", machine: "studio", harness: "codex", task: "Fix" }),
      row({ session_id: "s-idle-cc", state: "idle", title: "notes", harness: "claude", last_event: "Stop" }),
      row({ session_id: "s-idle-cx", state: "idle", title: "docs", harness: "codex", last_event: "Stop" }),
      row({ session_id: "s-need-cc", state: "needs-you", title: "gateway", harness: "claude", task: "Asks" }),
      row({ session_id: "s-need-cx", state: "needs-you", title: "api", harness: "codex", task: "Asks" }),
      // a report from before the hook named its harness is a Claude Code one
      row({ session_id: "s-old", title: "older", machine: "studio", task: "Older report" }),
    ]);
    for (const [id, harness, name] of [
      ["s-cc", "claude", "Claude Code"],
      ["s-cx", "codex", "Codex"],
      ["s-idle-cc", "claude", "Claude Code"],
      ["s-idle-cx", "codex", "Codex"],
      ["s-need-cc", "claude", "Claude Code"],
      ["s-need-cx", "codex", "Codex"],
      ["s-old", "claude", "Claude Code"],
    ] as const) {
      const chip = document.querySelector(`[data-row="${id}"] [data-harness]`) as HTMLElement | null;
      expect(chip, id).not.toBeNull();
      expect(chip?.dataset.harness, id).toBe(harness);
      // the chip names the agent in words beside its mark, so the mark never carries it alone
      expect(chip?.textContent, id).toContain(name);
      expect(chip?.querySelector("[data-harness-mark]"), id).not.toBeNull();
    }
    expect(legacyClasses(document.body, "[data-needs-bar], [data-needs-sheet]")).toEqual([]);
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
    expect(location.hash).toBe("#/sessions/s-work-1");
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
    expect(location.hash).toBe("#/sessions/s-work-1");
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
    expect(document.querySelector("[data-sessions-sections] img")).toBeNull();
    expect(document.querySelector("[data-sessions-sections] b")).toBeNull();
  });
});

describe("the small pieces", () => {
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

/** Mounts the Sessions list over a recording api, repainting on every registry change; the toast
 *  is a spy the link flows assert on. */
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
    return <SessionsPane layout={layout} />;
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
