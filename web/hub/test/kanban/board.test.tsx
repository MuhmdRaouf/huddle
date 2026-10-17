import { render, screen } from "@testing-library/preact";
import { userEvent } from "@testing-library/user-event";
import { describe, expect, it, vi } from "vite-plus/test";
import { Board } from "../../src/kanban/components/Board.tsx";
import { EMPTY_FILTERS } from "../../src/kanban/store.ts";
import type { Snapshot } from "../../src/kanban/types.ts";
import { makeCard, makeSnapshot, snapshot } from "./helpers.ts";

function renderBoard(snap: Snapshot | null = snapshot, filters = EMPTY_FILTERS) {
  return render(
    <Board snapshot={snap} filters={filters} changeSeq={0} changedIds={new Set()} onOpen={() => undefined} />,
  );
}

const columnsOf = (root: ParentNode) => [...root.querySelectorAll<HTMLElement>("section.col")];

describe("Board", () => {
  it("renders the seven columns in flow order, titled with the count of cards shown", () => {
    const { container } = renderBoard();
    expect(columnsOf(container).map((col) => col.dataset.column)).toEqual([
      "backlog",
      "ready",
      "in-progress",
      "review",
      "testing",
      "blocked",
      "done",
    ]);
    expect(columnsOf(container).map((col) => col.querySelector("h2")?.textContent)).toEqual([
      "Backlog 3",
      "Ready 1",
      "In progress 1",
      "Review 1",
      "Testing 1",
      "Blocked 1",
      "Done 3",
    ]);
  });

  it("lifts open expedite cards into a lane above the columns, out of their own column", () => {
    const { container } = renderBoard();
    const lane = container.querySelector('section[aria-label="Expedite"]');
    expect(lane?.querySelectorAll("article.card")).toHaveLength(1);
    expect(lane?.querySelector<HTMLElement>("article.card")?.dataset.id).toBe("KBN-UI");
    const inProgress = container.querySelector('section[data-column="in-progress"]');
    expect(inProgress?.querySelector('[data-id="KBN-UI"]')).toBeNull();
    expect(inProgress?.querySelectorAll("article.card")).toHaveLength(1);
  });

  it("keeps an expedite card that is done in the done column, out of the lane", () => {
    const snap = makeSnapshot([makeCard({ id: "KBN-DONE", column: "done", expedite: true })]);
    const { container } = renderBoard(snap);
    expect(container.querySelector('section[aria-label="Expedite"]')).toBeNull();
    expect(
      container.querySelector('section[data-column="done"]')?.querySelectorAll("article.card"),
    ).toHaveLength(1);
  });

  it("hides cards the filters remove, including whole columns' worth", () => {
    const onlyUi = { ...EMPTY_FILTERS, q: "KBN-UI" };
    const { container } = renderBoard(snapshot, onlyUi);
    expect(container.querySelectorAll("article.card")).toHaveLength(1);
    expect(columnsOf(container).map((col) => col.querySelector("h2")?.textContent)).toEqual([
      "Backlog 0",
      "Ready 0",
      "In progress 0",
      "Review 0",
      "Testing 0",
      "Blocked 0",
      "Done 0",
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
    const doneCol = () => container.querySelector('section[data-column="done"]');
    const shown = () => [...(doneCol()?.querySelectorAll<HTMLElement>("article.card") ?? [])];
    expect(shown()).toHaveLength(20);
    expect(shown()?.[0]?.dataset.id).toBe("DONE-24");
    expect(shown()?.[19]?.dataset.id).toBe("DONE-05");
    await userEvent.click(screen.getByRole("button", { name: "show all 25" }));
    expect(shown()).toHaveLength(25);
  });

  it("orders done by finishedAt when it disagrees with updatedAt, falling back when empty", () => {
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
    const shown = [
      ...(container
        .querySelector('section[data-column="done"]')
        ?.querySelectorAll<HTMLElement>("article.card") ?? []),
    ];
    expect(shown.map((card) => card.dataset.id)).toEqual(["D-OLDISH", "D-NOFINISH", "D-LAST"]);
  });

  it("shows a loading state while no snapshot has arrived", () => {
    renderBoard(null);
    expect(screen.getByRole("status").textContent).toContain("Loading the board");
  });

  it("renders every card the snapshot holds across its columns", () => {
    const { container } = renderBoard();
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
