/**
 * The huddle side of the kanban API: the board list, one board's read-only slice of the kanban
 * page's own API (snapshot, tickets, events) under its key, and the one write the page makes,
 * closing or reopening a feature. Every request rides the session cookie the app already sends;
 * a non-2xx is an Error carrying the status, except the feature action, which answers as a value.
 */

import { decode } from "../api/decode.ts";
import {
  BoardsAnswerSchema,
  DeltaSchema,
  FeatureAnswerSchema,
  SnapshotSchema,
  TicketSchema,
  type BoardRef,
  type Snapshot,
  type Ticket,
} from "../api/schema/kanban.ts";
import type { Io } from "./store.ts";
import type { Stream, StreamHandlers } from "./types.ts";

export type { BoardRef } from "../api/schema/kanban.ts";

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
  return decode(BoardsAnswerSchema, await response.json(), "api/kanban/boards").boards;
}

/** The board's events under its key; otherwise the browser stream the kanban page runs. */
function connectEvents(url: string, handlers: StreamHandlers): Stream {
  const source = new EventSource(url);
  source.onopen = () => handlers.onOpen();
  source.onerror = () => {
    source.close();
    handlers.onError();
  };
  for (const name of ["snapshot", "delta", "reload", "ping"] as const) {
    source.addEventListener(name, (event: MessageEvent<string>) => {
      if (name === "snapshot")
        handlers.onSnapshot(decode(SnapshotSchema, JSON.parse(event.data), "api/events snapshot"));
      else if (name === "delta")
        handlers.onDelta(decode(DeltaSchema, JSON.parse(event.data), "api/events delta"));
      else if (name === "reload") handlers.onReload();
      else handlers.onPing();
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
      return decode(SnapshotSchema, await response.json(), "api/board");
    },
    fetchTicket: async (id: string) => {
      const response = await fetch(`${base}/api/tickets/${id}`);
      if (!response.ok) throw new Error(`api/tickets/${id}: ${response.status}`);
      return decode(TicketSchema, await response.json(), `api/tickets/${id}`);
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

/** The answer a close or reopen gives: the server's lines, or the refusal as a value. */
export type FeatureActionResult =
  | { ok: true; lines: string[] }
  | { ok: false; status: number; error: string };

/** Closes or reopens a feature on the board at `key`. The refusal rides back as a value — the
 *  status and the server's error, a dead connection as status 0 — never a throw: the caller toasts
 *  whatever comes back. */
export async function featureAction(
  key: string,
  action: "close" | "reopen",
  feature: string,
): Promise<FeatureActionResult> {
  try {
    const response = await fetch(`/api/kanban/b/${encodeURIComponent(key)}/feature`, {
      method: "POST",
      headers: {
        "content-type": "application/json",
        // the hub's write guard refuses a POST that names no action
        "x-huddle-action": `/api/kanban/b/${encodeURIComponent(key)}/feature`,
      },
      body: JSON.stringify({ action, feature }),
    });
    const body = decode(FeatureAnswerSchema, await response.json().catch(() => ({})), "api/feature");
    if (!response.ok) {
      return { ok: false, status: response.status, error: body.error ?? `api/feature: ${response.status}` };
    }
    return { ok: true, lines: body.lines ?? [] };
  } catch (error) {
    return { ok: false, status: 0, error: error instanceof Error ? error.message : String(error) };
  }
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
