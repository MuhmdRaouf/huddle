import { fireEvent, render, screen, within } from "@testing-library/preact";
import type { JSX } from "preact";
import { useState } from "preact/hooks";
import { describe, expect, it, vi } from "vite-plus/test";
import { Dialog } from "../src/dialog.tsx";
import { Sheet } from "../src/sheet.tsx";

/** A page with a trigger button and a dialog the button opens. */
function Harness({ initial = false }: { initial?: boolean | undefined }): JSX.Element {
  const [open, setOpen] = useState(initial);
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>
        trigger
      </button>
      <Dialog open={open} onClose={() => setOpen(false)} title="Rename">
        <p>body text</p>
      </Dialog>
    </>
  );
}

describe("Dialog", () => {
  it("opens modally as a ui-dialog with its title and body, and closes from the header button", () => {
    const onClose = vi.fn();
    const view = render(
      <Dialog open onClose={onClose} title="Rename">
        <p>body text</p>
      </Dialog>,
    );
    const dlg = view.baseElement.querySelector("dialog");
    expect(dlg?.className).toBe("ui-dialog");
    expect(dlg?.open).toBe(true);
    expect(screen.getByText("Rename")).toBeTruthy();
    expect(screen.getByText("body text")).toBeTruthy();
    expect(dlg?.getAttribute("aria-labelledby")).toBe(screen.getByRole("heading", { name: "Rename" }).id);
    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(onClose).toHaveBeenCalled();
    expect(dlg?.open).toBe(false);
  });

  it("gives its title an id of its own, so two open dialogs never share one", () => {
    const view = render(
      <>
        <Dialog open onClose={() => {}} title="One">
          <p>1</p>
        </Dialog>
        <Dialog open onClose={() => {}} title="Two">
          <p>2</p>
        </Dialog>
      </>,
    );
    const dlgs = [...view.baseElement.querySelectorAll("dialog")];
    expect(dlgs.length).toBe(2);
    const ha = screen.getByRole("heading", { name: "One" });
    const hb = screen.getByRole("heading", { name: "Two" });
    expect(dlgs[0]?.getAttribute("aria-labelledby")).toBe(ha.id);
    expect(dlgs[1]?.getAttribute("aria-labelledby")).toBe(hb.id);
    expect(ha.id).not.toBe(hb.id);
  });

  it("renders no header and no labelledby without a title", () => {
    const view = render(
      <Dialog open onClose={() => {}}>
        <p>body</p>
      </Dialog>,
    );
    expect(view.baseElement.querySelector(".ui-dialog-head")).toBeNull();
    expect(view.baseElement.querySelector("dialog")?.getAttribute("aria-labelledby")).toBeNull();
  });

  it("closes on a scrim click and returns focus to the opener", async () => {
    const view = render(<Harness />);
    const trigger = screen.getByText("trigger") as HTMLElement;
    trigger.focus();
    fireEvent.click(trigger); // opens
    const dlg = view.baseElement.querySelector("dialog");
    expect(dlg?.open).toBe(true);
    fireEvent.click(dlg as Element); // the click that reaches the dialog is the scrim
    expect(dlg?.open).toBe(false);
    await new Promise((r) => setTimeout(r, 0));
    expect(document.activeElement).toBe(trigger);
  });

  it("keeps a click inside the box from reading as the scrim, and a second scrim click stays quiet", () => {
    const onClose = vi.fn();
    const view = render(
      <Dialog open onClose={onClose}>
        <p>body text</p>
      </Dialog>,
    );
    const dlg = view.baseElement.querySelector("dialog") as HTMLDialogElement;
    fireEvent.click(screen.getByText("body text")); // inside the box, not on the scrim
    expect(dlg.open).toBe(true);
    expect(onClose).not.toHaveBeenCalled();
    fireEvent.click(dlg); // the scrim closes it
    expect(onClose).toHaveBeenCalledTimes(1);
    fireEvent.click(dlg); // already closed: nothing more to do
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("closes on Escape", () => {
    const onClose = vi.fn();
    const view = render(
      <Dialog open onClose={onClose}>
        <p>body</p>
      </Dialog>,
    );
    fireEvent.keyDown(view.baseElement.querySelector("dialog") as Element, { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("keeps the focus in a dialog that is still open when another one closes", async () => {
    const view = render(<Harness />);
    const trigger = screen.getByText("trigger") as HTMLElement;
    trigger.focus();
    fireEvent.click(trigger);
    const other = document.createElement("dialog");
    document.body.append(other);
    other.showModal();
    other.focus();
    fireEvent.click(view.baseElement.querySelector("dialog") as Element);
    await new Promise((r) => setTimeout(r, 0));
    expect(document.activeElement).toBe(other);
    other.close();
    other.remove();
  });

  it("flips open off through the native close and stays quiet when already closed", () => {
    const onClose = vi.fn();
    const view = render(
      <Dialog open onClose={onClose}>
        <p>body</p>
      </Dialog>,
    );
    view.rerender(
      <Dialog open={false} onClose={onClose}>
        <p>body</p>
      </Dialog>,
    );
    expect(view.baseElement.querySelector("dialog")?.open).toBe(false);
    expect(onClose).toHaveBeenCalledTimes(1);
    view.rerender(
      <Dialog open={false} onClose={onClose}>
        <p>body</p>
      </Dialog>,
    );
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("takes the caller's labelledby over the title default", () => {
    const view = render(
      <Dialog open onClose={() => {}} labeledBy="td-title">
        <p>body</p>
      </Dialog>,
    );
    expect(view.baseElement.querySelector("dialog")?.getAttribute("aria-labelledby")).toBe("td-title");
  });

  it("paints the close button as a ghost icon button on the tokens", () => {
    render(
      <Dialog open onClose={() => {}} title="Rename">
        <p>body</p>
      </Dialog>,
    );
    const close = screen.getByRole("button", { name: "Close" });
    expect(close.getAttribute("class")).toBe("ui-btn ui-btn-ghost ui-btn-icon");
  });

  // A page like the tokens gallery: the sheet stays mounted while the dialog opens over it, so the
  // button that opened the dialog keeps its place and takes the focus back on close.
  function SheetHarness(): JSX.Element {
    const [dlgOpen, setDlgOpen] = useState(false);
    return (
      <>
        <Sheet open title="Task 42" onClose={() => {}}>
          <button type="button" onClick={() => setDlgOpen(true)}>
            Open dialog
          </button>
        </Sheet>
        {dlgOpen ? (
          <Dialog open title="Remove channel" onClose={() => setDlgOpen(false)}>
            <p>dialog body</p>
          </Dialog>
        ) : null}
      </>
    );
  }

  it("closing above an open sheet hands the focus back into the sheet", async () => {
    const view = render(<SheetHarness />);
    const sheet = view.baseElement.querySelector("dialog.ui-sheet") as HTMLDialogElement;
    const opener = within(sheet).getByRole("button", { name: "Open dialog" });
    opener.focus();
    fireEvent.click(opener);
    const dlg = view.baseElement.querySelector("dialog.ui-dialog") as HTMLDialogElement;
    expect(dlg.open).toBe(true);
    fireEvent.keyDown(dlg, { key: "Escape" });
    expect(dlg.open).toBe(false);
    await new Promise((r) => setTimeout(r, 0));
    expect(document.activeElement).toBe(opener);
  });

  it("opened over an open sheet, marks the sheet covered and uncovers it when it closes", () => {
    const view = render(
      <>
        <Sheet open title="Task 42" onClose={() => {}}>
          <p>sheet body</p>
        </Sheet>
        <Dialog open onClose={() => {}} title="Remove channel">
          <p>dialog body</p>
        </Dialog>
      </>,
    );
    const sheet = view.baseElement.querySelector("dialog.ui-sheet") as HTMLDialogElement;
    const dlg = view.baseElement.querySelector("dialog.ui-dialog") as HTMLDialogElement;
    expect(sheet.open).toBe(true);
    expect(dlg.open).toBe(true);
    expect(sheet.hasAttribute("data-ui-covered")).toBe(true);
    expect(dlg.hasAttribute("data-ui-covered")).toBe(false);
    fireEvent.click(within(dlg).getByRole("button", { name: "Close" }));
    expect(dlg.open).toBe(false);
    expect(sheet.hasAttribute("data-ui-covered")).toBe(false);
  });

  it("unmounts cleanly while open", () => {
    const view = render(
      <Dialog open onClose={() => {}}>
        <p>body</p>
      </Dialog>,
    );
    expect(() => view.unmount()).not.toThrow();
  });
});
