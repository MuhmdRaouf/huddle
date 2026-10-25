import { render, screen } from "@testing-library/preact";
import { userEvent } from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { Board } from "../../src/kanban/components/Board.tsx";
import { clearFilters, EMPTY_FILTERS } from "../../src/kanban/store.ts";
import type { Snapshot } from "../../src/kanban/types.ts";
import { makeCard, makeSnapshot, snapshot } from "./helpers.ts";
import { stubMatchMedia } from "./match-media.ts";

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderBoard(snap: Snapshot | null = snapshot, filters = EMPTY_FILTERS) {
  return render(
    <Board snapshot={snap} filters={filters} changeSeq={0} changedIds={new Set()} onOpen={() => undefined} />,
  );
}

const columnsOf = (root: ParentNode) => [...root.querySelectorAll<HTMLElement>("section.col")];

/** Opens a column sitting behind its rail; tests name the column they need open. */
async function openRail(container: Element, column: string) {
  const rail = container.querySelector<HTMLButtonElement>(
    `section[data-column="${column}"] button.rail-button`,
  );
  if (rail === null) throw new Error(`no ${column} rail`);
  await userEvent.click(rail);
}

describe("Board", () => {
  it("renders the six open columns in flow order and done as a rail beside them", () => {
    const { container } = renderBoard();
    const openCols = columnsOf(container).filter((col) => col.querySelector("h2") !== null);
    expect(openCols.map((col) => col.dataset.column)).toEqual([
      "backlog",
      "ready",
      "in-progress",
      "review",
      "testing",
      "blocked",
    ]);
    expect(openCols.map((col) => col.querySelector("h2")?.textContent)).toEqual([
      "Backlog 3",
      "Ready 1",
      "In progress 2",
      "Review 1",
      "Testing 1",
      "Blocked 1",
    ]);
    // done stands beside them as a rail carrying its count
    expect(container.querySelector('section[data-column="done"] h2')).toBeNull();
    const rail = container.querySelector<HTMLButtonElement>('section[data-column="done"] .rail-button');
    expect(rail?.textContent).toContain("Done");
    expect(rail?.querySelector(".ui-sr")?.textContent).toBe("Done, 3 cards, show column");
  });

  it("keeps expedite cards inside their own column, first, with the mark, and counts them", () => {
    // four expedite cards and one plain card in testing; the plain one comes first in the snapshot
    const snap = makeSnapshot([
      makeCard({ id: "PLAIN-1", column: "testing", expedite: false }),
      ...[1, 2, 3, 4].map((i) => makeCard({ id: `EXP-${i}`, column: "testing", expedite: true })),
    ]);
    const { container } = renderBoard(snap);
    const testing = () => container.querySelector('section[data-column="testing"]');
    expect(testing()?.querySelector("h2")?.textContent).toBe("Testing 5");
    expect(screen.getByRole("button", { name: "Testing 5" })).toBeDefined();
    const shown = [...(testing()?.querySelectorAll<HTMLElement>("article.card") ?? [])];
    expect(shown.map((card) => card.dataset.id)).toEqual(["EXP-1", "EXP-2", "EXP-3", "EXP-4", "PLAIN-1"]);
    expect(shown.map((card) => card.querySelector(".b-expedite") !== null)).toEqual([
      true,
      true,
      true,
      true,
      false,
    ]);
    // no strip above the columns holds them
    expect(container.querySelector('section[aria-label="Expedite"], section.expedite')).toBeNull();
  });

  it("counts expedite cards in their own column's tab on a phone", () => {
    stubMatchMedia((query) => query.includes("max-width"));
    const snap = makeSnapshot([
      makeCard({ id: "PLAIN-1", column: "testing", expedite: false }),
      ...[1, 2, 3, 4].map((i) => makeCard({ id: `EXP-${i}`, column: "testing", expedite: true })),
    ]);
    const { container } = renderBoard(snap);
    expect(screen.getByRole("tab", { name: "Testing 5" }).getAttribute("aria-selected")).toBe("true");
    const shown = [...container.querySelectorAll<HTMLElement>('section[data-column="testing"] article.card')];
    expect(shown.map((card) => card.dataset.id)).toEqual(["EXP-1", "EXP-2", "EXP-3", "EXP-4", "PLAIN-1"]);
    expect(container.querySelector('section[aria-label="Expedite"], section.expedite')).toBeNull();
  });

  it("keeps an expedite card that is done in the done column", async () => {
    const snap = makeSnapshot([makeCard({ id: "KBN-DONE", column: "done", expedite: true })]);
    const { container } = renderBoard(snap);
    await openRail(container, "done");
    expect(
      container.querySelector('section[data-column="done"]')?.querySelectorAll("article.card"),
    ).toHaveLength(1);
  });

  it("hides cards the filters remove; the columns they empty become rails", () => {
    const onlyUi = { ...EMPTY_FILTERS, q: "KBN-UI" };
    const { container } = renderBoard(snapshot, onlyUi);
    // the one visible card is KBN-UI in in progress, so every other column is a rail
    expect(container.querySelectorAll("article.card")).toHaveLength(1);
    expect(container.querySelector('section[data-column="in-progress"] h2')?.textContent).toBe(
      "In progress 1",
    );
    const rails = [...container.querySelectorAll<HTMLButtonElement>("button.rail-button")];
    expect(rails.map((rail) => rail.querySelector(".ui-sr")?.textContent)).toEqual([
      "Backlog, 0 cards, show column",
      "Ready, 0 cards, show column",
      "Review, 0 cards, show column",
      "Testing, 0 cards, show column",
      "Blocked, 0 cards, show column",
      "Done, 0 cards, show column",
    ]);
  });

  it("caps done at the latest 20 by updatedAt, then shows all on demand", async () => {
    const done = Array.from({ length: 25 }, (_, i) =>
      makeCard({
        id: `DONE-${String(i).padStart(2, "0")}`,
        column: "done",
        expedite: false,
        updatedAt: new Date(Date.UTC(2026, 9, 1, i)).toISOString(),
      }),
    );
    const { container } = renderBoard(makeSnapshot(done));
    await openRail(container, "done");
    const doneCol = () => container.querySelector('section[data-column="done"]');
    const shown = () => [...(doneCol()?.querySelectorAll<HTMLElement>("article.card") ?? [])];
    expect(shown()).toHaveLength(20);
    expect(shown()?.[0]?.dataset.id).toBe("DONE-24");
    expect(shown()?.[19]?.dataset.id).toBe("DONE-05");
    // the heading counts the whole column; the button names what opening it adds up to
    expect(doneCol()?.querySelector("h2")?.textContent).toBe("Done 25");
    await userEvent.click(screen.getByRole("button", { name: "Show all 25" }));
    expect(shown()).toHaveLength(25);
  });

  it("orders done by finishedAt when it disagrees with updatedAt, falling back when empty", async () => {
    const done = [
      makeCard({
        id: "D-OLDISH",
        column: "done",
        updatedAt: "2026-10-05T00:00:00+03:00",
        finishedAt: "2026-10-12T00:00:00+03:00",
      }),
      // the newest updatedAt of the three, yet finished last: finishedAt wins
      makeCard({
        id: "D-LAST",
        column: "done",
        updatedAt: "2026-10-13T00:00:00+03:00",
        finishedAt: "2026-10-08T00:00:00+03:00",
      }),
      // no finishedAt: its updatedAt slots it between the two
      makeCard({
        id: "D-NOFINISH",
        column: "done",
        updatedAt: "2026-10-10T00:00:00+03:00",
      }),
    ];
    const { container } = renderBoard(makeSnapshot(done));
    await openRail(container, "done");
    const shown = [
      ...(container
        .querySelector('section[data-column="done"]')
        ?.querySelectorAll<HTMLElement>("article.card") ?? []),
    ];
    expect(shown.map((card) => card.dataset.id)).toEqual(["D-OLDISH", "D-NOFINISH", "D-LAST"]);
  });

  it("shows a skeleton while no snapshot has arrived", () => {
    const { container } = renderBoard(null);
    const loading = container.querySelector('[data-layout="loading"] [role="status"]');
    expect(loading?.getAttribute("aria-label")).toBe("Loading the board");
    expect(loading?.querySelector(".kanban-skeleton")).toBeTruthy();
  });

  it("tabs its columns on a phone, the tabs the flow bar's own labels", () => {
    stubMatchMedia((query) => query.includes("max-width"));
    const { container } = renderBoard();
    const tab = container.querySelector('[role="tab"]') as HTMLElement;
    expect(tab.textContent).toBe("Backlog 3");
    expect(tab.getAttribute("aria-selected")).toBe("true");
  });

  it("renders every card the snapshot holds across its columns once done is opened", async () => {
    const { container } = renderBoard();
    await openRail(container, "done");
    expect(container.querySelectorAll("article.card").length).toBe(snapshot.cards.length);
  });

  it("carries a card's click to its parent with the card's id", async () => {
    const onOpen = vi.fn((_id: string) => undefined);
    render(
      <Board
        snapshot={snapshot}
        filters={EMPTY_FILTERS}
        changeSeq={0}
        changedIds={new Set()}
        onOpen={onOpen}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: /^KBN-PLUGIN:/ }));
    expect(onOpen).toHaveBeenCalledWith("KBN-PLUGIN", expect.any(HTMLButtonElement));
  });
});

