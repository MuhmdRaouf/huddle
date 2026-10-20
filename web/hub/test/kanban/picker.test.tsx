// picker.test.tsx — the board picker: a button naming the open board, a popover listing every
// board by name and root, filtered as one types, driven from the keyboard, opened with b.
import { render, screen } from "@testing-library/preact";
import { userEvent } from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { BoardPicker, rootTail } from "../../src/kanban/components/BoardPicker.tsx";

const BOARDS = [
  { key: "a1a1", root: "/repo/alpha", name: "alpha" },
  { key: "b2b2", root: "/code/huddle/web/beta", name: "beta" },
  { key: "c3c3", root: "/repo/alpine", name: "alpine" },
];

describe("Feature: the picker's root line", () => {
  it("Scenario: the board's own .agents/kanban tail is dropped for the name-bearing segments", () => {
    // Given the roots the API sends, the board folder itself included
    // When the tail is taken
    // Then the two segments that name the project show, and a root with nothing else stands whole
    expect(rootTail("/home/me/project/e2e-demo/.agents/kanban")).toBe("project/e2e-demo");
    expect(rootTail("/repo/alpha")).toBe("repo/alpha");
    expect(rootTail("/")).toBe("/");
  });
});

afterEach(() => {
  document.body.innerHTML = "";
});

describe("Feature: the board picker", () => {
  it("Scenario: the button names the open board and opens the list of boards", async () => {
    // Given two boards with the first one open
    const onBoard = vi.fn();
    render(<BoardPicker boards={BOARDS} boardKey="a1a1" onBoard={onBoard} />);
    // When the picker is clicked
    await userEvent.click(screen.getByRole("button", { name: "Board" }));
    // Then the list names every board with the tail of its root beside it
    const list = screen.getByRole("listbox", { name: "Boards" });
    expect(list).toBeTruthy();
    expect(screen.getByRole("option", { name: "alpha repo/alpha" })).toBeTruthy();
    expect(screen.getByRole("option", { name: "beta web/beta" })).toBeTruthy();
  });

  it("Scenario: a board the list does not know keeps its key on the button", () => {
    // Given a gone board's key
    render(<BoardPicker boards={BOARDS} boardKey="dead" onBoard={() => undefined} />);
    // Then the button still says which key is open, so another board is one pick away
    expect(screen.getByRole("button", { name: "Board" }).textContent).toContain("dead");
  });

  it("Scenario: typing filters the list with the fuzzy matcher", async () => {
    // Given the open picker
    render(<BoardPicker boards={BOARDS} boardKey="a1a1" onBoard={() => undefined} />);
    await userEvent.click(screen.getByRole("button", { name: "Board" }));
    // When "alp" is typed into its filter box
    await userEvent.type(screen.getByRole("combobox", { name: "Filter boards" }), "alp");
    // Then alpha and alpine remain, ranked with the substring match first, and beta is gone
    const options = screen.getAllByRole("option");
    expect(options.map((o) => o.getAttribute("aria-label"))).toEqual([
      "alpha repo/alpha",
      "alpine repo/alpine",
    ]);
  });

  it("Scenario: Enter picks the highlighted board and closes", async () => {
    // Given the open picker over three boards
    const onBoard = vi.fn();
    render(<BoardPicker boards={BOARDS} boardKey="a1a1" onBoard={onBoard} />);
    await userEvent.click(screen.getByRole("button", { name: "Board" }));
    // When the second board is walked to and taken with Enter
    await userEvent.type(screen.getByRole("combobox", { name: "Filter boards" }), "{ArrowDown}");
    await userEvent.type(screen.getByRole("combobox", { name: "Filter boards" }), "{Enter}");
    // Then the board's key is handed up and the popover is gone
    expect(onBoard).toHaveBeenCalledWith("b2b2");
    expect(screen.queryByRole("listbox", { name: "Boards" })).toBeNull();
  });

  it("Scenario: b opens it from anywhere except inside an input", async () => {
    // Given the picker closed and a focus outside any input
    render(
      <div>
        <BoardPicker boards={BOARDS} boardKey="a1a1" onBoard={() => undefined} />
        <button type="button">somewhere else</button>
      </div>,
    );
    expect(screen.queryByRole("listbox", { name: "Boards" })).toBeNull();
    // When b is pressed on the body
    await userEvent.keyboard("b");
    // Then the picker opens
    expect(screen.getByRole("listbox", { name: "Boards" })).toBeTruthy();
    // When b is pressed inside the picker's own filter box
    await userEvent.type(screen.getByRole("combobox", { name: "Filter boards" }), "b");
    // Then it filters instead of toggling
    expect(screen.getAllByRole("option").map((o) => o.getAttribute("aria-label"))).toEqual(["beta web/beta"]);
  });

  it("Scenario: Esc closes and hands the focus back to the button", async () => {
    // Given the open picker
    render(<BoardPicker boards={BOARDS} boardKey="a1a1" onBoard={() => undefined} />);
    const button = screen.getByRole("button", { name: "Board" });
    await userEvent.click(button);
    // When Escape is pressed in the filter box
    await userEvent.type(screen.getByRole("combobox", { name: "Filter boards" }), "{Escape}");
    // Then the popover is gone and the button holds the focus again
    expect(screen.queryByRole("listbox", { name: "Boards" })).toBeNull();
    expect(document.activeElement).toBe(button);
  });
});
