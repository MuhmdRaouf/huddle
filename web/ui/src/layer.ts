// layer.ts — the one stacking rule for every modal <dialog> of the kit, kept in the DOM: an opening
// dialog numbers itself above every open layer (`data-ui-layer`), and every open layer but the
// newest wears `data-ui-covered`, which the stylesheet hides on a phone so one layer shows at a
// time. No module state: the open dialogs' attributes are the whole record.

/** Show `d` modally above every open layer and mark it the newest. */
export function showLayer(d: HTMLDialogElement): void {
  let top = 0;
  for (const el of d.ownerDocument.querySelectorAll("dialog[open][data-ui-layer]")) {
    top = Math.max(top, Number(el.getAttribute("data-ui-layer")));
  }
  d.setAttribute("data-ui-layer", String(top + 1));
  d.showModal();
  restack(d.ownerDocument);
}

/** Give every open layer but the newest `data-ui-covered`, and take it off the newest. */
export function restack(doc: Document): void {
  const open = [...doc.querySelectorAll<HTMLDialogElement>("dialog[open][data-ui-layer]")];
  let newest: HTMLDialogElement | null = null;
  for (const d of open) {
    if (
      newest === null ||
      Number(d.getAttribute("data-ui-layer")) > Number(newest.getAttribute("data-ui-layer"))
    ) {
      newest = d;
    }
  }
  for (const d of open) {
    if (d === newest) d.removeAttribute("data-ui-covered");
    else d.setAttribute("data-ui-covered", "");
  }
}
