// sheet.tsx — a modal panel on a native <dialog>: it rises from the bottom on a phone, within thumb
// reach, and stands at the right edge on a wide screen. Escape, a tap outside the panel or the close
// button closes it; focus returns to whatever opened it.

import type { ComponentChildren, JSX } from "preact";
import { useEffect, useId, useRef } from "preact/hooks";
import { Button } from "./button.tsx";
import { restack, showLayer } from "./layer.ts";

/** What a sheet shows: open or not, how to close it, its title, header actions and body. */
export type SheetProps = {
  open: boolean;
  onClose: () => void;
  title: ComponentChildren;
  actions?: ComponentChildren;
  children: ComponentChildren;
};

/** The sheet. Every close path (Escape, outside tap, button, `open` turning false) ends in one onClose. */
export function Sheet({ open, onClose, title, actions, children }: SheetProps): JSX.Element {
  const dlg = useRef<HTMLDialogElement>(null);
  const opener = useRef<Element | null>(null);
  const id = useId();

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

  const close = (): void => {
    if (dlg.current?.open) dlg.current.close();
  };
  const closed = (): void => {
    onClose();
    restack(document);
    const back = opener.current;
    opener.current = null;
    if (back instanceof HTMLElement && back.isConnected) back.focus({ preventScroll: true });
  };

  return (
    <dialog
      ref={dlg}
      class="ui-sheet"
      aria-labelledby={id}
      // Preact binds onClose to the native close event, the one path every close ends in
      onClose={closed}
      onCancel={(e) => {
        e.preventDefault();
        close();
      }}
      // the dialog box itself is the dimmed backdrop: a click that lands on it, not the panel, is outside
      onClick={(e) => {
        if (e.target === e.currentTarget) close();
      }}
    >
      <div class="ui-sheet-panel">
        <div class="ui-sheet-handle" aria-hidden="true" />
        <div class="ui-sheet-head">
          <h2 id={id} class="ui-sheet-title">
            {title}
          </h2>
          {actions}
          <Button variant="ghost" icon="x" label="Close" onClick={close} />
        </div>
        <div class="ui-sheet-body">{children}</div>
      </div>
    </dialog>
  );
}
