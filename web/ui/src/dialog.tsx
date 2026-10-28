// dialog.tsx — a modal <dialog> on the shared layer rule: on a phone it rises from the bottom edge
// within thumb reach, from 768 px it stands centred. Escape, a click on the backdrop or the close
// button closes it, and focus returns to whatever opened it — unless another layer is still open,
// which keeps the focus where it is. Port of core.js openDlg/closeDlg plus the per-dialog wiring,
// and of app.js openModal's title + close-button shell.

import type { ComponentChildren, JSX } from "preact";
import { useEffect, useRef } from "preact/hooks";
import { Button } from "./button.tsx";
import { useUiId } from "./id.ts";
import { restack, showLayer } from "./layer.ts";

/** What a dialog shows: whether it is open, how to close it, an optional title and the body. */
export type DialogProps = {
  open: boolean;
  onClose: () => void;
  title?: ComponentChildren | undefined;
  /** The element the dialog is labelled by; the title heading when it is left out. */
  labeledBy?: string | undefined;
  children: ComponentChildren;
};

/** The dialog. Every close path (Escape, backdrop, button, `open` turning false) ends in one onClose. */
export function Dialog({ open, onClose, title, labeledBy, children }: DialogProps): JSX.Element {
  const dlg = useRef<HTMLDialogElement>(null);
  const opener = useRef<Element | null>(null);
  const titleId = useUiId("ui-dialog");

  // Open modally as the newest layer and remember what to give focus back to; close when the parent
  // flips `open` off.
  useEffect(() => {
    const d = dlg.current;
    if (!d) return;
    if (open && !d.open) {
      opener.current = document.activeElement;
      showLayer(d);
    } else if (!open && d.open) {
      d.close();
    }
  }, [open]);

  // The native close event is the one path every close ends in: tell the parent, restack, and give
  // focus back when the opener still lives in an open layer — or nothing is open any more.
  const closed = (): void => {
    onClose();
    restack(document);
    const t = opener.current;
    opener.current = null;
    const back =
      t instanceof HTMLElement &&
      t.isConnected &&
      !t.closest("dialog:not([open])") &&
      (document.querySelector("dialog[open]") === null || t.closest("dialog[open]") !== null)
        ? t
        : null;
    back?.focus({ preventScroll: true });
  };

  return (
    <dialog
      ref={dlg}
      // Preact binds onClose to the element's native close event
      onClose={closed}
      class="ui-dialog"
      aria-labelledby={labeledBy ?? (title ? titleId : undefined)}
      onClick={(e) => {
        if (e.target === dlg.current) closeNow(dlg.current);
      }}
      onKeyDown={(e) => {
        if (e.key === "Escape") {
          e.preventDefault();
          e.stopPropagation();
          closeNow(dlg.current);
        }
      }}
    >
      {title ? (
        <div class="ui-dialog-head">
          <h2 id={titleId} class="ui-dialog-title">
            {title}
          </h2>
          <Button variant="ghost" icon="x" label="Close" onClick={() => closeNow(dlg.current)} />
        </div>
      ) : null}
      <div class="ui-dialog-body">{children}</div>
    </dialog>
  );
}

/** Close the dialog once, through the native close so `onClose` and the refocus run exactly once. */
function closeNow(d: HTMLDialogElement | null): void {
  if (d?.open) d.close();
}
