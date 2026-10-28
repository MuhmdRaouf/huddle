// Sessions.test.tsx — the Sessions list: the groups and their counts from a fixture, the state
// chips as words on their role's tint, a registry.session row moving a session between groups
// live, the search box and the machine chips, the set-up row that opens the checklist in a sheet,
// the needs-you card, the desk's two-line rows and the phone's avatar rows, the desktop
// keys (j/k move, Enter opens, L links a pick of two), the pick bar's link sheet and the picks
// it clears, the Select toggle that gates the desktop's checkboxes, and session text always
// rendered as text, never HTML.
import { act, cleanup, screen, within } from "@testing-library/preact";
import type { ComponentChildren, JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import { describe, expect, it, vi } from "vite-plus/test";
import type { Api } from "../../src/api.ts";
import type { HuddleContextValue } from "../../src/app/context.tsx";
import type { Layout } from "../../src/app/layout.ts";
import { StateMark, whereOf } from "../../src/app/session-parts.tsx";
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
function followRender(
  make: () => ComponentChildren,
  registry: RegistryStore,
  ctx: HuddleContextValue = makeCtx(makeState(), { registry }),
): ReturnType<typeof renderIn> {
  function Follow(): JSX.Element {
    const [, set] = useState(0);
    useEffect(() => registry.subscribe(() => set((n) => n + 1)), [registry]);
    return <>{make()}</>;
  }
  return renderIn(<Follow />, ctx);
}

/** The headings of the list's sections, as text. */
const headings = (): string[] =>
  [...document.querySelectorAll("[data-sessions-sections] h2")].map((h) => h.textContent ?? "");

/** The list mounted over a registry the test fills; the document is clean first. */
function mountNow(
  layout: Layout = "phone",
  sessions: RegistrySession[] | null = fixture(),
  needs: NeedItem[] | null = null,
  openId: string | null = null,
) {
  cleanup();
  const registry = makeRegistry({ sessions, needs });
  return {
    registry,
    ...followRender(() => <SessionsPane layout={layout} openId={openId ?? undefined} />, registry),
  };
}

/** The list over a context shaped for the set-up row's tests: whether this browser holds TOTP,
 *  whether a channel exists, what the budgets api answers. The fixture's nine sessions keep the
 *  checklist's session step done, so the row's count moves with the rest. */
function mountSetup(
  over: { layout?: Layout; totp?: boolean; channels?: { name: string }[]; budgets?: unknown[] } = {},
): ReturnType<typeof followRender> {
  cleanup();
  const api: Api = {
    api: (path) => Promise.resolve(path === "/api/radar/api/budgets" ? { budgets: over.budgets ?? [] } : {}),
    op: () => Promise.resolve({}),
    channelPath: (ch, p) => `/api/c/${ch}${p}`,
    channelHref: (ch, p) => `#/channels/${ch}${p}`,
  };
  const registry = makeRegistry({ sessions: fixture() });
  const ctx = makeCtx(makeState({ channels: over.channels ?? null }), {
    registry,
    api,
    who: over.totp ? { kind: "owner", totp: true } : { kind: "owner" },
  });
  return followRender(() => <SessionsPane layout={over.layout ?? "phone"} />, registry, ctx);
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

  it("shows a skeleton before the first read and the start-a-session card when nothing is registered", () => {
    stubMedia({ "(min-width: 640px)": false, "(min-width: 1024px)": false });
    localStorage.clear(); // the machine chips' preference must not leak from this test
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
      within(card as HTMLElement).getByText("claude-code-plugins · port/KBN-E2E · laptop"),
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

  it("on the desk a needs-you row is one plain link to the session, and the open row is marked", () => {
    stubMedia({ "(min-width: 640px)": true, "(min-width: 1024px)": true });
    mountNow("desktop", fixture(), [needItem()], "s-need-1");
    const deskRow = document.querySelector('a[data-row="s-need-1"]') as HTMLAnchorElement;
    expect(deskRow.getAttribute("href")).toBe("#/sessions/s-need-1");
    expect(deskRow.getAttribute("aria-current")).toBe("page");
    // the row opens the session; its needs bar answers the ask, so no action lives on the row
    expect(deskRow.querySelector('[data-act="allow"]')).toBeNull();
    expect(deskRow.querySelector('[data-act="deny"]')).toBeNull();
    expect(deskRow.querySelector('[data-act="review"]')).toBeNull();
  });

  it("on the phone a permission ask still answers Allow and Deny at the card", () => {
    stubMedia({ "(min-width: 640px)": false, "(min-width: 1024px)": false });
    mountNow("phone", fixture(), [needItem()]);
    const card = document.querySelector('[data-row="s-need-1"]') as HTMLElement;
    expect(within(card).getByRole("button", { name: "Deny" })).toBeDefined();
    expect(within(card).getByRole("button", { name: "Allow once" })).toBeDefined();
  });

  it("splits a desk row into a title line and a meta line", () => {
    stubMedia({ "(min-width: 640px)": true, "(min-width: 1024px)": true });
    mountNow("desktop");
    const rowEl = document.querySelector('a[data-row="s-work-1"]') as HTMLElement;
    expect(rowEl).not.toBeNull();
    const lines = [...rowEl.children].filter((el) => el.tagName === "SPAN") as HTMLElement[];
    expect(lines.length, rowEl.outerHTML).toBe(2);
    // the name's line carries the harness chip and no state
    expect(lines[0]?.querySelector("[data-harness]"), "line 1 harness").not.toBeNull();
    expect(lines[0]?.textContent, "line 1 name").toContain("lead");
    expect(lines[0]?.querySelector("[data-state]"), "line 1 state").toBeNull();
    // the meta line carries the where and the state, and no harness
    expect(lines[1]?.querySelector("[data-state]"), "line 2 state").not.toBeNull();
    expect(lines[1]?.querySelector("[data-harness]"), "line 2 harness").toBeNull();
    expect(lines[1]?.textContent, "line 2 where").toContain("claude-code-plugins · studio");
  });

  it("keeps the set-up row's text at the left edge", () => {
    mountSetup({ totp: true, channels: [{ name: "shop" }] });
    const btn = screen.getByRole("button", { name: "Set up · 1 step left" });
    expect(btn.className, "no centring").not.toMatch(/text-center|justify-center/);
    expect(btn.className, "left-aligned text").toContain("text-left");
  });

  it("draws the working row: harness chip, where, state chip, no model chip", () => {
    stubMedia({ "(min-width: 640px)": false, "(min-width: 1024px)": false });
    mountNow("phone");
    const rowEl = screen.getByText("lead").closest("[data-row]") as HTMLElement;
    const harness = rowEl.querySelector("[data-harness]") as HTMLElement;
    expect(harness.dataset.harness).toBe("claude");
    expect(harness.textContent).toContain("Claude Code");
    // the where reads project · machine, the meta line the mock draws
    const meta = rowEl.querySelector(".ui-list-meta") as HTMLElement;
    expect(meta.textContent, "the where").toContain("claude-code-plugins · studio");
    const state = rowEl.querySelector('[data-state="working"]') as HTMLElement;
    expect(state.dataset.role).toBe("working");
    expect(state.textContent).toContain("Working");
    // the phone row carries no model chip and no context bar
    expect(rowEl.textContent).not.toContain("opus-4-6");
    expect(rowEl.textContent).not.toContain("41%");
  });

  it("carries the state as a word on every row, never colour alone", () => {
    stubMedia({ "(min-width: 640px)": false, "(min-width: 1024px)": false });
    mountNow("phone");
    for (const [title, word, state] of [
      ["lead", "Working", "working"],
      ["docs-sweep", "Idle", "idle"],
      ["quiet-one", "Stuck", "stuck"],
      ["finished", "Ended", "ended"],
      // the phone's needs-you row is the card, whose chip keeps the plain word
      ["kanban-e2e", "needs you", "needs-you"],
    ] as const) {
      const rowEl = screen.getByText(title).closest("[data-row]") as HTMLElement;
      const chip = within(rowEl).getByText(word);
      expect(chip.getAttribute("data-state"), title).toBe(state);
    }
  });

  it("writes the row chip's word the mock's way: a capital first letter, the raw state on data-state", () => {
    stubMedia({ "(min-width: 640px)": true, "(min-width: 1024px)": true });
    mountNow("desktop");
    for (const [id, word, state] of [
      ["s-need-1", "Needs you", "needs-you"],
      ["s-work-1", "Working", "working"],
      ["s-idle-1", "Idle", "idle"],
      ["s-stuck", "Stuck", "stuck"],
      ["s-done", "Ended", "ended"],
    ] as const) {
      const chip = document.querySelector(`[data-row="${id}"] [data-state]`) as HTMLElement;
      expect(chip.textContent, id).toBe(word);
      expect(chip.getAttribute("data-state"), id).toBe(state);
    }
  });

  it("keeps the search and the machine chips, with no group-by control and no needs filter", () => {
    stubMedia({ "(min-width: 640px)": false, "(min-width: 1024px)": false });
    localStorage.clear();
    mountNow("phone", fixture(), [needItem()]);
    expect(screen.queryByText("Group by")).toBeNull();
    expect(screen.queryByRole("button", { name: "Machine" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Project" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Needs you" })).toBeNull();
    expect(document.querySelector("[data-needs-filter]")).toBeNull();
    expect(screen.getByRole("searchbox", { name: "Search sessions" })).toBeDefined();
    expect(document.querySelector("[data-machine-chips]")).not.toBeNull();
  });

  it("narrows the list from the search box, across name, project, branch, machine and task", () => {
    stubMedia({ "(min-width: 640px)": false, "(min-width: 1024px)": false });
    localStorage.clear();
    mountNow("phone");
    const box = screen.getByRole("searchbox", { name: "Search sessions" }) as HTMLInputElement;
    const type = (q: string): void => {
      void act(() => {
        box.value = q;
        box.dispatchEvent(new Event("input", { bubbles: true }));
      });
    };
    // the title, in any case
    type("LEAD");
    expect(headings()).toEqual(["Working · 1"]);
    expect(screen.getByText("lead")).toBeDefined();
    expect(screen.queryByText("hub-home")).toBeNull();
    // the branch
    type("kbn-e2e");
    expect(headings()).toEqual(["Needs you · 1"]);
    expect(screen.getByText("kanban-e2e")).toBeDefined();
    // the project
    type("gateway");
    expect(headings()).toEqual(["Needs you · 1", "Idle · 1", "Stuck · 1", "Ended · 1"]);
    // the machine
    type("build-box");
    expect(headings()).toEqual(["Needs you · 1", "Working · 1", "Ended · 1"]);
    // the task line
    type("chart");
    expect(headings()).toEqual(["Working · 1"]);
    expect(screen.getByText("radar-ui")).toBeDefined();
    // an unknown word leaves no row and no welcome: the list is only quiet
    type("zzz-no-such-session");
    expect(headings()).toEqual([]);
    expect(screen.queryByText("No sessions yet")).toBeNull();
    // the search is not kept between visits: a fresh mount holds the whole list, box empty
    mountNow("phone");
    const fresh = screen.getByRole("searchbox", { name: "Search sessions" }) as HTMLInputElement;
    expect(fresh.value).toBe("");
    expect(headings()).toEqual(["Needs you · 2", "Working · 3", "Idle · 2", "Stuck · 1", "Ended · 1"]);
  });

  it("filters by machine chip, clears with All, and keeps the pick across a remount", () => {
    stubMedia({ "(min-width: 640px)": false, "(min-width: 1024px)": false });
    localStorage.clear();
    mountNow("phone");
    const chipRow = document.querySelector("[data-machine-chips]") as HTMLElement;
    expect(chipRow).not.toBeNull();
    const chips = (): HTMLButtonElement[] => [
      ...(document.querySelector("[data-machine-chips]") as HTMLElement).querySelectorAll("button"),
    ];
    expect(chips().map((b) => b.textContent)).toEqual(["All", "build-box", "laptop", "studio"]);
    const chip = (name: string): HTMLButtonElement =>
      chips().find((b) => b.textContent === name) as HTMLButtonElement;
    void act(() => {
      chip("laptop").click();
    });
    expect(chip("laptop").getAttribute("aria-pressed")).toBe("true");
    expect(chip("All").getAttribute("aria-pressed")).toBe("false");
    expect(headings()).toEqual(["Needs you · 1", "Working · 1", "Idle · 1"]);
    expect(screen.getByText("kanban-e2e")).toBeDefined();
    expect(screen.queryByText("hub-home")).toBeNull();
    expect(localStorage.getItem("huddle:sessionsmachine")).toBe('"laptop"');
    // the pick survives a remount, read back through storage
    mountNow("phone");
    expect(chip("laptop").getAttribute("aria-pressed")).toBe("true");
    expect(headings()).toEqual(["Needs you · 1", "Working · 1", "Idle · 1"]);
    // All gives the whole list back and keeps that as the choice
    void act(() => {
      chip("All").click();
    });
    expect(chip("laptop").getAttribute("aria-pressed")).toBe("false");
    expect(chip("All").getAttribute("aria-pressed")).toBe("true");
    expect(headings()).toEqual(["Needs you · 2", "Working · 3", "Idle · 2", "Stuck · 1", "Ended · 1"]);
    expect(localStorage.getItem("huddle:sessionsmachine")).toBe('""');
  });

  it("keeps each machine chip a 28 px pill in one inner span, the button the coarse pointer's tap target", () => {
    stubMedia({ "(min-width: 640px)": false, "(min-width: 1024px)": false });
    localStorage.clear();
    mountNow("phone");
    const chips = (): HTMLButtonElement[] => [
      ...(document.querySelector("[data-machine-chips]") as HTMLElement).querySelectorAll("button"),
    ];
    expect(chips().map((b) => [b.getAttribute("data-machine-chip"), b.getAttribute("aria-pressed")])).toEqual(
      [
        ["all", "true"],
        ["build-box", "false"],
        ["laptop", "false"],
        ["studio", "false"],
      ],
    );
    for (const b of chips()) {
      const name = b.textContent ?? "";
      // the pill: one span carries the whole look on every pointer
      expect(b.querySelectorAll("span").length, name).toBe(1);
      const pill = b.querySelector("span") as HTMLElement;
      for (const cls of [
        "h-7",
        "items-center",
        "rounded-[var(--ui-radius-sm)]",
        "px-2.5",
        "text-sm",
        "font-medium",
      ]) {
        expect(pill.classList.contains(cls), `${name}: .${cls}`).toBe(true);
      }
      // the button: the transparent box at least --ui-tap on each side under a coarse pointer
      expect(b.classList.contains("pointer-coarse:min-h-[var(--ui-tap)]!"), name).toBe(true);
      expect(b.classList.contains("pointer-coarse:min-w-[var(--ui-tap)]!"), name).toBe(true);
    }
    // the pill's fill follows the chip's state
    const pillOf = (name: string): HTMLElement =>
      (chips().find((b) => b.textContent === name) as HTMLButtonElement).querySelector("span") as HTMLElement;
    expect(pillOf("All").classList.contains("bg-[var(--ui-text)]")).toBe(true);
    expect(pillOf("All").classList.contains("text-[var(--ui-bg)]")).toBe(true);
    expect(pillOf("laptop").classList.contains("bg-[var(--ui-surface-2)]")).toBe(true);
    expect(pillOf("laptop").classList.contains("text-[var(--ui-muted)]")).toBe(true);
  });

  it("carries the set-up row under the machine chips, its count the checklist's steps left", () => {
    stubMedia({ "(min-width: 640px)": false, "(min-width: 1024px)": false });
    localStorage.clear();
    mountSetup();
    const row = screen.getByRole("button", { name: "Set up · 3 steps left" });
    // the row rides under the machine chips, above every section, and the body holds no checklist
    const chips = document.querySelector("[data-machine-chips]") as HTMLElement;
    const sections = document.querySelector("[data-sessions-sections]") as HTMLElement;
    expect(chips.compareDocumentPosition(row) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(row.compareDocumentPosition(sections) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(sections.querySelector("[data-first-run]")).toBeNull();
    // TOTP on leaves two steps, and a channel too leaves the last one, in the singular
    mountSetup({ totp: true });
    expect(screen.getByRole("button", { name: "Set up · 2 steps left" })).toBeDefined();
    mountSetup({ totp: true, channels: [{ name: "shop" }] });
    expect(screen.getByRole("button", { name: "Set up · 1 step left" })).toBeDefined();
  });

  it("opens the checklist in a sheet from the set-up row, and leaves the row out when it is done or dismissed", async () => {
    stubMedia({ "(min-width: 640px)": false, "(min-width: 1024px)": false });
    localStorage.clear();
    mountSetup({ totp: true, channels: [{ name: "shop" }] });
    expect(document.querySelector("dialog[open]")).toBeNull();
    await act(async () => {
      screen.getByRole("button", { name: "Set up · 1 step left" }).click();
    });
    const sheet = document.querySelector("dialog[open].ui-sheet") as HTMLElement;
    expect(sheet).not.toBeNull();
    expect(within(sheet).getByRole("heading", { name: "Get set up" })).toBeDefined();
    // with the budgets answered every step is done: the row is gone
    mountSetup({ totp: true, channels: [{ name: "shop" }], budgets: [{ name: "cap" }] });
    await flush();
    expect(screen.queryByRole("button", { name: /^Set up · / })).toBeNull();
    // a dismissed checklist hides the row too
    localStorage.setItem("huddle:first-run", "true");
    mountSetup({ totp: true, channels: [{ name: "shop" }] });
    expect(screen.queryByRole("button", { name: /^Set up · / })).toBeNull();
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
      // the chip names the agent in words, so a mark or colour never carries it alone
      expect(chip?.textContent, id).toContain(name);
      // the needs-you card's chip leads with its mark; the list's rows draw the name alone
      const rowEl = document.querySelector(`[data-row="${id}"]`) as HTMLElement;
      const mark = rowEl.querySelector("[data-harness-mark]");
      if (rowEl.tagName === "ARTICLE") {
        expect(mark, id).not.toBeNull();
      } else {
        expect(mark, id).toBeNull();
      }
    }
    expect(legacyClasses(document.body, "[data-needs-bar], [data-needs-sheet]")).toEqual([]);
  });

  it("draws the idle row: name, where, state chip, age", () => {
    stubMedia({ "(min-width: 640px)": false, "(min-width: 1024px)": false });
    mountNow("phone");
    const idle = screen.getByText("docs-sweep").closest("[data-row]") as HTMLElement;
    const state = idle.querySelector('[data-state="idle"]') as HTMLElement;
    expect(state.dataset.role).toBe("idle");
    const meta = idle.querySelector(".ui-list-meta") as HTMLElement;
    expect(meta.textContent, "the where").toContain("claude-code-plugins · studio");
    expect(within(idle).getByText("12 min")).toBeDefined();
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
      screen.getByRole("button", { name: "Select" }).click();
    });
    await act(async () => {
      screen.getByRole("checkbox", { name: "Select kanban-e2e" }).click();
    });
    await act(async () => {
      screen.getByRole("checkbox", { name: "Select lead" }).click();
    });
    expect(document.querySelector("[data-select-bar]")).not.toBeNull();
    expect(legacyClasses(document.body, lent)).toEqual([]);
    // a machine chip and its pressed state sit on the same tokens
    const chip = ((): HTMLElement =>
      [...(document.querySelector("[data-machine-chips]") as HTMLElement).querySelectorAll("button")].find(
        (b) => b.textContent === "build-box",
      ) as HTMLElement)();
    await act(async () => {
      chip.click();
    });
    expect(chip.getAttribute("aria-pressed")).toBe("true");
    expect(legacyClasses(document.body, lent)).toEqual([]);
    // All gives the other cases their whole list back
    const all = ((): HTMLElement =>
      [...(document.querySelector("[data-machine-chips]") as HTMLElement).querySelectorAll("button")].find(
        (b) => b.textContent === "All",
      ) as HTMLElement)();
    await act(async () => {
      all.click();
    });
  });

  it("colours each row's state chip with its state's role token", () => {
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
      // the list's rows carry the word alone; the needs-you card answers for its own chip
      if (rowEl.tagName !== "ARTICLE") {
        expect(rowEl.querySelector("svg[data-shape]"), title).toBeNull();
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
 *  is a spy the link flows assert on. `needs` seeds the needs-you items. */
function mountNowOver(
  layout: Layout,
  answer?: (path: string, n: number) => unknown,
  needs: NeedItem[] | null = null,
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
  const registry = makeRegistry({ sessions: fixture(), needs });
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
      screen.getByRole("button", { name: "Select" }).click();
    });
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
      screen.getByRole("button", { name: "Select" }).click();
    });
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
    // the pane's tasks reads share the call log; the link POST is picked by path
    const link = h.calls.find((c) => c.path === "/api/links");
    expect(link).toEqual({
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
      screen.getByRole("button", { name: "Select" }).click();
    });
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

describe("the Select toggle", () => {
  it("keeps every checkbox off the desktop rows until Select is pressed", async () => {
    mountNowOver("desktop");
    expect(screen.queryByRole("checkbox")).toBeNull();
    const toggle = screen.getByRole("button", { name: "Select" });
    expect(toggle.getAttribute("data-select-toggle")).toBe("");
    await act(async () => {
      toggle.click();
    });
    expect(screen.getByRole("button", { name: "Done" }).getAttribute("data-select-toggle")).toBe("");
    // one checkbox per row, and a pick raises the send bar
    const wrap = document.querySelectorAll("[data-select-row]");
    const boxes = document.querySelectorAll("[data-select-row] input[type='checkbox']");
    expect(wrap.length).toBeGreaterThan(0);
    expect(boxes.length).toBe(wrap.length);
    await act(async () => {
      screen.getByRole("checkbox", { name: "Select lead" }).click();
    });
    expect(screen.getByText("1 selected")).toBeDefined();
    expect(screen.getByRole("button", { name: "Send to 1 session" })).toBeDefined();
  });

  it("hides the checkboxes and clears the picks on Done", async () => {
    mountNowOver("desktop");
    await act(async () => {
      screen.getByRole("button", { name: "Select" }).click();
    });
    await act(async () => {
      screen.getByRole("checkbox", { name: "Select kanban-e2e" }).click();
    });
    expect(screen.getByText("1 selected")).toBeDefined();
    await act(async () => {
      screen.getByRole("button", { name: "Done" }).click();
    });
    expect(screen.queryByRole("checkbox")).toBeNull();
    expect(screen.queryByText("1 selected")).toBeNull();
    expect(screen.queryByRole("button", { name: /^Send to/ })).toBeNull();
  });

  it("keeps the phone on its long-press, with no Select button and no checkbox", async () => {
    mountNowOver("phone");
    expect(screen.queryByRole("button", { name: "Select" })).toBeNull();
    expect(screen.queryByRole("checkbox")).toBeNull();
    const held = document.querySelector<HTMLElement>("[data-hold-row]");
    expect(held).not.toBeNull();
    await act(async () => {
      held?.dispatchEvent(new Event("pointerdown", { bubbles: true }));
      await new Promise((r) => setTimeout(r, 560));
    });
    expect(screen.getByText("1 selected")).toBeDefined();
  });
});

describe("the feature chips", () => {
  /** The tasks answer of a carded session: two held cards in two clusters on one board. */
  const held = (): unknown => ({
    todos: [],
    tasks: [],
    cards: [
      { id: "c1", title: "Ship the router", column: "doing", cluster: "swr", source: "held" },
      { id: "c2", title: "Wire the hub", column: "doing", cluster: "swe", source: "held" },
    ],
    board: "e2e-demo",
  });

  it("shows the held cards' clusters as chip links under a working row, each to its board feature", async () => {
    mountNowOver("phone", (path) => (path === "/api/sessions/s-work-1/tasks" ? held() : {}));
    await flush();
    const chips = [
      ...document.querySelectorAll('[data-row="s-work-1"] [data-feature-chip]'),
    ] as HTMLAnchorElement[];
    expect(chips.map((c) => c.textContent)).toEqual(["swe", "swr"]);
    expect(chips.map((c) => c.getAttribute("href"))).toEqual([
      "#/kanban/e2e-demo/swe",
      "#/kanban/e2e-demo/swr",
    ]);
    // a row with chips is a container, never one anchor holding others: its title link opens it
    expect(document.querySelector("a[data-row='s-work-1']")).toBeNull();
    expect(document.querySelector('[data-row="s-work-1"] > a[href="#/sessions/s-work-1"]')).not.toBeNull();
  });

  it("keeps the desk row one tap: its title link opens the session, the chips are links of their own", async () => {
    mountNowOver("desktop", (path) => (path === "/api/sessions/s-work-1/tasks" ? held() : {}));
    await flush();
    expect(document.querySelector("a[data-row='s-work-1']")).toBeNull();
    const rowEl = document.querySelector('[data-row="s-work-1"]') as HTMLElement;
    expect(rowEl).not.toBeNull();
    const chips = [...rowEl.querySelectorAll("[data-feature-chip]")] as HTMLAnchorElement[];
    expect(chips.map((c) => c.textContent)).toEqual(["swe", "swr"]);
    // the title link's overlay stretches over the whole row, so the row's tap still opens it
    const title = rowEl.querySelector('a[href="#/sessions/s-work-1"]') as HTMLAnchorElement;
    expect(title?.className).toContain("after:absolute");
  });

  it("carries the chips inside the needs-you card too", async () => {
    mountNowOver("phone", (path) => (path === "/api/sessions/s-need-1/tasks" ? held() : {}), [needItem()]);
    await flush();
    const card = document.querySelector('article[data-row="s-need-1"]') as HTMLElement;
    expect(card).not.toBeNull();
    const chips = [...card.querySelectorAll("[data-feature-chip]")] as HTMLAnchorElement[];
    expect(chips.map((c) => c.textContent)).toEqual(["swe", "swr"]);
  });

  it("falls back to the authored cards when the member holds none", async () => {
    mountNowOver("phone", (path) =>
      path === "/api/sessions/s-work-1/tasks"
        ? {
            todos: [],
            tasks: [],
            cards: [
              { id: "c3", title: "Sweep the docs", column: "doing", cluster: "docs", source: "authored" },
            ],
            board: "e2e-demo",
          }
        : {},
    );
    await flush();
    const chips = [...document.querySelectorAll('[data-row="s-work-1"] [data-feature-chip]')];
    expect(chips.map((c) => c.textContent)).toEqual(["docs"]);
  });

  it("shows no chip line when the answer names no board", async () => {
    const h = mountNowOver("phone", (path) =>
      path === "/api/sessions/s-work-1/tasks"
        ? {
            todos: [],
            tasks: [],
            cards: [{ id: "c1", title: "Ship", column: "doing", cluster: "swr", source: "held" }],
          }
        : {},
    );
    await flush();
    expect(h.calls.some((c) => c.path === "/api/sessions/s-work-1/tasks")).toBe(true);
    expect(document.querySelector('[data-row="s-work-1"] [data-feature-chips]')).toBeNull();
    // no chips: the row keeps its one plain anchor
    expect(document.querySelector("a[data-row='s-work-1']")).not.toBeNull();
  });

  it("asks each open row once while the list stands, and never the ended one", async () => {
    const h = mountNowOver("phone");
    await flush();
    expect(h.calls.some((c) => c.path === "/api/sessions/s-done/tasks")).toBe(false);
    const asked = h.calls.filter((c) => c.path === "/api/sessions/s-work-1/tasks").length;
    expect(asked).toBeGreaterThan(0);
    // a registry repaint re-renders the list without re-asking: the pane keeps one cache per session
    void act(() => {
      h.registry.applyRow(row({ session_id: "s-fresh", title: "fresh" }));
    });
    await flush();
    expect(h.calls.filter((c) => c.path === "/api/sessions/s-work-1/tasks").length).toBe(asked);
  });
});

describe("whereOf", () => {
  it("joins project · branch · machine, the mocks' order, and leaves the parts it lacks out", () => {
    expect(
      whereOf(row({ session_id: "s", machine: "mbp", project: "/work/huddle", branch: "feat/huddle-daily" })),
    ).toBe("huddle · feat/huddle-daily · mbp");
    expect(whereOf(row({ session_id: "s", machine: "mbp", project: "/work/huddle" }))).toBe("huddle · mbp");
    expect(whereOf(row({ session_id: "s", project: "/work/huddle" }))).toBe("huddle");
    expect(whereOf(row({ session_id: "s" }))).toBe("");
  });
});
