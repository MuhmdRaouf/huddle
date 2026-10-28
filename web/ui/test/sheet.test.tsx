import { fireEvent, render, screen } from "@testing-library/preact";
import type { JSX } from "preact";
import { useState } from "preact/hooks";
import { describe, expect, it, vi } from "vite-plus/test";
import { Field } from "../src/field.tsx";
import { Sheet } from "../src/sheet.tsx";

/** A trigger and the sheet it opens, its state owned here. */
function Harness({ onClose }: { onClose: () => void }): JSX.Element {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>
        Details
      </button>
      <Sheet
        open={open}
        title="Task 42"
        onClose={() => {
          setOpen(false);
          onClose();
        }}
        actions={<button type="button">Copy</button>}
      >
        <p>body</p>
      </Sheet>
    </>
  );
}

/** A sheet whose body gains a field only from `named`, the way a page's form mounts when its sheet
 * opens. */
function SheetWithField({ named }: { named: boolean }): JSX.Element {
  return (
    <Sheet open title="Task 42" onClose={() => {}}>
      {named ? <Field label="Name" value="" onInput={() => {}} /> : <p>body</p>}
    </Sheet>
  );
}

describe("Sheet", () => {
  it("opens modally under its title and closes from the close button, focus back on the opener", () => {
    const onClose = vi.fn();
    const view = render(<Harness onClose={onClose} />);
    const dlg = view.baseElement.querySelector("dialog") as HTMLDialogElement;
    expect(dlg.open).toBe(false);
    const opener = screen.getByRole("button", { name: "Details" });
    opener.focus();
    fireEvent.click(opener);
    expect(dlg.open).toBe(true);
    expect(dlg.className).toBe("ui-sheet");
    const title = screen.getByRole("heading", { name: "Task 42" });
    expect(dlg.getAttribute("aria-labelledby")).toBe(title.id);
    expect(screen.getByRole("button", { name: "Copy" })).toBeTruthy();
    expect(screen.getByText("body")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(onClose).toHaveBeenCalledOnce();
    expect(dlg.open).toBe(false);
    expect(document.activeElement).toBe(opener);
  });

  it("draws a grab handle above the head, hidden from the tree", () => {
    const view = render(
      <Sheet open title="Logs" onClose={() => {}}>
        <p>line</p>
      </Sheet>,
    );
    const panel = view.baseElement.querySelector(".ui-sheet-panel") as HTMLElement;
    const handle = panel.querySelector(".ui-sheet-handle");
    expect(handle).toBeTruthy();
    expect(handle?.getAttribute("aria-hidden")).toBe("true");
    // the handle leads the panel, above the head it pulls down
    expect(panel.firstElementChild).toBe(handle);
  });

  it("closes on Escape", () => {
    const onClose = vi.fn();
    const view = render(
      <Sheet open title="Logs" onClose={onClose}>
        <p>line</p>
      </Sheet>,
    );
    const dlg = view.baseElement.querySelector("dialog") as HTMLDialogElement;
    fireEvent(dlg, new Event("cancel", { cancelable: true }));
    expect(onClose).toHaveBeenCalledOnce();
    expect(dlg.open).toBe(false);
  });

  it("closes on a tap outside the panel, not on one inside it", () => {
    const onClose = vi.fn();
    const view = render(
      <Sheet open title="Logs" onClose={onClose}>
        <p>line</p>
      </Sheet>,
    );
    const dlg = view.baseElement.querySelector("dialog") as HTMLDialogElement;
    fireEvent.click(screen.getByText("line"));
    expect(onClose).not.toHaveBeenCalled();
    fireEvent.click(dlg);
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("an open sheet carries data-ui-layer as the first layer, uncovered", () => {
    const view = render(
      <Sheet open title="Logs" onClose={() => {}}>
        <p>line</p>
      </Sheet>,
    );
    const dlg = view.baseElement.querySelector("dialog") as HTMLDialogElement;
    expect(dlg.getAttribute("data-ui-layer")).toBe("1");
    expect(dlg.hasAttribute("data-ui-covered")).toBe(false);
  });

  it("closes when open turns false", () => {
    const onClose = vi.fn();
    const view = render(
      <Sheet open title="Logs" onClose={onClose}>
        <p>line</p>
      </Sheet>,
    );
    view.rerender(
      <Sheet open={false} title="Logs" onClose={onClose}>
        <p>line</p>
      </Sheet>,
    );
    expect((view.baseElement.querySelector("dialog") as HTMLDialogElement).open).toBe(false);
  });

  it("a field mounted after the first paints keeps an id of its own, not the sheet title's", () => {
    const view = render(<SheetWithField named={false} />);
    // an intervening paint, the way the live page paints between mount and the sheet opening
    view.rerender(<SheetWithField named={false} />);
    view.rerender(<SheetWithField named={true} />);
    const title = screen.getByRole("heading", { name: "Task 42" });
    const input = screen.getByLabelText("Name");
    expect(input.tagName).toBe("INPUT");
    expect((input as HTMLInputElement).id).not.toBe(title.id);
    // one element on the page carries the input's id, so the label resolves to the input
    expect(view.baseElement.querySelectorAll(`[id="${(input as HTMLInputElement).id}"]`)).toHaveLength(1);
  });
});
