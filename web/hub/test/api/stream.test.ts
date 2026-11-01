// stream.test.ts — the hub stream's one client: it asks for the union of the registered topics, resumes
// with the last seq it saw, asks for a snapshot of a topic added since the last connect, drops a frame that
// fails its schema and counts it, replaces a silent source through the watchdog, backs off 1, 2, 4, 8 s,
// and moves lastFrameAt on every frame, a ping included. The source and the clock are fakes.
import { describe, expect, it } from "vite-plus/test";
import { createStreamClient, type StreamClient } from "../../src/api/stream.ts";
import type { KeyedMessage } from "../../src/api/keyed.ts";
import type { EventSourceLike } from "../../src/live.ts";
import { STREAM_SILENCE_MS, type StreamWatch } from "../../src/shared/watchdog.ts";

interface FakeSource extends EventSourceLike {
  url: string;
  closed: boolean;
}

/** A clock the test moves by hand; timers fire in due order as it advances. */
function fakeClock() {
  let now = 1_000_000;
  let next = 0;
  const pending = new Map<number, { due: number; fn: () => void }>();
  const watch: StreamWatch = {
    timers: {
      setTimeout: (fn, ms) => {
        const handle = ++next;
        pending.set(handle, { due: now + ms, fn });
        return handle;
      },
      clearTimeout: (handle) => {
        pending.delete(handle as number);
      },
    },
    now: () => now,
    page: null,
  };
  function advance(ms: number): void {
    const end = now + ms;
    for (;;) {
      let soonest: [number, { due: number; fn: () => void }] | undefined;
      for (const entry of pending)
        if (entry[1].due <= end && (!soonest || entry[1].due < soonest[1].due)) soonest = entry;
      if (!soonest) break;
      pending.delete(soonest[0]);
      now = Math.max(now, soonest[1].due);
      soonest[1].fn();
    }
    now = end;
  }
  return { watch, advance };
}

/** Every source the client opened, in order, with helpers to drive the last one. */
function fakeHub() {
  const opened: FakeSource[] = [];
  const factory = (url: string): EventSourceLike => {
    const source: FakeSource = {
      url,
      closed: false,
      onopen: null,
      onerror: null,
      onmessage: null,
      close() {
        source.closed = true;
      },
    };
    opened.push(source);
    return source;
  };
  const last = (): FakeSource => {
    const source = opened.at(-1);
    if (source === undefined) throw new Error("no source opened");
    return source;
  };
  const send = (frame: unknown): void =>
    last().onmessage?.({ data: typeof frame === "string" ? frame : JSON.stringify(frame) });
  const open = (): void => last().onopen?.();
  const fail = (): void => last().onerror?.();
  const query = (index = opened.length - 1): URLSearchParams =>
    new URL(opened[index]?.url ?? "", "http://hub.test").searchParams;
  return { opened, factory, send, open, fail, query };
}

const hello = (head: number, gone: string[] = []) => ({ type: "hello", head, served: [], gone });
const upsert = (topic: string, seq: number, id: string, data: unknown = { n: seq }) => ({
  topic,
  seq,
  op: "upsert",
  id,
  data,
});
const snapshot = (topic: string, seq: number) => ({ topic, seq, op: "snapshot", id: "", data: { rows: [] } });

function setup() {
  const clock = fakeClock();
  const hub = fakeHub();
  const client: StreamClient = createStreamClient({ factory: hub.factory, watch: clock.watch });
  return { clock, hub, client };
}

