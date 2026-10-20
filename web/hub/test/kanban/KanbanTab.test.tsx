// KanbanTab.test.tsx — the tab over faked huddle endpoints: the board list and its states, the
// picker moving the address and the board, a gone board, a live delta, and the stream's lifetime.
import { act, render, screen } from "@testing-library/preact";
import { userEvent } from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { KanbanTab } from "../../src/kanban/KanbanTab.tsx";
import type { Snapshot } from "../../src/kanban/types.ts";
import { cardOf, clone, countColumns, makeCard, snapshot, ticketUi } from "./helpers.ts";

/** Two distinct repos, as /api/kanban/boards sends them. */
const BOARDS = [
  { key: "a1a1", root: "/repo/alpha", name: "alpha" },
  { key: "b2b2", root: "/repo/beta", name: "beta" },
];

/** Two repos under one name, so the picker has to disambiguate with the root. */
const TWINS = [
  { key: "a1a1", root: "/repo/one", name: "dupe" },
  { key: "b2b2", root: "/repo/two", name: "dupe" },
];

/** Beta's board: one card alpha's snapshot does not carry. */
function betaSnapshot(): Snapshot {
  const card = makeCard({ id: "B-ONE", title: "Only on beta" });
  return { ...clone(snapshot), cards: [card], columns: countColumns([card]) };
}

/** A stand-in EventSource: every instance kept, every listener reachable, close counted. */
class FakeSource {
  static made: FakeSource[] = [];
  closed = false;
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  private readonly listeners = new Map<string, ((event: { data: string }) => void)[]>();

  constructor(readonly url: string) {
    FakeSource.made.push(this);
  }

  addEventListener(name: string, fn: (event: { data: string }) => void): void {
    const list = this.listeners.get(name) ?? [];
    list.push(fn);
    this.listeners.set(name, list);
  }

  emit(name: string, data: string): void {
    for (const fn of this.listeners.get(name) ?? []) fn({ data });
  }

  close(): void {
    this.closed = true;
  }
}

/** Fakes fetch over the huddle endpoints and EventSource; answers the named boards (gone keys
 *  404) and returns every URL it saw. */
function stubNet(
  boards: { key: string; root: string; name: string }[] = BOARDS,
  failBoards = false,
  firstBoard: Snapshot = snapshot,
): string[] {
  const calls: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(
      async (input: URL | string): Promise<{ ok: boolean; status: number; json: () => Promise<unknown> }> => {
        const url = String(input);
        calls.push(url);
        if (url === "/api/kanban/boards") {
          if (failBoards) return { ok: false, status: 500, json: async () => ({}) };
          return { ok: true, status: 200, json: async () => ({ boards }) };
        }
        const board = /\/api\/kanban\/b\/([^/]+)\/api\/board$/.exec(url);
        if (board !== null) {
          const key = decodeURIComponent(board[1] ?? "");
          if (!boards.some((b) => b.key === key))
            return { ok: false, status: 404, json: async () => ({ error: `no board ${key}` }) };
          return {
            ok: true,
            status: 200,
            json: async () => clone(key === "b2b2" ? betaSnapshot() : firstBoard),
          };
        }
        if (/\/api\/kanban\/b\/[^/]+\/api\/tickets\//.test(url))
          return { ok: true, status: 200, json: async () => clone(ticketUi) };
        return { ok: false, status: 404, json: async () => ({}) };
      },
    ),
  );
  vi.stubGlobal("EventSource", FakeSource);
  return calls;
}

beforeEach(() => {
  location.hash = "";
});

afterEach(() => {
  vi.unstubAllGlobals();
  FakeSource.made = [];
});

