// io.test.ts — the huddle side of the kanban API: the board list, the per-key prefixes, the
// non-2xx errors, and the browser event stream's wiring.
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { type BoardRef, boardIo, fetchBoards } from "../../src/kanban/io.ts";
import type { StreamHandlers } from "../../src/kanban/types.ts";
import { clone, snapshot, ticketUi } from "./helpers.ts";

afterEach(() => {
  vi.unstubAllGlobals();
});

/** A response the way fetch resolves one; ok defaults to true. */
function reply(
  body: unknown,
  over: { ok?: boolean; status?: number } = {},
): {
  ok: boolean;
  status: number;
  json: () => Promise<unknown>;
} {
  return { ok: over.ok ?? true, status: over.status ?? 200, json: async () => body };
}

/** Fakes fetch, answering `route(url)`; returns every URL it saw. */
function stubFetch(route: (url: string) => ReturnType<typeof reply>): string[] {
  const calls: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: URL | string) => {
      const url = String(input);
      calls.push(url);
      return route(url);
    }),
  );
  return calls;
}

describe("fetchBoards", () => {
  it("reads the boards from huddle's own endpoint", async () => {
    const boards: BoardRef[] = [{ key: "k", root: "/r", name: "n" }];
    stubFetch((url) =>
      url === "/api/kanban/boards" ? reply({ boards }) : reply({}, { ok: false, status: 404 }),
    );
    expect(await fetchBoards()).toEqual(boards);
  });

  it("throws with the status when huddle refuses", async () => {
    stubFetch(() => reply({}, { ok: false, status: 503 }));
    await expect(fetchBoards()).rejects.toThrow("api/kanban/boards: 503");
  });
});

describe("boardIo", () => {
  it("prefixes every path with the board key", async () => {
    const calls = stubFetch((url) =>
      url.endsWith("/api/board") ? reply(clone(snapshot)) : reply(clone(ticketUi)),
    );
    const io = boardIo("a b");
    expect((await io.fetchSnapshot()).rev).toBe(snapshot.rev);
    expect((await io.fetchTicket("KBN-UI")).card.id).toBe("KBN-UI");
    expect(calls).toEqual(["/api/kanban/b/a%20b/api/board", "/api/kanban/b/a%20b/api/tickets/KBN-UI"]);
  });

  it("throws with the status when a board's read refuses", async () => {
    stubFetch(() => reply({ error: "no board dead" }, { ok: false, status: 404 }));
    const io = boardIo("dead");
    await expect(io.fetchSnapshot()).rejects.toThrow("api/board: 404");
    await expect(io.fetchTicket("KBN-UI")).rejects.toThrow("api/tickets/KBN-UI: 404");
  });

  it("wires the events stream: open, snapshot, delta, reload, and an error closing the source", () => {
    const seen: string[] = [];
    const listeners = new Map<string, (event: { data: string }) => void>();
    const made: { onopen: (() => void) | null; onerror: (() => void) | null }[] = [];
    class FakeSource {
      onopen: (() => void) | null = null;
      onerror: (() => void) | null = null;
      constructor(url: string) {
        seen.push(url);
        made.push(this);
      }
      addEventListener(name: string, fn: (event: { data: string }) => void): void {
        listeners.set(name, fn);
      }
      close(): void {
        seen.push("closed");
      }
    }
    vi.stubGlobal("EventSource", FakeSource);
    const handlers: StreamHandlers = {
      onSnapshot: () => seen.push("snapshot"),
      onDelta: () => seen.push("delta"),
      onReload: () => seen.push("reload"),
      onOpen: () => seen.push("open"),
      onError: () => seen.push("error"),
    };
    const stream = boardIo("k1").connectEvents(handlers);
    expect(seen).toEqual(["/api/kanban/b/k1/api/events"]);
    // the source's own callbacks and its three named events reach the store's handlers
    made[0]?.onopen?.();
    made[0]?.onerror?.();
    listeners.get("snapshot")?.({ data: "{}" });
    listeners.get("delta")?.({ data: "{}" });
    listeners.get("reload")?.({ data: "" });
    // the error handler closes the source at once, then hands the decision to the store
    expect(seen).toEqual([
      "/api/kanban/b/k1/api/events",
      "open",
      "closed",
      "error",
      "snapshot",
      "delta",
      "reload",
    ]);
    stream.close();
    expect(seen[seen.length - 1]).toBe("closed");
  });
});
