// browser.ts — the browser's own objects as the radar controller's IO: fetch, the event stream,
// storage, the hash, timers, the CSV download and the focus hand-back. Thin glue on purpose (the
// same call radar's entry makes): everything testable lives in the controller and the views.

import type { EventStream, Io } from "./app/controller.ts";

/** Hand the reader a CSV file: an object URL on a throwaway link, revoked once the click has been taken. */
function download(name: string, text: string): void {
  const href = URL.createObjectURL(new Blob([text], { type: "text/csv;charset=utf-8" }));
  const link = document.createElement("a");
  link.href = href;
  link.download = name;
  document.body.append(link);
  link.click();
  link.remove();
  setTimeout(() => URL.revokeObjectURL(href), 1000);
}

/** The browser's EventSource behind the controller's slice of it, forwarding the three handlers it sets. */
class BrowserStream implements EventStream {
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onmessage: ((message: { data: string }) => void) | null = null;
  private readonly source: EventSource;

  constructor(path: string) {
    this.source = new EventSource(path);
    this.source.onopen = () => this.onopen?.();
    this.source.onerror = () => this.onerror?.();
    this.source.onmessage = (message) => this.onmessage?.(message);
  }

  close(): void {
    this.source.close();
  }
}

/** The browser's own objects; huddle's shell owns the theme, so nothing here applies one. */
export function browserIo(): Io {
  return {
    fetch: (path, init) => fetch(path, init),
    EventSource: BrowserStream,
    storage: localStorage,
    location,
    replaceHash: (hash) => history.replaceState(null, "", hash),
    // the panel, search, repo and scope choices get a real history entry, so Back returns to them
    pushHash: (hash) => {
      location.hash = hash;
    },
    now: () => Date.now(),
    setTimeout: (fn, ms) => setTimeout(fn, ms),
    clearTimeout: (id) => clearTimeout(id),
    setInterval: (fn, ms) => setInterval(fn, ms),
    clearInterval: (id) => clearInterval(id),
    download,
    href: () => location.href,
    clipboard: (text) => {
      void navigator.clipboard?.writeText(text).catch(() => undefined);
    },
    revealView: () => {
      // the phone can scroll a view far down the pane; bring its top back into sight after a change
      const view = document.querySelector<HTMLElement>("[data-radar-view]");
      if (view === null || view.getBoundingClientRect().top < window.innerHeight * 0.6) return;
      view.scrollIntoView({ behavior: "smooth", block: "start" });
    },
    focusOpener: (id) => {
      for (const row of document.querySelectorAll<HTMLElement>('[data-action="drawer"]')) {
        if (row.getAttribute("data-value") === id) {
          row.focus();
          return;
        }
      }
    },
  };
}
