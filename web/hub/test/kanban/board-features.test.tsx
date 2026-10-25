// board-features.test.tsx — the redesigned board's behaviours: columns that earn their width
// (rails for the empty ones), the dependency thread, and the board's words.
import { fireEvent, render, screen } from "@testing-library/preact";
import { userEvent } from "@testing-library/user-event";
import { describe, expect, it, vi } from "vite-plus/test";
import { Board, type BoardProps } from "../../src/kanban/components/Board.tsx";
import { EMPTY_FILTERS } from "../../src/kanban/store.ts";
import { cardOf, makeSnapshot, snapshot } from "./helpers.ts";
import { stubMatchMedia } from "./match-media.ts";

function renderBoard(snap: BoardProps["snapshot"] = snapshot, props: Partial<BoardProps> = {}) {
  return render(
    <Board
      snapshot={snap}
      filters={EMPTY_FILTERS}
      changeSeq={0}
      changedIds={new Set()}
      onOpen={() => undefined}
      {...props}
    />,
  );
}

const railOf = (container: Element, column: string) =>
  container.querySelector<HTMLButtonElement>(`section[data-column="${column}"] button.rail-btn`);

const cardById = (container: Element, id: string) =>
  container.querySelector<HTMLElement>(`[data-id="${id}"]`);

describe("Feature: columns that earn their width", () => {
  it("Scenario: an empty column renders as a rail and opens on click", async () => {
    // Given a board whose review column holds no card
    const cards = [cardOf("KBN-API")];
    const { container } = renderBoard(makeSnapshot(cards));
    // Then review is a rail naming its count, closed
    const rail = railOf(container, "review");
    expect(rail?.getAttribute("aria-expanded")).toBe("false");
    expect(rail?.querySelector(".sr-only")?.textContent).toBe("Review, 0 cards, show column");
    // When the rail is clicked
    await userEvent.click(rail as HTMLButtonElement);
    // Then review is a column of its own again, empty but open
    expect(railOf(container, "review")).toBeNull();
    expect(container.querySelector('section[data-column="review"] h2')?.textContent).toBe("Review 0");
  });

  it("Scenario: done is a rail with its count until opened, then shows its cards", async () => {
    // Given a board with three done cards
    const { container } = renderBoard();
    // Then done is a rail carrying its count
    const rail = railOf(container, "done");
    expect(rail?.querySelector(".sr-only")?.textContent).toBe("Done, 3 cards, show column");
    expect(container.querySelector('section[data-column="done"] h2')).toBeNull();
    // When the rail is clicked
    await userEvent.click(rail as HTMLButtonElement);
    // Then the done column opens with its cards and its count in the heading
    expect(container.querySelector('section[data-column="done"] h2')?.textContent).toBe("Done 3");
    expect(container.querySelectorAll('section[data-column="done"] article.card')).toHaveLength(3);
  });

  it("Scenario: a column opened by hand closes again when the filters change", async () => {
    // Given review opened from its rail
    const cards = [cardOf("KBN-API")];
    const view = renderBoard(makeSnapshot(cards));
    await userEvent.click(railOf(view.container, "review") as HTMLButtonElement);
    expect(view.container.querySelector('section[data-column="review"] h2')).toBeTruthy();
    // When the filters change
    view.rerender(
      <Board
        snapshot={makeSnapshot(cards)}
        filters={{ ...EMPTY_FILTERS, q: "KBN-API" }}
        changeSeq={0}
        changedIds={new Set()}
        onOpen={() => undefined}
      />,
    );
    // Then the hand-opened column is a rail again
    expect(railOf(view.container, "review")).toBeTruthy();
  });

  it("Scenario: a flow label scrolls its column into view", async () => {
    // Given the open board and a spy on scrollIntoView
    const spy = vi.fn();
    const proto = HTMLElement.prototype as unknown as { scrollIntoView?: unknown };
    const original = proto.scrollIntoView;
    proto.scrollIntoView = spy;
    try {
      const { container } = renderBoard();
      // When the Blocked flow label is clicked
      const label = [...container.querySelectorAll<HTMLButtonElement>(".flow-label")].find(
        (b) => b.textContent === "Blocked 1",
      );
      await userEvent.click(label as HTMLButtonElement);
      // Then the blocked column is what scrolled
      const column = container.querySelector('section[data-column="blocked"]');
      expect(spy).toHaveBeenCalled();
      expect(spy.mock.contexts).toContain(column);
    } finally {
      proto.scrollIntoView = original;
    }
  });
});