describe("the stream client", () => {
  it("reconnects with after = the last seq", () => {
    const { clock, hub, client } = setup();
    client.on("sessions", () => {});
    client.on("needs", () => {});
    client.start();
    expect(hub.query(0).get("topics")).toBe("sessions,needs");
    expect(hub.query(0).has("after")).toBe(false);
    hub.open();
    hub.send(hello(100, ["sessions", "needs"]));
    hub.send(snapshot("sessions", 100));
    hub.send(snapshot("needs", 100));
    hub.send(upsert("sessions", 101, "a"));
    hub.send(upsert("needs", 102, "b"));
    expect(client.lastSeq).toBe(102);

    hub.fail();
    clock.advance(1000);

    expect(hub.opened).toHaveLength(2);
    expect(hub.opened[0]?.closed).toBe(true);
    expect(hub.query().get("after")).toBe("102");
    expect(hub.query().get("topics")).toBe("sessions,needs");
    expect(hub.query().has("snapshot")).toBe(false);
  });

  it("a topic added later reconnects with snapshot=<it>", () => {
    const { clock, hub, client } = setup();
    client.on("sessions", () => {});
    client.start();
    hub.open();
    hub.send(hello(10, ["sessions"]));
    hub.send(snapshot("sessions", 10));
    hub.send(upsert("sessions", 11, "a"));

    client.on("channel:e2e", () => {});
    clock.advance(0);

    expect(hub.opened).toHaveLength(2);
    expect(hub.opened[0]?.closed).toBe(true);
    expect(hub.query().get("topics")).toBe("sessions,channel:e2e");
    expect(hub.query().get("after")).toBe("11");
    expect(hub.query().get("snapshot")).toBe("channel:e2e");
  });

  it("a topic whose snapshot never arrived is asked for again", () => {
    const { clock, hub, client } = setup();
    client.on("sessions", () => {});
    client.on("needs", () => {});
    client.start();
    hub.open();
    hub.send(hello(10, ["sessions", "needs"]));
    hub.send(snapshot("sessions", 10));
    hub.fail();
    clock.advance(1000);

    expect(hub.query().get("after")).toBe("10");
    expect(hub.query().get("snapshot")).toBe("needs");
  });

  it("a frame that fails its schema is dropped and counted, not thrown", () => {
    const { hub, client } = setup();
    const seen: KeyedMessage<unknown>[] = [];
    client.on("sessions", (message) => seen.push(message));
    client.start();
    hub.open();
    hub.send(hello(5));

    expect(() => {
      hub.send("not json");
      hub.send({ topic: "sessions", seq: "7", op: "upsert", id: "a", data: {} });
      hub.send({ topic: "sessions", seq: 8, op: "explode", id: "a" });
      hub.send({ topic: "sessions", seq: 9, op: "snapshot", id: "", data: { rows: "none" } });
    }).not.toThrow();
    expect(client.dropped).toBe(4);
    expect(seen).toEqual([]);
    expect(client.lastSeq).toBe(0);

    hub.send(upsert("sessions", 10, "a"));
    expect(seen).toHaveLength(1);
    expect(client.dropped).toBe(4);
  });

  it("a silent source is replaced by the watchdog", () => {
    const { clock, hub, client } = setup();
    client.on("sessions", () => {});
    client.start();
    hub.open();
    hub.send(hello(20, ["sessions"]));
    hub.send(snapshot("sessions", 20));
    hub.send(upsert("sessions", 21, "a"));
    expect(client.status).toBe("live");

    clock.advance(STREAM_SILENCE_MS);

    expect(hub.opened).toHaveLength(2);
    expect(hub.opened[0]?.closed).toBe(true);
    expect(hub.query().get("after")).toBe("21");
    expect(client.status).toBe("offline");
    hub.open();
    expect(client.status).toBe("live");
  });

  it("lastFrameAt moves on a ping", () => {
    const { clock, hub, client } = setup();
    client.on("sessions", () => {});
    client.start();
    expect(client.lastFrameAt).toBeNull();
    hub.open();
    hub.send(hello(1));
    const afterHello = client.lastFrameAt;

    clock.advance(15_000);
    hub.send({ type: "ping" });

    expect(client.lastFrameAt).toBe((afterHello ?? 0) + 15_000);
    expect(client.dropped).toBe(0);
  });

  it("backs off 1, 2, 4, 8 s and starts over after a hello", () => {
    const { clock, hub, client } = setup();
    client.on("sessions", () => {});
    client.start();
    const waits: number[] = [];
    for (const wait of [1000, 2000, 4000, 8000, 8000]) {
      const before = hub.opened.length;
      hub.fail();
      expect(client.status).toBe("offline");
      clock.advance(wait - 1);
      expect(hub.opened).toHaveLength(before);
      clock.advance(1);
      expect(hub.opened).toHaveLength(before + 1);
      expect(client.status).toBe("connecting");
      waits.push(wait);
    }
    hub.open();
    hub.send(hello(3));
    hub.fail();
    clock.advance(999);
    const count = hub.opened.length;
    clock.advance(1);
    expect(hub.opened).toHaveLength(count + 1);
    expect(waits).toEqual([1000, 2000, 4000, 8000, 8000]);
  });

  it("routes each frame to its topic's handlers and stops after the handler is removed", () => {
    const { hub, client } = setup();
    const sessions: string[] = [];
    const needs: string[] = [];
    const off = client.on("sessions", (message) => sessions.push(message.id));
    client.on("needs", (message) => needs.push(message.id));
    client.start();
    hub.open();
    hub.send(hello(1));
    hub.send(upsert("sessions", 2, "s1"));
    hub.send(upsert("needs", 3, "n1"));
    hub.send(upsert("radar.live", 4, "ignored"));
    off();
    hub.send(upsert("sessions", 5, "s2"));

    expect(sessions).toEqual(["s1"]);
    expect(needs).toEqual(["n1"]);
    expect(client.lastSeq).toBe(5);
  });

  it("stop closes the source and leaves nothing to fire", () => {
    const { clock, hub, client } = setup();
    client.on("sessions", () => {});
    client.start();
    hub.open();
    hub.fail();
    client.stop();
    clock.advance(60_000);

    expect(hub.opened).toHaveLength(1);
    expect(hub.opened[0]?.closed).toBe(true);
    expect(client.status).toBe("offline");
  });
});
