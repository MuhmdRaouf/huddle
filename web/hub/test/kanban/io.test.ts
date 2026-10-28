// io.test.ts — the huddle side of the kanban API: the board list, the per-key prefixes, the
// non-2xx errors, and the browser event stream's wiring.
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { type BoardRef, boardIo, fetchBoards, featureAction, ioAt, staticIo } from "../../src/kanban/io.ts";
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

  it("hands the caller's signal to the request, so an abort ends it", async () => {
    const seen: (AbortSignal | null | undefined)[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (_input: URL | string, init?: RequestInit) => {
        seen.push(init?.signal);
        return reply({ boards: [] });
      }),
    );
    const controller = new AbortController();
    await fetchBoards(controller.signal);
    expect(seen).toEqual([controller.signal]);
  });
});

describe("ioAt", () => {
  it("fetches the board server's own paths over its base", async () => {
    const calls = stubFetch((url) =>
      url.endsWith("/api/board") ? reply(clone(snapshot)) : reply(clone(ticketUi)),
    );
    const io = ioAt("");
    expect((await io.fetchSnapshot()).rev).toBe(snapshot.rev);
    expect((await io.fetchTicket("KBN-UI")).card.id).toBe("KBN-UI");
    expect(calls).toEqual(["/api/board", "/api/tickets/KBN-UI"]);
  });
});

describe("staticIo", () => {
  it("answers the exported snapshot and bodies with no network", async () => {
    stubFetch(() => {
      throw new Error("the exported page must not fetch");
    });
    const io = staticIo({ snapshot: clone(snapshot), bodies: { "KBN-UI": clone(ticketUi) } });
    expect((await io.fetchSnapshot()).rev).toBe(snapshot.rev);
    expect((await io.fetchTicket("KBN-UI")).card.id).toBe("KBN-UI");
    await expect(io.fetchTicket("GONE")).rejects.toThrow("no exported body for GONE");
  });

  it("opens no stream: no handler fires and the store never reconnects", () => {
    const io = staticIo({ snapshot: clone(snapshot), bodies: {} });
    const fired: string[] = [];
    const stream = io.connectEvents({
      onSnapshot: () => fired.push("snapshot"),
      onDelta: () => fired.push("delta"),
      onReload: () => fired.push("reload"),
      onPing: () => fired.push("ping"),
      onOpen: () => fired.push("open"),
      onError: () => fired.push("error"),
    });
    expect(fired).toEqual([]);
    stream.close();
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

  it("wires the events stream: open, snapshot, delta, reload, ping, and an error closing the source", () => {
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
      onPing: () => seen.push("ping"),
      onOpen: () => seen.push("open"),
      onError: () => seen.push("error"),
    };
    const stream = boardIo("k1").connectEvents(handlers);
    expect(seen).toEqual(["/api/kanban/b/k1/api/events"]);
    // the source's own callbacks and its four named events reach the store's handlers
    made[0]?.onopen?.();
    made[0]?.onerror?.();
    listeners.get("snapshot")?.({ data: "{}" });
    listeners.get("delta")?.({ data: "{}" });
    listeners.get("reload")?.({ data: "" });
    listeners.get("ping")?.({ data: "{}" });
    // the error handler closes the source at once, then hands the decision to the store
    expect(seen).toEqual([
      "/api/kanban/b/k1/api/events",
      "open",
      "closed",
      "error",
      "snapshot",
      "delta",
      "reload",
      "ping",
    ]);
    stream.close();
    expect(seen[seen.length - 1]).toBe("closed");
  });
});

describe("featureAction", () => {
  it("posts the action and the feature to the board's feature endpoint, and answers its lines", async () => {
    const seen: { url: string; init: RequestInit | undefined }[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: URL | string, init?: RequestInit) => {
        seen.push({ url: String(input), init });
        return reply({ lines: ["Closed pick-a: 2 cards archived"] });
      }),
    );
    expect(await featureAction("a b", "close", "pick-a")).toEqual({
      ok: true,
      lines: ["Closed pick-a: 2 cards archived"],
    });
    expect(seen).toEqual([
      {
        url: "/api/kanban/b/a%20b/feature",
        init: {
          method: "POST",
          headers: {
            "content-type": "application/json",
            // the hub's write guard refuses a POST that names no action
            "x-huddle-action": "/api/kanban/b/a%20b/feature",
          },
          body: JSON.stringify({ action: "close", feature: "pick-a" }),
        },
      },
    ]);
  });

  it("posts reopen with the reopen action in the body", async () => {
    const bodies: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (_input: URL | string, init?: RequestInit) => {
        if (typeof init?.body === "string") bodies.push(init.body);
        return reply({ lines: ["Reopened gamma: 7 cards back"] });
      }),
    );
    await featureAction("k", "reopen", "gamma");
    expect(bodies).toEqual([JSON.stringify({ action: "reopen", feature: "gamma" })]);
  });

  it("answers a refusal as a value — the status and the server's error, never a throw", async () => {
    stubFetch(() =>
      reply({ error: "only the owner closes or reopens a feature" }, { ok: false, status: 403 }),
    );
    expect(await featureAction("k", "close", "pick-a")).toEqual({
      ok: false,
      status: 403,
      error: "only the owner closes or reopens a feature",
    });
  });

  it("answers a dead connection as bad news with status 0", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new TypeError("fetch failed");
      }),
    );
    expect(await featureAction("k", "close", "pick-a")).toEqual({
      ok: false,
      status: 0,
      error: "fetch failed",
    });
  });
});
