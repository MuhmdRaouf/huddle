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

/**
 * Makes `dialog.close()` behave as it does in a browser: the dialog closes at once and its close
 * event is queued for a later task. Returns the function that delivers the queued events.
 */
function queueCloseEvents(dlg: HTMLDialogElement): () => void {
  let due = 0;
  dlg.close = () => {
    if (!dlg.open) return;
    dlg.removeAttribute("open");
    due += 1;
  };
  return () => {
    const n = due;
    due = 0;
    for (let i = 0; i < n; i += 1) dlg.dispatchEvent(new Event("close"));
  };
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

  it("reopens when its owner asks for it again before the owner repainted the close", () => {
    // an owner that keeps `open` outside the tree, as the hub shell keeps it in the address:
    // the close and the next open both land before the tree paints again
    let wanted = true;
    const props = (): { open: boolean; onClose: () => void } => ({
      open: wanted,
      onClose: () => {
        wanted = false;
      },
    });
    const { baseElement, rerender } = render(
      <SlideOver {...props()} label="Session" header="h">
        b
      </SlideOver>,
    );
    const dlg = baseElement.querySelector("dialog") as HTMLDialogElement;
    expect(dlg.open).toBe(true);
    fireEvent(dlg, new Event("cancel", { cancelable: true })); // Escape
    expect(dlg.open).toBe(false);
    wanted = true; // the next click on the opener, before any repaint
    rerender(
      <SlideOver {...props()} label="Session" header="h">
        b
      </SlideOver>,
    );
    expect(dlg.open).toBe(true);
  });

  it.each([
    ["Escape", (dlg: HTMLDialogElement) => fireEvent(dlg, new Event("cancel", { cancelable: true }))],
    ["a backdrop click", (dlg: HTMLDialogElement) => fireEvent.click(dlg)],
    [
      "the close button",
      (dlg: HTMLDialogElement) => fireEvent.click(within(dlg).getByRole("button", { name: "Close" })),
    ],
  ])("tells its owner once about %s, in the task that starts the close", (_name, start) => {
    const onClose = vi.fn();
    const { baseElement } = render(
      <SlideOver open onClose={onClose} label="Request" header="h">
        b
      </SlideOver>,
    );
    const dlg = baseElement.querySelector("dialog") as HTMLDialogElement;
    const deliver = queueCloseEvents(dlg);
    start(dlg);
    expect(dlg.open).toBe(false);
    expect(onClose).toHaveBeenCalledTimes(1);
    deliver();
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("tells its owner once when the owner closes it, from the close event", () => {
    const onClose = vi.fn();
    const { baseElement, rerender } = render(
      <SlideOver open onClose={onClose} label="Request" header="h">
        b
      </SlideOver>,
    );
    const dlg = baseElement.querySelector("dialog") as HTMLDialogElement;
    const deliver = queueCloseEvents(dlg);
    rerender(
      <SlideOver open={false} onClose={onClose} label="Request" header="h">
        b
      </SlideOver>,
    );
    expect(dlg.open).toBe(false);
    deliver();
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("keeps a reopened dialog open when the close event of the Escape arrives late", () => {
    const onClose = vi.fn();
    const { baseElement, rerender } = render(
      <SlideOver open onClose={onClose} label="Request" header="h">
        b
      </SlideOver>,
    );
    const dlg = baseElement.querySelector("dialog") as HTMLDialogElement;
    const deliver = queueCloseEvents(dlg);
    fireEvent(dlg, new Event("cancel", { cancelable: true })); // Escape
    expect(onClose).toHaveBeenCalledTimes(1);
    rerender(
      <SlideOver open onClose={onClose} label="Request" header="h">
        b
      </SlideOver>,
    ); // the owner is asked to open it again before the event ran
    expect(dlg.open).toBe(true);
    deliver();
    expect(dlg.open).toBe(true);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("counts each close it started: two Escapes before either event ran call the owner twice", () => {
    const onClose = vi.fn();
    const { baseElement, rerender } = render(
      <SlideOver open onClose={onClose} label="Request" header="h">
        b
      </SlideOver>,
    );
    const dlg = baseElement.querySelector("dialog") as HTMLDialogElement;
    const deliver = queueCloseEvents(dlg);
    fireEvent(dlg, new Event("cancel", { cancelable: true }));
    rerender(
      <SlideOver open onClose={onClose} label="Request" header="h">
        b
      </SlideOver>,
    );
    fireEvent(dlg, new Event("cancel", { cancelable: true }));
    expect(onClose).toHaveBeenCalledTimes(2);
    deliver();
    expect(onClose).toHaveBeenCalledTimes(2);
    // both events are spent: a close the owner starts afterwards is heard from its event
    rerender(
      <SlideOver open onClose={onClose} label="Request" header="h">
        b
      </SlideOver>,
    );
    rerender(
      <SlideOver open={false} onClose={onClose} label="Request" header="h">
        b
      </SlideOver>,
    );
    deliver();
    expect(onClose).toHaveBeenCalledTimes(3);
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

  it("lays the head on the bar: each action once, the close once, the actions in their grid area", () => {
    const { baseElement } = render(
      <SlideOver
        open
        onClose={() => {}}
        label="Request"
        header={<h2>Opus</h2>}
        actions={
          <>
            <button type="button">Pause alpha</button>
            <button type="button">Remove from channel</button>
          </>
        }
      >
        b
      </SlideOver>,
    );
    // getByRole throws on a second match, so each action renders once, and the close button is
    // the one button its name finds
    expect(screen.getByRole("button", { name: "Pause alpha" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Remove from channel" })).toBeTruthy();
    expect(screen.getAllByRole("button", { name: "Close" })).toHaveLength(1);
    const actions = baseElement.querySelector(".ui-slide-over-bar-actions") as HTMLElement | null;
    expect(actions, "the actions sit in the bar's actions grid area").toBeTruthy();
    expect(actions?.textContent).toContain("Pause alpha");
    expect(actions?.textContent).toContain("Remove from channel");
    // the close button is a grid area of its own, outside the actions
    expect(actions?.contains(screen.getByRole("button", { name: "Close" }))).toBe(false);
  });
});
