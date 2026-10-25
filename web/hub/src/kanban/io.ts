/**
 * The huddle side of the kanban API: the board list, and one board's read-only slice of the
 * kanban page's own API (snapshot, tickets, events) under its key. Every request rides the
 * session cookie the app already sends; a non-2xx is an Error carrying the status.
 */

import type { Io } from "./store.ts";
import type { Snapshot, Stream, StreamHandlers, Ticket } from "./types.ts";

/** One board huddle knows: its key (the API's address for it), the repo root it lives under. */
export type BoardRef = { key: string; root: string; name: string };

/** The picker's label: the repo's name, disambiguated with its root once the name repeats. */
export function optionLabel(boards: BoardRef[], board: BoardRef): string {
  return boards.some((other) => other !== board && other.name === board.name)
    ? `${board.name} (${board.root})`
    : board.name;
}

/** The boards every known session works with, sorted by name as the API sends them; the signal
 *  ends the request when its caller gives up on it. */
export async function fetchBoards(signal?: AbortSignal): Promise<BoardRef[]> {
  const response = await fetch("/api/kanban/boards", { signal: signal ?? null });
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

/** The browser's fetch and EventSource behind one board's store, every path under one base. */
export function ioAt(base: string): Io {
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

/** The board's store over the hub, every path under its key. */
export function boardIo(key: string): Io {
  return ioAt(`/api/kanban/b/${encodeURIComponent(key)}`);
}

/**
 * The exported board's IO: the snapshot and every ticket body come from the payload the page
 * carries, nothing is fetched and no stream opens. The store the pane builds over it starts from
 * the static state (mode "static"), so a closed page never shows the live dot or reconnecting.
 */
export function staticIo(payload: { snapshot: Snapshot; bodies: Record<string, Ticket> }): Io {
  return {
    fetchSnapshot: async () => payload.snapshot,
    fetchTicket: async (id: string) => {
      const ticket = payload.bodies[id];
      if (ticket === undefined) throw new Error(`no exported body for ${id}`);
      return ticket;
    },
    connectEvents: (): Stream => ({ close: () => {} }),
    setTimeout: (fn: () => void, ms: number) => setTimeout(fn, ms),
    clearTimeout: (handle: unknown) => clearTimeout(handle as ReturnType<typeof setTimeout>),
  };
}
