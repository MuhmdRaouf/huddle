import { fireEvent, render, screen, within } from "@testing-library/preact";
import type { JSX } from "preact";
import { useState } from "preact/hooks";
import { describe, expect, it, vi } from "vite-plus/test";
import { Dialog } from "../src/dialog.tsx";
import { SlideOver } from "../src/slide-over.tsx";

/** A page with a trigger button and a slide-over the button opens. */
function Harness(): JSX.Element {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>
        open it
      </button>
      <SlideOver open={open} onClose={() => setOpen(false)} label="Request" header={<h2>Opus</h2>}>
        <p>body</p>
      </SlideOver>
    </>
  );
}

describe("SlideOver", () => {
  it("opens as the kit's own layer dialog, with no daisyUI class anywhere in it", () => {
    const { baseElement } = render(
      <SlideOver open onClose={() => {}} label="Request" header="h" tabs={<div>tabs</div>}>
        b
      </SlideOver>,
    );
    const dlg = baseElement.querySelector("dialog") as HTMLDialogElement;
    expect(dlg.className).toBe("ui-slide-over");
    expect(dlg.open).toBe(true);
    const daisy = [...dlg.querySelectorAll("[class]"), dlg].flatMap((el) =>
      [...el.classList].filter((c) => /modal|^btn$|glass/.test(c)),
    );
    expect(daisy).toEqual([]);
    expect(dlg.querySelector("[data-slide-over]")).toBeTruthy();
    expect(dlg.querySelector("[data-slide-over-body]")).toBeTruthy();
  });

  it("opens through the layer rule and carries data-ui-layer", () => {
    const { baseElement } = render(
      <SlideOver open onClose={() => {}} label="Request" header="h">
        b
      </SlideOver>,
    );
    expect(baseElement.querySelector("dialog")?.getAttribute("data-ui-layer")).toBe("1");
  });

  it("a dialog opened over it covers it, and closing the dialog uncovers it", () => {
    const { baseElement } = render(
      <>
        <SlideOver open onClose={() => {}} label="Request" header="h">
          b
        </SlideOver>
        <Dialog open onClose={() => {}} title="Remove channel">
          dialog body
        </Dialog>
      </>,
    );
    const so = baseElement.querySelector("dialog.ui-slide-over") as HTMLDialogElement;
    const dlg = baseElement.querySelector("dialog.ui-dialog") as HTMLDialogElement;
    expect(so.open).toBe(true);
    expect(dlg.open).toBe(true);
    expect(so.hasAttribute("data-ui-covered")).toBe(true);
    expect(dlg.hasAttribute("data-ui-covered")).toBe(false);
    fireEvent.click(within(dlg).getByRole("button", { name: "Close" }));
    expect(dlg.open).toBe(false);
    expect(so.hasAttribute("data-ui-covered")).toBe(false);
  });

  it("closes on Escape and on a backdrop click through one onClose", () => {
    const onClose = vi.fn();
    const { baseElement, rerender } = render(
      <SlideOver open onClose={onClose} label="Request" header="h">
        b
      </SlideOver>,
    );
    const dlg = baseElement.querySelector("dialog") as HTMLDialogElement;
    fireEvent(dlg, new Event("cancel", { cancelable: true })); // Escape
    expect(dlg.open).toBe(false);
    rerender(
      <SlideOver open={false} onClose={onClose} label="Request" header="h">
        b
      </SlideOver>,
    );
    rerender(
      <SlideOver open onClose={onClose} label="Request" header="h">
        b
      </SlideOver>,
    );
    expect(dlg.open).toBe(true);
    fireEvent.click(dlg); // the click that reaches the element is the backdrop
    expect(dlg.open).toBe(false);
    expect(onClose).toHaveBeenCalledTimes(2);
    rerender(
      <SlideOver open={false} onClose={onClose} label="Request" header="h">
        b
      </SlideOver>,
    );
    expect(onClose).toHaveBeenCalledTimes(2);
  });

  it("keeps a click inside the panel from reading as the backdrop", () => {
    const onClose = vi.fn();
    render(
      <SlideOver open onClose={onClose} label="Request" header="h">
        <p>body text</p>
      </SlideOver>,
    );
    fireEvent.click(screen.getByText("body text"));
    expect(onClose).not.toHaveBeenCalled();
  });

  it("returns the focus to the opener when the backdrop closes it", async () => {
    const view = render(<Harness />);
    const trigger = screen.getByText("open it") as HTMLElement;
    trigger.focus();
    fireEvent.click(trigger);
    const dlg = view.baseElement.querySelector("dialog") as HTMLDialogElement;
    expect(dlg.open).toBe(true);
    fireEvent.click(dlg);
    expect(dlg.open).toBe(false);
    await new Promise((r) => setTimeout(r, 0));
    expect(document.activeElement).toBe(trigger);
  });

  it("unmounts cleanly while open", () => {
    const view = render(
      <SlideOver open onClose={() => {}} label="Request" header="h">
        b
      </SlideOver>,
    );
    expect(() => view.unmount()).not.toThrow();
  });
});
