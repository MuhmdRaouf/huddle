// KanbanTab.test.tsx — the tab over faked huddle endpoints: the board list and its states, the
// picker moving the address and the board, a gone board, a live delta, the stream's lifetime, and
// the list's retry ladder, deadline and refetch on a return to the page.
import { act, fireEvent, render, screen } from "@testing-library/preact";
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

/** How one board-list request answers: a network failure, no answer until it is aborted, or the list. */
type ListStep = "fail" | "hang" | "ok";

/** stubNet, with the board list answering each request by the next step (then "ok"); returns
 *  every URL seen and the signal each board-list request carried. */
function stubListSteps(steps: ListStep[]): { calls: string[]; signals: (AbortSignal | undefined)[] } {
  const calls = stubNet();
  const net = globalThis.fetch;
  const signals: (AbortSignal | undefined)[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: URL | string, init?: RequestInit): Promise<unknown> => {
      if (String(input) !== "/api/kanban/boards") return net(input, init);
      signals.push(init?.signal ?? undefined);
      const step = steps.shift() ?? "ok";
      if (step === "ok") return net(input, init);
      calls.push(String(input));
      if (step === "fail") throw new TypeError("Failed to fetch");
      return new Promise((_, reject) => {
        init?.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
      });
    }),
  );
  return { calls, signals };
}

/** The board-list requests among the URLs seen. */
function listCalls(calls: string[]): number {
  return calls.filter((url) => url === "/api/kanban/boards").length;
}

/** Runs the faked clock forward and lets the answers it frees settle into the render. */
async function advance(ms: number): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

/** Sets the page's visibility and tells the document, as a return from the background does. */
function setVisibility(state: DocumentVisibilityState): void {
  Object.defineProperty(document, "visibilityState", { configurable: true, get: () => state });
  fireEvent(document, new Event("visibilitychange"));
}

beforeEach(() => {
  location.hash = "";
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  FakeSource.made = [];
  Reflect.deleteProperty(document, "visibilityState");
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
    const toggle = screen.getByRole("checkbox", { name: /^Hide done/ });
    // the default lands one render after the first paint of the board
    await vi.waitFor(() => expect(toggle.getAttribute("aria-checked")).toBe("true"));
    // done keeps its count behind its rail, and the board says how many it hides
    expect(screen.getByRole("checkbox", { name: "Hide done (60)" })).toBe(toggle);
    expect(screen.getByRole("button", { name: "Done, 60 cards, show column" })).toBeDefined();
    expect(screen.getByText(/60 done hidden/)).toBeDefined();
    await userEvent.click(screen.getByRole("button", { name: "Show done" }));
    expect(toggle.getAttribute("aria-checked")).toBe("false");
    expect(screen.queryByText(/done hidden/)).toBeNull();
    // done is counted again, still behind its rail until it is opened
    const rail = screen.getByRole("button", { name: "Done, 60 cards, show column" });
    await userEvent.click(rail);
    // opened, the latest twenty show with the show-all button beside them
    expect(screen.getByRole("heading", { name: "Done 60" })).toBeDefined();
    expect(screen.getByRole("button", { name: "Show all 60" })).toBeDefined();
  });

  it("Clear filters shows the done cards hide-done kept off the board", async () => {
    const shown = [makeCard({ id: "R-1", column: "ready", holder: "", tags: [] })];
    for (let i = 0; i < 60; i += 1)
      shown.push(makeCard({ id: `D-${i}`, column: "done", holder: "", tags: [] }));
    const heavy: Snapshot = { ...clone(snapshot), cards: shown, columns: countColumns(shown) };
    stubNet(BOARDS.slice(0, 1), false, heavy);
    render(<KanbanTab boardKey="a1a1" />);
    await screen.findByRole("button", { name: /R-1:/ });
    const toggle = screen.getByRole("checkbox", { name: /^Hide done/ });
    await vi.waitFor(() => expect(toggle.getAttribute("aria-checked")).toBe("true"));
    // a search that matches only done cards empties the board
    await userEvent.type(screen.getByRole("searchbox", { name: "Search" }), "D-1");
    await userEvent.click(await screen.findByRole("button", { name: "Clear filters" }));
    expect(toggle.getAttribute("aria-checked")).toBe("false");
    expect(screen.getByRole("searchbox", { name: "Search" })).toHaveProperty("value", "");
    expect(screen.getByRole("button", { name: "Done, 60 cards, show column" })).toBeDefined();
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

  describe("the board list over a flaky network", () => {
    beforeEach(() => {
      vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    });

    it("retries a failed list and shows the board in the same tab", async () => {
      const { calls } = stubListSteps(["fail"]);
      const view = render(<KanbanTab boardKey={null} />);
      const tab = view.container.querySelector(".kanban-tab");
      await advance(0);
      expect(screen.getByText("The board list is unreachable.")).toBeDefined();
      await advance(999);
      expect(listCalls(calls)).toBe(1);
      await advance(1);
      expect(listCalls(calls)).toBe(2);
      expect(screen.getByRole("button", { name: "Board" })).toBeDefined();
      expect(screen.queryByText("The board list is unreachable.")).toBeNull();
      expect(view.container.querySelector(".kanban-tab")).toBe(tab);
    });

    it("climbs the ladder while the list keeps failing", async () => {
      const { calls } = stubListSteps(["fail", "fail", "fail", "fail"]);
      render(<KanbanTab boardKey={null} />);
      await advance(0);
      for (const [wait, total] of [
        [1000, 2],
        [2000, 3],
        [4000, 4],
        [8000, 5],
      ] as const) {
        await advance(wait - 1);
        expect(listCalls(calls)).toBe(total - 1);
        await advance(1);
        expect(listCalls(calls)).toBe(total);
      }
      expect(screen.getByRole("button", { name: "Board" })).toBeDefined();
    });

    it("a list that never answers turns into the failed line with Retry after 10 s", async () => {
      const { calls, signals } = stubListSteps(["hang", "hang"]);
      render(<KanbanTab boardKey={null} />);
      await advance(9999);
      expect(screen.getByRole("status", { name: "Loading the boards" })).toBeDefined();
      expect(screen.queryByText("The board list is unreachable.")).toBeNull();
      await advance(1);
      expect(signals[0]?.aborted).toBe(true);
      expect(screen.getByText("The board list is unreachable.")).toBeDefined();
      // the Retry button asks again now, ahead of the ladder
      fireEvent.click(screen.getByRole("button", { name: "Retry" }));
      await advance(0);
      expect(listCalls(calls)).toBe(2);
      // the second ask hangs too; its own deadline aborts it and the ladder's next rung answers
      await advance(10_000);
      expect(signals[1]?.aborted).toBe(true);
      await advance(1000);
      expect(screen.getByRole("button", { name: "Board" })).toBeDefined();
      expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
    });

    it("refetches the list when the page becomes visible again", async () => {
      const { calls } = stubListSteps([]);
      render(<KanbanTab boardKey="a1a1" />);
      await advance(0);
      expect(listCalls(calls)).toBe(1);
      setVisibility("hidden");
      await advance(0);
      expect(listCalls(calls)).toBe(1);
      setVisibility("visible");
      await advance(0);
      expect(listCalls(calls)).toBe(2);
      expect(screen.getByRole("button", { name: "Board" })).toBeDefined();
    });
  });
});
