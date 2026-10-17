import { render, screen } from "@testing-library/preact";
import { userEvent } from "@testing-library/user-event";
import { describe, expect, it } from "vite-plus/test";
import { Board } from "../src/components/Board.tsx";
import { EMPTY_FILTERS } from "../src/store.ts";
import { snapshot } from "./helpers.ts";
import { stubMatchMedia } from "./ui/match-media.ts";

const narrow = (query: string): boolean => query === "(max-width: 639px)";

function renderBoard(snap = snapshot) {
  return render(
    <Board
      snapshot={snap}
      filters={EMPTY_FILTERS}
      changeSeq={0}
      changedIds={new Set()}
      onOpen={() => undefined}
    />,
  );
}

describe("the responsive board", () => {
  it("shows every column side by side on a wide screen", () => {
    const { container } = renderBoard();
    expect(container.querySelector('[role="tablist"]')).toBeNull();
    expect(container.querySelectorAll("section.col")).toHaveLength(7);
  });

  it("switches to a tab bar of columns with counts below 640 px, one column at a time", async () => {
    stubMatchMedia(narrow);
    const { container } = renderBoard();
    const board = container.querySelector(".board") as HTMLElement;
    expect(board.dataset.layout).toBe("tabs");
    const tabs = screen.getAllByRole("tab");
    expect(tabs.map((tab) => tab.textContent)).toEqual([
      "Backlog 3",
      "Ready 1",
      "In progress 1",
      "Review 1",
      "Testing 1",
      "Blocked 1",
      "Done 3",
    ]);
    expect(container.querySelectorAll("section.col")).toHaveLength(1);
    expect(container.querySelector<HTMLElement>("section.col")?.dataset.column).toBe("backlog");
    await userEvent.click(tabs[6] as HTMLElement);
    expect(container.querySelector<HTMLElement>("section.col")?.dataset.column).toBe("done");
    expect(container.querySelector("section.col")?.querySelectorAll("article.card")).toHaveLength(3);
  });

  it("starts the phone layout on the first column holding cards", () => {
    stubMatchMedia(narrow);
    const noBacklog = { ...snapshot, cards: snapshot.cards.filter((c) => c.column !== "backlog") };
    const { container } = renderBoard(noBacklog);
    expect(container.querySelector<HTMLElement>("section.col")?.dataset.column).toBe("ready");
  });

  it("keeps a 360 px viewport on the tab strip, with no column laid out beside another", () => {
    stubMatchMedia(narrow);
    const { container } = renderBoard();
    const board = container.querySelector(".board") as HTMLElement;
    expect(board.dataset.layout).toBe("tabs");
    // the tab strip is the page's own horizontally scrollable strip; one column means nothing sits
    // beside anything, so a 360 px viewport never scrolls the page sideways
    const strip = board.querySelector('[role="tablist"]');
    expect(strip).not.toBeNull();
    expect(strip?.classList.contains("tabs")).toBe(true);
    expect(container.querySelectorAll("section.col")).toHaveLength(1);
  });
});