describe("KanbanTab", () => {
  it("shows a skeleton while the board list is on its way", () => {
    stubNet();
    render(<KanbanTab boardKey={null} />);
    const loading = document.querySelector('[role="status"][aria-label="Loading the boards"]');
    expect(loading?.querySelector(".skeleton")).toBeTruthy();
  });

  it("lists the boards and opens the first", async () => {
    stubNet();
    const view = render(<KanbanTab boardKey={null} />);
    await screen.findByRole("button", { name: "Board" });
    // the picker opens onto the list: name bold, the root's tail beside it
    await userEvent.click(screen.getByRole("button", { name: "Board" }));
    expect(screen.getByRole("option", { name: "alpha repo/alpha" })).toBeDefined();
    expect(screen.getByRole("option", { name: "beta repo/beta" })).toBeDefined();
    // the bare address adopts the first board's; the effect flushes on the timer tick
    await vi.waitFor(() => expect(location.hash).toBe("#/kanban/a1a1"));
    // the shell's hashchange lands the key back in as the prop; the board paints
    view.rerender(<KanbanTab boardKey="a1a1" />);
    await screen.findByText("kanban: Preact board page, responsive and live, with its Vitest suite");
    expect(screen.getAllByRole("button", { name: /^KBN-PLUGIN:/ }).length).toBeGreaterThan(0);
  });

  it("shows the empty line when there are no boards", async () => {
    stubNet([]);
    render(<KanbanTab boardKey={null} />);
    await screen.findByText(
      "No boards yet. A session working in a repo with .agents/kanban adds its board here.",
    );
    expect(screen.queryByRole("button", { name: "Board" })).toBeNull();
    expect(location.hash).toBe("");
  });

  it("the picker changes the address and the board", async () => {
    stubNet(TWINS);
    const view = render(<KanbanTab boardKey="a1a1" />);
    await screen.findByText("kanban: Preact board page, responsive and live, with its Vitest suite");
    // one name across the boards: the picker disambiguates with the root's tail
    await userEvent.click(screen.getByRole("button", { name: "Board" }));
    expect(screen.getByRole("option", { name: "dupe repo/one" })).toBeDefined();
    expect(screen.getByRole("option", { name: "dupe repo/two" })).toBeDefined();
    // typing narrows the list, Enter picks
    await userEvent.type(screen.getByRole("combobox", { name: "Filter boards" }), "two");
    await userEvent.type(screen.getByRole("combobox", { name: "Filter boards" }), "{Enter}");
    expect(location.hash).toBe("#/kanban/b2b2");
    // the shell follows the address: the old stream closes, the new board paints
    view.rerender(<KanbanTab boardKey="b2b2" />);
    await screen.findByText("Only on beta");
    expect(
      screen.queryByText("kanban: Preact board page, responsive and live, with its Vitest suite"),
    ).toBeNull();
    expect(FakeSource.made.length).toBe(2);
    expect(FakeSource.made[0]?.closed).toBe(true);
    expect(FakeSource.made[1]?.closed).toBe(false);
  });

  it("hides done on a done-heavy board until the reader asks for it", async () => {
    const shown = [makeCard({ id: "R-1", column: "ready", holder: "", tags: [] })];
    for (let i = 0; i < 60; i += 1)
      shown.push(makeCard({ id: `D-${i}`, column: "done", holder: "", tags: [] }));
    const heavy: Snapshot = { ...clone(snapshot), cards: shown, columns: countColumns(shown) };
    stubNet(BOARDS.slice(0, 1), false, heavy);
    render(<KanbanTab boardKey="a1a1" />);
    await screen.findByRole("button", { name: /R-1:/ });
    const toggle = screen.getByRole("checkbox", { name: "Hide done" });
    // the default lands one render after the first paint of the board
    await vi.waitFor(() => expect(toggle.getAttribute("aria-checked")).toBe("true"));
    // done sits behind its rail while the filter hides it
    expect(screen.getByRole("button", { name: "Done, 0 cards, show column" })).toBeDefined();
    await userEvent.click(toggle);
    // done is counted again, still behind its rail until it is opened
    const rail = screen.getByRole("button", { name: "Done, 60 cards, show column" });
    await userEvent.click(rail);
    // opened, the latest twenty show with the show-all button beside them
    expect(screen.getByRole("heading", { name: "Done 60" })).toBeDefined();
    expect(screen.getByRole("button", { name: "Show all 60" })).toBeDefined();
  });

  it("a gone board says so and keeps the picker", async () => {
    const calls = stubNet();
    render(<KanbanTab boardKey="dead" />);
    await screen.findByText("That board is gone. Pick another.");
    expect(screen.getByRole("button", { name: "Board" })).toBeDefined();
    expect(calls.some((url) => url.includes("/b/dead/"))).toBe(false);
  });

  it("a live delta repaints the board", async () => {
    stubNet();
    render(<KanbanTab boardKey="a1a1" />);
    await vi.waitFor(() => expect(FakeSource.made.length).toBe(1));
    const source = FakeSource.made[0];
    expect(source?.url).toBe("/api/kanban/b/a1a1/api/events");
    await act(async () => {
      source?.emit("snapshot", JSON.stringify(clone(snapshot)));
    });
    await screen.findByText("kanban: Preact board page, responsive and live, with its Vitest suite");
    const renamed = { ...cardOf("KBN-UI"), title: "Renamed live" };
    await act(async () => {
      source?.emit("delta", JSON.stringify({ rev: snapshot.rev + 1, upsert: [renamed], remove: [] }));
    });
    await screen.findByText("Renamed live");
    expect(
      screen.queryByText("kanban: Preact board page, responsive and live, with its Vitest suite"),
    ).toBeNull();
  });

  it("unmount closes the stream", async () => {
    stubNet();
    const view = render(<KanbanTab boardKey="a1a1" />);
    await vi.waitFor(() => expect(FakeSource.made.length).toBe(1));
    expect(FakeSource.made[0]?.closed).toBe(false);
    // the cleanup is deferred past the unmounting commit; act flushes it
    await act(async () => {
      view.unmount();
    });
    expect(FakeSource.made[0]?.closed).toBe(true);
  });

  it("opens a card's drawer and shows the ticket body", async () => {
    stubNet();
    render(<KanbanTab boardKey="a1a1" />);
    await screen.findByText("kanban: Preact board page, responsive and live, with its Vitest suite");
    await userEvent.click(screen.getByRole("button", { name: /^KBN-UI:/ }));
    await screen.findByRole("heading", { level: 2, name: "Description" });
    expect(screen.getByRole("dialog", { name: /^KBN-UI:/ })).toBeDefined();
  });

  it("says when the board list cannot be read", async () => {
    stubNet(BOARDS, true);
    render(<KanbanTab boardKey={null} />);
    await screen.findByText("The board list is unreachable.");
    expect(screen.queryByRole("combobox", { name: "Board" })).toBeNull();
  });
});