describe("Feature: the dependency thread", () => {
  it("Scenario: hovering a card marks its deps waits-on, its dependents waited-by and the rest dim", () => {
    // Given the fixture board, where KBN-UI waits on KBN-API and KBN-EMBED waits on KBN-UI
    const { container } = renderBoard();
    // When KBN-UI is hovered
    fireEvent.mouseEnter(cardById(container, "KBN-UI") as HTMLElement);
    // Then the thread is marked on the cards
    const threadOf = (id: string) => cardById(container, id)?.dataset.thread;
    expect(threadOf("KBN-UI")).toBe("self");
    expect(threadOf("KBN-API")).toBe("waits-on");
    expect(threadOf("KBN-EMBED")).toBe("waited-by");
    expect(threadOf("KBN-PLUGIN")).toBe("dim");
  });

  it("Scenario: the thread follows the focus and lets go when the focus leaves", () => {
    // Given the fixture board
    const { container } = renderBoard();
    const openButton = container.querySelector('[data-id="KBN-BOARD"] button.card-open') as HTMLElement;
    // When the card's open button takes the focus
    fireEvent(openButton, new FocusEvent("focusin", { bubbles: true }));
    // Then its dependency carries the waits-on mark
    expect(cardById(container, "KBN-TICKET")?.dataset.thread).toBe("waits-on");
    // When the focus leaves the card
    fireEvent(openButton, new FocusEvent("focusout", { bubbles: true }));
    // Then no card carries a thread mark
    expect(container.querySelector("[data-thread]")).toBeNull();
  });

  it("Scenario: the card open in the panel holds the thread when nothing is hovered", () => {
    // Given the fixture board with KBN-TICKET open in the panel
    const { container } = renderBoard(snapshot, { openId: "KBN-TICKET" });
    // Then the cards waiting on it are marked and the rest dim, with no pointer anywhere
    expect(cardById(container, "KBN-TICKET")?.dataset.thread).toBe("self");
    expect(cardById(container, "KBN-BOARD")?.dataset.thread).toBe("waited-by");
    expect(cardById(container, "KBN-PARITY")?.dataset.thread).toBe("waited-by");
    expect(cardById(container, "KBN-PLUGIN")?.dataset.thread).toBe("dim");
  });

  it("Scenario: a thread whose card the filters hide lets go of the board", () => {
    // Given the thread held by KBN-TICKET
    const view = renderBoard(snapshot, { openId: "KBN-TICKET" });
    // When a filter hides KBN-TICKET itself
    view.rerender(
      <Board
        snapshot={snapshot}
        filters={{ ...EMPTY_FILTERS, q: "KBN-UI" }}
        changeSeq={0}
        changedIds={new Set()}
        onOpen={() => undefined}
        openId="KBN-TICKET"
      />,
    );
    // Then no card carries a thread mark
    expect(view.container.querySelector("[data-thread]")).toBeNull();
  });
});

describe("Feature: the board's words", () => {
  it("Scenario: an empty board says how to add a card", () => {
    // Given a board with no cards at all
    const { container } = renderBoard(makeSnapshot([]));
    // Then the board says so and where a card comes from
    const line = container.querySelector('[role="status"].board-empty');
    expect(line?.textContent).toContain("No cards yet.");
    expect(line?.textContent).toContain("kanban new");
  });

  it("Scenario: a filter that hides everything offers Clear filters", async () => {
    // Given the fixture board under a search nothing matches
    const onClearFilters = vi.fn();
    renderBoard(snapshot, {
      filters: { ...EMPTY_FILTERS, q: "nothing matches this" },
      onClearFilters,
    });
    // Then the board says no card matches and offers the clear button
    expect(screen.getByText("No cards match these filters.")).toBeTruthy();
    // When the button is clicked
    await userEvent.click(screen.getByRole("button", { name: "Clear filters" }));
    // Then the page is asked to drop the filters
    expect(onClearFilters).toHaveBeenCalled();
  });
});

describe("Feature: the phone board", () => {
  it("Scenario: the tab bar is the labelled flow bar over a strip of every column, none a rail", () => {
    // Given a phone-width window
    stubMatchMedia((query) => query.includes("max-width"));
    const { container } = renderBoard();
    // Then the flow bar's labels read as the column tabs in one tablist
    const tablist = container.querySelector('[role="tablist"]');
    expect(tablist?.getAttribute("aria-label")).toBe("Columns");
    const tabs = [...container.querySelectorAll('[role="tab"]')];
    expect(tabs.map((t) => t.textContent)).toContain("Backlog 3");
    expect(tabs.map((t) => t.textContent)).toContain("Done 3");
    // And the snap strip holds every column open, one screen each
    expect(container.querySelectorAll(".lanes > section.col:not(.rail)")).toHaveLength(7);
  });

  it("Scenario: a tab moves the strip to its column and selects it", async () => {
    // Given a phone-width window
    stubMatchMedia((query) => query.includes("max-width"));
    const { container } = renderBoard();
    const lanes = container.querySelector<HTMLElement>(".lanes");
    if (lanes === null) throw new Error("no lanes");
    Object.defineProperty(lanes, "clientWidth", { value: 390 });
    const scrollTo = vi.fn();
    lanes.scrollTo = scrollTo as unknown as HTMLElement["scrollTo"];
    // When the Done tab is tapped
    const done = [...container.querySelectorAll<HTMLElement>('[role="tab"]')].find(
      (t) => t.textContent === "Done 3",
    );
    if (done === undefined) throw new Error("no Done tab");
    await userEvent.click(done);
    // Then the strip is asked to scroll to the seventh screen and Done is selected
    expect(scrollTo).toHaveBeenCalledWith(expect.objectContaining({ left: 6 * 390 }));
    expect(done.getAttribute("aria-selected")).toBe("true");
  });
});