describe("collapsing a column by hand", () => {
  /** The heading's collapse button, or a throw: tests name the column they collapse. */
  function collapseBtn(container: Element, column: string) {
    const btn = container.querySelector<HTMLButtonElement>(
      `section[data-column="${column}"] button.col-collapse`,
    );
    if (btn === null) throw new Error(`no ${column} collapse button`);
    return btn;
  }

  it("shows a collapse button on an open column with cards, and its rail reopens it", async () => {
    const { container } = renderBoard();
    const collapse = collapseBtn(container, "ready");
    expect(collapse.getAttribute("aria-label")).toBe("Collapse Ready");
    await userEvent.click(collapse);
    expect(container.querySelector('section[data-column="ready"] h2')).toBeNull();
    const rail = container.querySelector<HTMLButtonElement>(
      'section[data-column="ready"] button.rail-button',
    );
    if (rail === null) throw new Error("no ready rail");
    expect(rail.querySelector(".ui-sr")?.textContent).toBe("Ready, 1 cards, show column");
    await userEvent.click(rail);
    const ready = container.querySelector('section[data-column="ready"]');
    expect(ready?.querySelector("h2")?.textContent).toBe("Ready 1");
    expect(ready?.querySelectorAll("article.card")).toHaveLength(1);
  });

  it("collapses an empty column it had opened from its rail", async () => {
    const onlyUi = { ...EMPTY_FILTERS, q: "KBN-UI" };
    const { container } = renderBoard(snapshot, onlyUi);
    await openRail(container, "backlog");
    const collapse = collapseBtn(container, "backlog");
    expect(collapse.getAttribute("aria-label")).toBe("Collapse Backlog");
    await userEvent.click(collapse);
    expect(
      container.querySelector<HTMLButtonElement>('section[data-column="backlog"] button.rail-button .ui-sr')
        ?.textContent,
    ).toBe("Backlog, 0 cards, show column");
  });

  it("collapses done again after opening it from its rail", async () => {
    const { container } = renderBoard();
    await openRail(container, "done");
    const collapse = collapseBtn(container, "done");
    expect(collapse.getAttribute("aria-label")).toBe("Collapse Done");
    await userEvent.click(collapse);
    const rail = container.querySelector<HTMLButtonElement>('section[data-column="done"] button.rail-button');
    expect(rail?.querySelector(".ui-sr")?.textContent).toBe("Done, 3 cards, show column");
  });

  it("renders no collapse button on a phone", () => {
    stubMatchMedia((query) => query.includes("max-width"));
    const { container } = renderBoard();
    expect(container.querySelector("button.col-collapse")).toBeNull();
  });

  it("clears a hand collapse when the filters change", async () => {
    const view = render(
      <Board
        snapshot={snapshot}
        filters={EMPTY_FILTERS}
        changeSeq={0}
        changedIds={new Set()}
        onOpen={() => undefined}
      />,
    );
    await userEvent.click(collapseBtn(view.container, "ready"));
    expect(view.container.querySelector('section[data-column="ready"] h2')).toBeNull();
    view.rerender(
      <Board
        snapshot={snapshot}
        filters={{ ...EMPTY_FILTERS, q: "KBN-API" }}
        changeSeq={0}
        changedIds={new Set()}
        onOpen={() => undefined}
      />,
    );
    const ready = view.container.querySelector('section[data-column="ready"]');
    expect(ready?.querySelector("h2")?.textContent).toBe("Ready 1");
    expect(ready?.querySelectorAll("article.card")).toHaveLength(1);
  });
});

