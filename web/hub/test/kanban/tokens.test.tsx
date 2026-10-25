// tokens.test.tsx — the Kanban page on the design system: every component wears ui-* classes or its
// own, never a daisyUI one; the search hint fits the device; the density button names its action;
// chips, Hide done and the plain actions are tap targets; the stale line ticks quietly; a phone lane
// leaves its name to the tab above it; and a storage whose getter throws never breaks the board.
import { act, render, screen } from "@testing-library/preact";
import { userEvent } from "@testing-library/user-event";
import { useState } from "preact/hooks";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { Badge } from "../../src/kanban/components/Badge.tsx";
import { BoardPicker } from "../../src/kanban/components/BoardPicker.tsx";
import { Board } from "../../src/kanban/components/Board.tsx";
import { CardView } from "../../src/kanban/components/Card.tsx";
import { Column } from "../../src/kanban/components/Column.tsx";
import { FlowBar } from "../../src/kanban/components/FlowBar.tsx";
import { Toolbar } from "../../src/kanban/components/Toolbar.tsx";
import { EMPTY_FILTERS, type Filters, useBacklogView, useDensity } from "../../src/kanban/store.ts";
import type { Card } from "../../src/kanban/types.ts";
import { cardOf, countColumns, legacyClasses, makeCard, makeSnapshot, snapshot } from "./helpers.ts";
import { stubMatchMedia } from "./match-media.ts";

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const BOARDS = [
  { key: "a1a1", root: "/repo/alpha", name: "alpha" },
  { key: "b2b2", root: "/repo/beta", name: "beta" },
];

/** A board with a holder, a tag and one done card, so every toolbar control shows. */
function cards(): Card[] {
  return [
    makeCard({ id: "C-1", column: "in-progress", holder: "glm-A", tags: ["web"] }),
    makeCard({ id: "C-2", column: "done", holder: "", tags: [] }),
  ];
}

/** The toolbar with its filters in state, every prop overridable. */
function Row({
  over = {},
  initial = EMPTY_FILTERS,
}: {
  over?: Partial<Parameters<typeof Toolbar>[0]>;
  initial?: Filters;
}) {
  const [filters, setFilters] = useState(initial);
  return (
    <Toolbar
      cards={cards()}
      filters={filters}
      onChange={setFilters}
      boards={BOARDS}
      boardKey="a1a1"
      onBoard={() => undefined}
      epoch={3}
      connected
      live
      density="compact"
      onDensity={() => undefined}
      {...over}
    />
  );
}

