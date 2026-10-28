import { fireEvent, render, screen } from "@testing-library/preact";
import { describe, expect, it, vi } from "vite-plus/test";
import { Empty } from "../src/empty.tsx";

describe("Empty", () => {
  it("renders the line", () => {
    const view = render(<Empty>No sessions yet.</Empty>);
    expect(view.container.firstElementChild?.className).toBe("ui-empty");
    expect(screen.getByText("No sessions yet.")).toBeTruthy();
    view.unmount();
  });

  it("renders the icon tile only when an icon is given", () => {
    const bare = render(<Empty>Nothing here.</Empty>);
    expect(bare.container.querySelector(".ui-empty-icon")).toBeNull();
    bare.unmount();

    const view = render(<Empty icon={<b>ghost</b>}>Nothing here.</Empty>);
    const tile = view.container.querySelector(".ui-empty-icon");
    expect(tile?.textContent).toBe("ghost");
  });

  it("renders the action as a button and runs it on click", () => {
    const onClick = vi.fn();
    render(<Empty action={{ label: "Add a channel", onClick }}>No channels yet.</Empty>);
    fireEvent.click(screen.getByRole("button", { name: "Add a channel" }));
    expect(onClick).toHaveBeenCalledOnce();
  });
});