describe("a board hiding its done cards", () => {
  const openCard = makeCard({ id: "OPEN-1", column: "in-progress", expedite: false });
  const doneCards = Array.from({ length: 59 }, (_, i) =>
    makeCard({ id: `DONE-${i}`, column: "done", expedite: false }),
  );
  const heavy = makeSnapshot([openCard, ...doneCards]);
  const hiding = { ...EMPTY_FILTERS, hideDone: true };

  it("keeps Done's whole count, says how many are hidden and shows them in one tap", async () => {
    const onShowDone = vi.fn();
    const { container } = render(
      <Board
        snapshot={heavy}
        filters={hiding}
        changeSeq={0}
        changedIds={new Set()}
        onOpen={() => undefined}
        onShowDone={onShowDone}
      />,
    );
    expect(screen.getByRole("button", { name: "Done 59" })).toBeTruthy();
    expect(container.querySelector('section[data-column="done"] .rail-button .ui-sr')?.textContent).toBe(
      "Done, 59 cards, show column",
    );
    expect(screen.getByRole("status").textContent).toContain("59 done hidden");
    expect(container.querySelectorAll("article.card")).toHaveLength(1);
    await userEvent.click(screen.getByRole("button", { name: "Show done" }));
    expect(onShowDone).toHaveBeenCalledTimes(1);
  });

  it("says nothing about hidden cards while done is shown", () => {
    renderBoard(heavy, EMPTY_FILTERS);
    expect(screen.queryByText(/done hidden/)).toBeNull();
    expect(screen.queryByRole("button", { name: "Show done" })).toBeNull();
  });

  it("counts only the hidden done cards the other filters let through", () => {
    const narrowed = makeSnapshot([
      openCard,
      makeCard({ id: "DONE-A", column: "done", title: "Alpha", summary: "", tags: [] }),
      makeCard({ id: "DONE-B", column: "done", title: "Beta", summary: "", tags: [] }),
    ]);
    // the search leaves no card on the board, and the line still says what it hides
    renderBoard(narrowed, { ...hiding, q: "DONE-A" });
    expect(screen.getByText(/1 done hidden/)).toBeTruthy();
  });

  it("turns hide-done off when the filters are cleared", () => {
    expect(clearFilters()).toEqual(EMPTY_FILTERS);
    expect(clearFilters().hideDone).toBe(false);
  });
});