describe("Feature: the toolbar on the tokens", () => {
  it("Scenario: no daisyUI class on the desktop row, the picker and an open menu", async () => {
    // Given the desktop toolbar with a pick
    stubMatchMedia(() => false);
    const { container } = render(
      <Row initial={{ ...EMPTY_FILTERS, picks: { ...EMPTY_FILTERS.picks, holder: ["glm-A"] } }} />,
    );
    // When the picker and the Holder menu open
    await userEvent.click(screen.getByRole("button", { name: "Board" }));
    expect(legacyClasses(container)).toEqual([]);
    await userEvent.click(screen.getByRole("button", { name: "Holder: 1" }));
    // Then nothing in the row wears a daisyUI class
    expect(legacyClasses(container)).toEqual([]);
  });

  it("Scenario: the search box is a ui-input whose hint names / only where a keyboard is", () => {
    // Given a device with a fine pointer
    stubMatchMedia(() => false);
    const desk = render(<Row />);
    const box = screen.getByRole("searchbox", { name: "Search" });
    // Then the box is the design system's input and the hint names the shortcut
    expect(box.classList.contains("ui-input")).toBe(true);
    expect(box.getAttribute("placeholder")).toBe("Search (press /)");
    desk.unmount();
    // Given a touch screen
    stubMatchMedia((query) => query.includes("pointer: coarse"));
    render(<Row />);
    // Then the hint is the plain word
    expect(screen.getByRole("searchbox", { name: "Search" }).getAttribute("placeholder")).toBe("Search");
  });

  it("Scenario: the density button names the action it takes and is a design-system button", async () => {
    // Given the compact board
    stubMatchMedia(() => false);
    const onDensity = vi.fn();
    const view = render(<Row over={{ density: "compact", onDensity }} />);
    const expand = screen.getByRole("button", { name: "Expand cards" });
    // Then it is a ui-btn action, not a pressed toggle
    expect(expand.classList.contains("ui-btn")).toBe(true);
    expect(expand.hasAttribute("aria-pressed")).toBe(false);
    // When it is pressed, the page is asked for the comfortable density
    await userEvent.click(expand);
    expect(onDensity).toHaveBeenCalledWith("comfortable");
    view.unmount();
    // Given the comfortable board, the button offers the way back
    render(<Row over={{ density: "comfortable", onDensity }} />);
    await userEvent.click(screen.getByRole("button", { name: "Compact cards" }));
    expect(onDensity).toHaveBeenCalledWith("compact");
  });

  it("Scenario: the kind menus, the Filters button and the pills are chips; Hide done is a switch its label carries", async () => {
    // Given the phone toolbar with a pick, unfolded
    stubMatchMedia((query) => query.includes("max-width"));
    const { container } = render(
      <Row initial={{ ...EMPTY_FILTERS, picks: { ...EMPTY_FILTERS.picks, holder: ["glm-A"] } }} />,
    );
    const filters = screen.getByRole("button", { name: "Filters: 1" });
    expect(filters.classList.contains("chip")).toBe(true);
    await userEvent.click(filters);
    // Then every kind menu and pill is a chip
    const kinds = [...container.querySelectorAll<HTMLButtonElement>("button[data-kind]")];
    expect(kinds.length).toBeGreaterThan(0);
    expect(kinds.every((b) => b.classList.contains("chip"))).toBe(true);
    expect(screen.getByRole("button", { name: "Remove Holder glm-A" }).classList.contains("chip")).toBe(true);
    expect(screen.getByRole("button", { name: "Clear all" }).classList.contains("chip")).toBe(true);
    // And Hide done is a plain checkbox inside its label, beside a drawn switch, with no daisyUI toggle
    const toggle = screen.getByRole("checkbox", { name: /^Hide done/ });
    expect(toggle.classList.length).toBe(0);
    expect(toggle.closest("label.hide-done")?.querySelector(".switch[aria-hidden='true']")).toBeTruthy();
    await userEvent.click(toggle);
    expect(screen.getByRole("checkbox", { name: /^Hide done/ })).toHaveProperty("checked", true);
    // And the grouping toggle is a design-system button
    expect(legacyClasses(container)).toEqual([]);
  });

  it("Scenario: the stale line's age ticks off the live region; only Reconnecting is announced", async () => {
    vi.useFakeTimers();
    const at = Date.now();
    // Given a live board whose last frame came just now
    const view = render(<Row over={{ lastFrameAt: at }} />);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000);
    });
    // Then the line shows the age in a part screen readers are not told about
    const line = view.container.querySelector(".stale-line") as HTMLElement;
    expect(line.textContent).toBe("Updated 5 s ago");
    expect(line.querySelector("[aria-live='off']")?.textContent).toBe("Updated 5 s ago");
    const status = screen.getByRole("status");
    expect(line.contains(status)).toBe(true);
    expect(status.textContent).toBe("");
    // When the stream has been down 10 s
    view.rerender(<Row over={{ lastFrameAt: at, connected: false }} />);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000);
    });
    // Then the polite region says Reconnecting, and the age is gone
    expect(screen.getByRole("status").textContent).toBe("Reconnecting");
    expect(line.textContent).toBe("Reconnecting");
  });
});

