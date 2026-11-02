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

/**
 * The slide-over. Every close path (Escape, backdrop, button, `open` turning false) ends in one
 * onClose. A close the slide-over starts tells the owner in the same task; a close the owner starts
 * by turning `open` false, or a form method="dialog", tells it from the native close event.
 */
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
  // The closes the slide-over started whose native close event has not run yet: the browser queues
  // that event for a later task, after the owner has been told and may have reopened the dialog. Each
  // such event is spent against this count, so it never reports the same close twice.
  const startedCloses = useRef(0);

  // Open modally as the newest layer and remember what to give focus back to; close when the parent
  // flips `open` off. It runs on every render, not only when `open` changes: a native close the
  // owner has not repainted yet (Escape, then the opener clicked again before the next paint) leaves
  // `open` true on both sides, and the render that carries it must still show the dialog.
  useEffect(() => {
    const d = dlg.current;
    if (!d) return;
    if (open && !d.open) {
      opener.current = document.activeElement;
      showLayer(d);
    } else if (!open && d.open) {
      d.close();
    }
  });

  // Tell the parent, restack, and give focus back when the opener still lives in an open layer —
  // or nothing is open any more.
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
  // Escape, the backdrop and the close button close the dialog here and tell the parent now, in
  // this task, and note that one native close event is still due for it.
  const close = (): void => {
    const d = dlg.current;
    if (!d?.open) return;
    startedCloses.current += 1;
    d.close();
    closed();
  };
  // The native close event of a close the slide-over started was told already; any other close
  // (the parent turning `open` false, a form method="dialog") is told from here.
  const closeEvent = (): void => {
    if (startedCloses.current > 0) {
      startedCloses.current -= 1;
      return;
    }
    closed();
  };

  return (
    <dialog
      ref={dlg}
      id={id}
      class="ui-slide-over"
      aria-label={label}
      // Preact binds onClose to the native close event
      onClose={closeEvent}
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
          <div class="ui-slide-over-bar px-4 pt-4 pb-3 tablet:px-6 tablet:pt-5 tablet:pb-4">
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
          {tabs ? <div class="px-4 pb-3 tablet:px-6">{tabs}</div> : null}
        </div>
        <div
          class="ui-slide-over-body px-4 pt-5 pb-[calc(var(--ui-space-1)*5+var(--ui-safe-bottom))] tablet:px-6"
          data-slide-over-body
        >
          {children}
        </div>
      </div>
    </dialog>
  );
}
