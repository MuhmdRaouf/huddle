// slide-over.tsx — a large panel over the whole dashboard: a bottom sheet on a phone, a full-height
// pane at the right edge from 40 rem, on the shared layer rule — an opening slide-over numbers
// itself above every open layer, and on a phone only the newest layer shows. Escape, a click on
// the dimmed page or the close button closes it, focus stays inside while it is open and returns
// to whatever opened it. The panel and its head are solid on the tokens' surface, the body scrolls
// on its own, and both pad themselves by the safe areas.

import type { ComponentChildren, JSX } from "preact";
import { useEffect, useRef } from "preact/hooks";
import { restack, showLayer } from "./layer.ts";

/** What a slide-over shows: open or not, how to close it, a header and the body. */
export type SlideOverProps = {
  open: boolean;
  onClose: () => void;
  /** Accessible name of the dialog. */
  label: string;
  /** The header's left side: title, chips, key facts. */
  header: ComponentChildren;
  /** Buttons before the close button (previous/next, copy...). */
  actions?: ComponentChildren;
  /** Under the header, above the scrolling body: the panel's tabs. */
  tabs?: ComponentChildren;
  /** The pane's width from 40 rem; on a phone the sheet is full width whatever this says. */
  width?: string;
  /** The dialog element's id, for the code that recognizes one slide-over by it. */
  id?: string;
  children: ComponentChildren;
};

const X = (
  <svg
    class="size-5"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="2"
    stroke-linecap="round"
    aria-hidden="true"
  >
    <path d="M18 6 6 18M6 6l12 12" />
  </svg>
);

/** The slide-over. Every close path (Escape, backdrop, button, `open` turning false) ends in one onClose. */
export function SlideOver({
  open,
  onClose,
  label,
  header,
  actions,
  tabs,
  width = "w-[min(64rem,94vw)]",
  id,
  children,
}: SlideOverProps): JSX.Element {
  const dlg = useRef<HTMLDialogElement>(null);
  const opener = useRef<Element | null>(null);

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
  const close = (): void => {
    if (dlg.current?.open) dlg.current.close();
  };

  return (
    <dialog
      ref={dlg}
      id={id}
      class="ui-slide-over"
      aria-label={label}
      // Preact binds onClose to the native close event, the one path every close ends in
      onClose={closed}
      onCancel={(e) => {
        e.preventDefault();
        close();
      }}
      // the element is the dimmed click-away itself: a click that lands on it, not the panel, is outside
      onClick={(e) => {
        if (e.target === dlg.current) close();
      }}
    >
      <div class={`ui-slide-over-panel ${width}`} data-slide-over>
        <div class="ui-slide-over-head border-b border-[var(--ui-line)]">
          <div class="ui-slide-over-bar px-4 pt-4 pb-3 sm:px-6 sm:pt-5 sm:pb-4">
            {/* one DOM on the bar's grid areas: on a phone the close button keeps the header
             * company on the first row and the actions wrap on their own full-width row, so a
             * wide action group cannot push the close button past the sheet's edge */}
            <div class="ui-slide-over-bar-head">{header}</div>
            {actions ? <div class="ui-slide-over-bar-actions">{actions}</div> : null}
            <button
              type="button"
              class="ui-btn ui-btn-ghost ui-btn-icon ui-slide-over-bar-close"
              aria-label="Close"
              title="Close (Esc)"
              onClick={close}
            >
              {X}
            </button>
          </div>
          {tabs ? <div class="px-4 pb-3 sm:px-6">{tabs}</div> : null}
        </div>
        <div
          class="ui-slide-over-body px-4 pt-5 pb-[calc(var(--ui-space-1)*5+var(--ui-safe-bottom))] sm:px-6"
          data-slide-over-body
        >
          {children}
        </div>
      </div>
    </dialog>
  );
}
