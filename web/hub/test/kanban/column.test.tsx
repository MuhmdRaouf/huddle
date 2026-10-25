// column.test.tsx — a collapsed column is a rail: one button whose accessible name carries the
// title and the count, the count being the column's whole count when the shown cards are capped
// and the cards' own number otherwise.

import { fireEvent, render, screen } from "@testing-library/preact";
import { describe, expect, it, vi } from "vite-plus/test";
import { Column } from "../../src/kanban/components/Column.tsx";
import { makeCard } from "./helpers.ts";

function rail(count?: number) {
  const onExpand = vi.fn();
  render(
    <Column
      id="done"
      title="Done"
      cards={[makeCard({ id: "A-1" }), makeCard({ id: "A-2" })]}
      changeSeq={0}
      changedIds={new Set()}
      onOpen={() => {}}
      collapsed
      count={count}
      onExpand={onExpand}
    />,
  );
  return onExpand;
}

describe("a collapsed column", () => {
  it("is a rail named by its title and its whole count, and a click opens it", () => {
    const onExpand = rail(14);
    const button = screen.getByRole("button", { name: "Done, 14 cards, show column" });
    expect(button.getAttribute("aria-expanded")).toBe("false");
    fireEvent.click(button);
    expect(onExpand).toHaveBeenCalledTimes(1);
  });

  it("counts its own cards when no whole count is handed in", () => {
    rail();
    expect(screen.getByRole("button", { name: "Done, 2 cards, show column" })).toBeTruthy();
  });
});
