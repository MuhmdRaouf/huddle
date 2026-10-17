import type { Io } from "./store.ts";
import type { Stream, StreamHandlers } from "./types.ts";

/** The browser's own fetch and EventSource behind the store's slice of them. */
export const browserIo: Io = {
  fetchSnapshot: async () => {
    const response = await fetch("/api/board");
    if (!response.ok) throw new Error(`api/board: ${response.status}`);
    return (await response.json()) as Awaited<ReturnType<Io["fetchSnapshot"]>>;
  },
  fetchTicket: async (id: string) => {
    const response = await fetch(`/api/tickets/${id}`);
    if (!response.ok) throw new Error(`api/tickets/${id}: ${response.status}`);
    return (await response.json()) as Awaited<ReturnType<Io["fetchTicket"]>>;
  },
  connectEvents: (handlers: StreamHandlers): Stream => connectEvents(handlers),
  setTimeout: (fn: () => void, ms: number) => setTimeout(fn, ms),
  clearTimeout: (handle: unknown) => clearTimeout(handle as ReturnType<typeof setTimeout>),
};

/**
 * The server-sent events stream. The browser's EventSource retries on its own schedule; the spec's
 * backoff is ours, so an error closes the source at once and hands the decision to the store.
 */
function connectEvents(handlers: StreamHandlers): Stream {
  const source = new EventSource("/api/events");
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
