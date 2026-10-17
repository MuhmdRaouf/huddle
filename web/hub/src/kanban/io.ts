/**
 * The huddle side of the kanban API: the board list, and one board's read-only slice of the
 * kanban page's own API (snapshot, tickets, events) under its key. Every request rides the
 * session cookie the app already sends; a non-2xx is an Error carrying the status.
 */

import type { Io } from "./store.ts";
import type { Stream, StreamHandlers } from "./types.ts";

/** One board huddle knows: its key (the API's address for it), the repo root it lives under. */
export type BoardRef = { key: string; root: string; name: string };

/** The boards every known session works with, sorted by name as the API sends them. */
export async function fetchBoards(): Promise<BoardRef[]> {
  const response = await fetch("/api/kanban/boards");
  if (!response.ok) throw new Error(`api/kanban/boards: ${response.status}`);
  return ((await response.json()) as { boards: BoardRef[] }).boards;
}

/** The board's events under its key; otherwise the browser stream the kanban page runs. */
function connectEvents(url: string, handlers: StreamHandlers): Stream {
  const source = new EventSource(url);
  source.onopen = () => handlers.onOpen();
  source.onerror = () => {
    source.close();
    handlers.onError();
  };
  for (const name of ["snapshot", "delta", "reload"] as const) {
    source.addEventListener(name, (event: MessageEvent<string>) => {
      if (name === "snapshot") handlers.onSnapshot(JSON.parse(event.data));
      else if (name === "delta") handlers.onDelta(JSON.parse(event.data));
      else handlers.onReload();
    });
  }
  return { close: () => source.close() };
}

/** The browser's fetch and EventSource behind one board's store, every path under its key. */
export function boardIo(key: string): Io {
  const base = `/api/kanban/b/${encodeURIComponent(key)}`;
  return {
    fetchSnapshot: async () => {
      const response = await fetch(`${base}/api/board`);
      if (!response.ok) throw new Error(`api/board: ${response.status}`);
      return (await response.json()) as Awaited<ReturnType<Io["fetchSnapshot"]>>;
    },
    fetchTicket: async (id: string) => {
      const response = await fetch(`${base}/api/tickets/${id}`);
      if (!response.ok) throw new Error(`api/tickets/${id}: ${response.status}`);
      return (await response.json()) as Awaited<ReturnType<Io["fetchTicket"]>>;
    },
    connectEvents: (handlers: StreamHandlers): Stream => connectEvents(`${base}/api/events`, handlers),
    setTimeout: (fn: () => void, ms: number) => setTimeout(fn, ms),
    clearTimeout: (handle: unknown) => clearTimeout(handle as ReturnType<typeof setTimeout>),
  };
}