describe("Feature: the board on the tokens", () => {
  const heavy = makeSnapshot([
    makeCard({ id: "OPEN-1", column: "backlog" }),
    ...Array.from({ length: 21 }, (_, i) => makeCard({ id: `DONE-${i}`, column: "done", tags: [] })),
  ]);

  it("Scenario: the loading board is a kanban skeleton, not daisyUI's", () => {
    const { container } = render(
      <Board
        snapshot={null}
        filters={EMPTY_FILTERS}
        changeSeq={0}
        changedIds={new Set()}
        onOpen={() => undefined}
      />,
    );
    expect(container.querySelector(".kanban-skeleton")).toBeTruthy();
    expect(legacyClasses(container)).toEqual([]);
  });

  it("Scenario: Show done, Clear filters and Show all are design-system buttons", async () => {
    stubMatchMedia(() => false);
    const hiding = render(
      <Board
        snapshot={heavy}
        filters={{ ...EMPTY_FILTERS, hideDone: true }}
        changeSeq={0}
        changedIds={new Set()}
        onOpen={() => undefined}
        onShowDone={() => undefined}
      />,
    );
    expect(screen.getByRole("button", { name: "Show done" }).classList.contains("ui-btn")).toBe(true);
    hiding.unmount();
    const empty = render(
      <Board
        snapshot={heavy}
        filters={{ ...EMPTY_FILTERS, q: "nothing matches this" }}
        changeSeq={0}
        changedIds={new Set()}
        onOpen={() => undefined}
        onClearFilters={() => undefined}
      />,
    );
    expect(screen.getByRole("button", { name: "Clear filters" }).classList.contains("ui-btn")).toBe(true);
    empty.unmount();
    const { container } = render(
      <Board
        snapshot={heavy}
        filters={EMPTY_FILTERS}
        changeSeq={0}
        changedIds={new Set()}
        onOpen={() => undefined}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Done, 21 cards, show column" }));
    expect(screen.getByRole("button", { name: "Show all 21" }).classList.contains("ui-btn")).toBe(true);
    expect(legacyClasses(container)).toEqual([]);
  });

  it("Scenario: on a phone the column tabs are the design system's tab strip and each lane's heading is for screen readers only", () => {
    stubMatchMedia((query) => query.includes("max-width"));
    const { container } = render(
      <Board
        snapshot={snapshot}
        filters={EMPTY_FILTERS}
        changeSeq={0}
        changedIds={new Set()}
        onOpen={() => undefined}
      />,
    );
    const strip = screen.getByRole("tablist", { name: "Columns" });
    expect(strip.classList.contains("ui-tabs")).toBe(true);
    const headings = [...container.querySelectorAll<HTMLElement>(".lanes section.col > h2")];
    expect(headings.length).toBe(7);
    expect(headings.every((h) => h.classList.contains("ui-sr"))).toBe(true);
    expect(screen.getByRole("heading", { name: "Backlog 3" })).toBeTruthy();
    expect(legacyClasses(container)).toEqual([]);
  });

  it("Scenario: the desktop columns keep their visible headings", () => {
    stubMatchMedia(() => false);
    const { container } = render(
      <Board
        snapshot={snapshot}
        filters={EMPTY_FILTERS}
        changeSeq={0}
        changedIds={new Set()}
        onOpen={() => undefined}
      />,
    );
    const heading = container.querySelector<HTMLElement>('section[data-column="backlog"] > h2');
    expect(heading?.classList.contains("ui-sr")).toBe(false);
  });
});

describe("Feature: the rail, the flow bar and the picker on the tokens", () => {
  it("Scenario: a rail is a rail-button whose spoken name sits in a ui-sr span", () => {
    const { container } = render(
      <Column
        id="done"
        title="Done"
        cards={[makeCard({ id: "A-1" })]}
        changeSeq={0}
        changedIds={new Set()}
        onOpen={() => undefined}
        collapsed
        onExpand={() => undefined}
      />,
    );
    const rail = screen.getByRole("button", { name: "Done, 1 cards, show column" });
    expect(rail.classList.contains("rail-button")).toBe(true);
    expect(rail.querySelector(".ui-sr")?.textContent).toBe("Done, 1 cards, show column");
    expect(legacyClasses(container)).toEqual([]);
  });

  it("Scenario: the desktop's flow labels stay plain buttons, the phone's are ui-tabs", () => {
    const columns = countColumns(snapshot.cards);
    const desk = render(<FlowBar columns={columns} onPick={() => undefined} />);
    expect(desk.container.querySelector(".ui-tabs")).toBeNull();
    desk.unmount();
    const phone = render(<FlowBar columns={columns} onPick={() => undefined} activeId="ready" />);
    expect(screen.getByRole("tablist", { name: "Columns" }).classList.contains("ui-tabs")).toBe(true);
    expect(screen.getByRole("tab", { name: "Ready 1" }).getAttribute("aria-selected")).toBe("true");
    expect(legacyClasses(phone.container)).toEqual([]);
  });

  it("Scenario: the picker is a picker-button and its filter box a ui-input", async () => {
    const { container } = render(<BoardPicker boards={BOARDS} boardKey="a1a1" onBoard={() => undefined} />);
    const button = screen.getByRole("button", { name: "Board" });
    expect(button.classList.contains("picker-button")).toBe(true);
    await userEvent.click(button);
    expect(screen.getByRole("combobox", { name: "Filter boards" }).classList.contains("ui-input")).toBe(true);
    expect(legacyClasses(container)).toEqual([]);
  });
});

describe("Feature: badges on the status tones", () => {
  it("Scenario: each kind reads its tone from the design system's badge", () => {
    const tone = (kind: string, label: string): string | undefined => {
      const { container, unmount } = render(<Badge badge={{ kind, label }} />);
      const li = container.querySelector("li");
      const badge = li?.querySelector(".ui-badge");
      const found = [...(badge?.classList ?? [])].find((c) => c !== "ui-badge");
      expect(li?.classList.contains(`b-${kind}`)).toBe(true);
      expect(badge?.textContent).toBe(label);
      unmount();
      return found;
    };
    expect(tone("blocked", "blocked")).toBe("ui-badge-fail");
    expect(tone("waits", "waits on X")).toBe("ui-badge-warn");
    expect(tone("hand", "hand-off")).toBe("ui-badge-warn");
    expect(tone("incomplete", "incomplete")).toBe("ui-badge-warn");
    expect(tone("priority", "P0")).toBe("ui-badge-fail");
    expect(tone("priority", "P1")).toBe("ui-badge-warn");
    expect(tone("priority", "P2")).toBe("ui-badge-info");
    expect(tone("priority", "P3")).toBe("ui-badge-neutral");
    expect(tone("type", "feature")).toBe("ui-badge-neutral");
  });

  it("Scenario: the card's expedite mark is a fail badge and the card wears no daisyUI badge", () => {
    const { container } = render(<CardView card={cardOf("KBN-UI")} flash={0} onOpen={() => undefined} />);
    const expedite = container.querySelector("ul.meta li.b-expedite .ui-badge");
    expect(expedite?.classList.contains("ui-badge-fail")).toBe(true);
    expect(expedite?.textContent).toBe("Expedite");
    expect(legacyClasses(container)).toEqual([]);
  });
});

describe("Feature: a storage whose getter throws", () => {
  /** A window whose localStorage getter throws, the way a private window can. */
  function blockStorage(): void {
    vi.spyOn(window, "localStorage", "get").mockImplementation(() => {
      throw new Error("denied");
    });
  }

  it("Scenario: the backlog view reads as the default and takes a change", async () => {
    blockStorage();
    function View() {
      const [view, setView] = useBacklogView("a1a1");
      return (
        <button type="button" onClick={() => setView({ grouping: "cluster", open: [] })}>
          {view.grouping}
        </button>
      );
    }
    render(<View />);
    const button = screen.getByRole("button", { name: "priority" });
    await userEvent.click(button);
    expect(screen.getByRole("button", { name: "cluster" })).toBeTruthy();
  });

  it("Scenario: the density reads as compact and takes a change", async () => {
    blockStorage();
    function View() {
      const [density, setDensity] = useDensity();
      return (
        <button type="button" onClick={() => setDensity("comfortable")}>
          {density}
        </button>
      );
    }
    render(<View />);
    await userEvent.click(screen.getByRole("button", { name: "compact" }));
    expect(screen.getByRole("button", { name: "comfortable" })).toBeTruthy();
  });
});
