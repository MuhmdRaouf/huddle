// flowbar.test.tsx — the flow bar: one segment per non-done column with cards, sized by its
// share of the board, and the counts as plain labels under it that hand their column up.
import { render, screen } from "@testing-library/preact";
import { userEvent } from "@testing-library/user-event";
import { describe, expect, it, vi } from "vite-plus/test";
import { FlowBar } from "../../src/kanban/components/FlowBar.tsx";
import { countColumns, snapshot } from "./helpers.ts";

describe("Feature: the flow bar", () => {
  it("Scenario: one segment per non-done column with cards, sized by its share", () => {
    // Given the fixture board's columns, every one of them holding cards
    const columns = countColumns(snapshot.cards);
    // When the flow bar renders over them
    const { container } = render(<FlowBar columns={columns} onPick={() => undefined} />);
    // Then the six non-done columns each carry a segment and done carries none
    const segs = [...container.querySelectorAll<HTMLElement>(".flow-seg")];
    expect(segs.map((s) => s.dataset.column)).toEqual([
      "backlog",
      "ready",
      "in-progress",
      "review",
      "testing",
      "blocked",
    ]);
    // and the segments read as one picture naming every column and its count, done included
    expect(
      screen.getByRole("img", {
        name: "Backlog 3, Ready 1, In progress 2, Review 1, Testing 1, Blocked 1, Done 3",
      }),
    ).toBeTruthy();
    // and each segment grows with its column's count
    expect(segs.map((s) => s.style.flexGrow)).toEqual(["3", "1", "2", "1", "1", "1"]);
  });

  it("Scenario: a column with no cards has no segment", () => {
    // Given a board whose review column sits empty
    const columns = countColumns(snapshot.cards).map((c) => (c.id === "review" ? { ...c, count: 0 } : c));
    // When the flow bar renders
    const { container } = render(<FlowBar columns={columns} onPick={() => undefined} />);
    // Then review has no segment and the rest keep theirs
    const segs = [...container.querySelectorAll<HTMLElement>(".flow-seg")];
    expect(segs.map((s) => s.dataset.column)).not.toContain("review");
    expect(segs).toHaveLength(5);
  });

  it("Scenario: the counts sit under the bar as plain labels, done included", () => {
    // Given the fixture board's columns
    // When the flow bar renders
    render(<FlowBar columns={countColumns(snapshot.cards)} onPick={() => undefined} />);
    // Then every column reads as a label button, zeros and done included
    expect(screen.getByRole("button", { name: "Backlog 3" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Review 1" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Done 3" })).toBeTruthy();
  });

  it("Scenario: clicking a label hands its column up", async () => {
    // Given the flow bar over the fixture's columns
    const onPick = vi.fn();
    render(<FlowBar columns={countColumns(snapshot.cards)} onPick={onPick} />);
    // When the In progress label is clicked
    await userEvent.click(screen.getByRole("button", { name: "In progress 2" }));
    // Then the board learns that column was asked for
    expect(onPick).toHaveBeenCalledWith("in-progress");
  });
});
