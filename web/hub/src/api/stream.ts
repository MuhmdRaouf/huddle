// stream.ts — the hub stream's one client: a single EventSource, behind the silence and return guards, that
// asks the hub for the union of the topics the app registered and routes each decoded message to that topic's
// handlers. It resumes with the last seq it saw, asks for a snapshot of a topic added since the last connect,
// backs off 1, 2, 4, 8 s between failed connects, and tells the page its status. A frame that does not match
// its schema is dropped and counted, never thrown. The source, the timers and the clock are injected.
import { decode, DecodeError } from "./decode.ts";
import type { KeyedMessage } from "./keyed.ts";
import { frameSchema, type Hello } from "./schema/envelope.ts";
import type { EventSourceFactory } from "../live.ts";
import { backoffMs } from "../kanban/store.ts";
import { browserWatch, type SourceLike, type StreamWatch, watchedSource } from "../shared/watchdog.ts";

const STREAM_PATH = "/api/stream";

/** `connecting` while a source opens, `live` once it has, `offline` after an error or before the first start. */
export type StreamStatus = "connecting" | "live" | "offline";

/** Gets every message of one topic; the data is as the wire carried it, for the topic's own schema to decode. */
export type TopicHandler = (message: KeyedMessage<unknown>) => void;

export interface StreamClientOptions {
  factory: EventSourceFactory;
  /** The timers, clock and page the guards read; the browser's by default. */
  watch?: StreamWatch;
}

export interface StreamClient {
  readonly status: StreamStatus;
  /** When the last thing arrived from the hub, a ping included, by the watch's clock; null before the first. */
  readonly lastFrameAt: number | null;
  /** The seq of the last message received; a reconnect resumes after it. 0 before the first. */
  readonly lastSeq: number;
  /** How many frames failed their schema and were dropped. */
  readonly dropped: number;
  /** Registers a handler for a topic; the returned function removes it. A new topic reconnects the stream. */
  on: (topic: string, handler: TopicHandler) => () => void;
  /** Connects, as soon as a topic is registered. */
  start: () => void;
  /** Closes the source and cancels every pending reconnect. */
  stop: () => void;
  /** Calls the function on each status change; the returned function unsubscribes it. */
  subscribe: (fn: () => void) => () => void;
}

/** A stream client with no topics, not yet started. */
export function createStreamClient(options: StreamClientOptions): StreamClient {
  const watch = options.watch ?? browserWatch();
  const handlers = new Map<string, Set<TopicHandler>>();
  // topics whose snapshot has not arrived yet: a connect asks for them with snapshot=
  const awaitingSnapshot = new Set<string>();
  const listeners = new Set<() => void>();
  let status: StreamStatus = "offline";
  let lastFrameAt: number | null = null;
  let lastSeq = 0;
  let dropped = 0;
  let attempt = 0;
  let started = false;
  let source: SourceLike | null = null;
  let requested: string[] = [];
  let backoffTimer: unknown = null;
  let regroupTimer: unknown = null;

  function setStatus(next: StreamStatus): void {
    if (next === status) return;
    status = next;
    for (const fn of listeners) fn();
  }

  function cancel(timer: unknown): null {
    if (timer !== null) watch.timers.clearTimeout(timer);
    return null;
  }

  function urlForNextConnect(): string {
    requested = [...handlers.keys()];
    const params = [`topics=${requested.map(encodeURIComponent).join(",")}`];
    if (lastSeq > 0) {
      params.push(`after=${lastSeq}`);
      const snapshot = requested.filter((topic) => awaitingSnapshot.has(topic));
      if (snapshot.length > 0) params.push(`snapshot=${snapshot.map(encodeURIComponent).join(",")}`);
    }
    return `${STREAM_PATH}?${params.join("&")}`;
  }

  /** A fresh source for the watchdog, which reopens through this on every replacement. It stamps lastFrameAt
   *  on everything the hub writes, the pings the watchdog keeps from the stream's own handler included. */
  function openSource(): SourceLike {
    const raw = options.factory(urlForNextConnect());
    const tap: SourceLike = { onopen: null, onerror: null, onmessage: null, close: () => raw.close() };
    raw.onopen = () => tap.onopen?.();
    raw.onerror = () => tap.onerror?.();
    raw.onmessage = (message) => {
      lastFrameAt = watch.now();
      tap.onmessage?.(message);
    };
    return tap;
  }

  /** Waits the ladder's next rung, then connects fresh. A source that opens on its own first cancels it. */
  function armBackoff(): void {
    if (backoffTimer !== null) return;
    const ms = backoffMs(attempt);
    attempt += 1;
    backoffTimer = watch.timers.setTimeout(() => {
      backoffTimer = null;
      connect();
    }, ms);
  }

  function connect(): void {
    source?.close();
    source = null;
    if (!started || handlers.size === 0) return;
    setStatus("connecting");
    const next = watchedSource(openSource, watch);
    next.onopen = () => {
      backoffTimer = cancel(backoffTimer);
      setStatus("live");
    };
    next.onerror = () => {
      setStatus("offline");
      armBackoff();
    };
    next.onmessage = (message) => receive(message.data);
    source = next;
  }

  /** A topic joined the stream: one connect, however many topics join in the same tick. */
  function regroup(): void {
    if (!started || backoffTimer !== null || regroupTimer !== null) return;
    regroupTimer = watch.timers.setTimeout(() => {
      regroupTimer = null;
      connect();
    }, 0);
  }

  function receive(raw: string): void {
    let frame;
    try {
      frame = decode(frameSchema, JSON.parse(raw), "stream frame");
    } catch (error) {
      if (!(error instanceof SyntaxError || error instanceof DecodeError)) throw error;
      dropped += 1;
      return;
    }
    if ("type" in frame) {
      if (frame.type === "hello") onHello(frame);
      return;
    }
    lastSeq = frame.seq;
    if (frame.op === "snapshot") awaitingSnapshot.delete(frame.topic);
    for (const handler of handlers.get(frame.topic) ?? []) handler(frame);
  }

  /** A hello opens the ladder again. The topics it names in `gone` get a snapshot in place of a replay, and
   *  stay awaiting until it arrives; the others were replayed and ask for nothing more. */
  function onHello(hello: Hello): void {
    attempt = 0;
    const gone = hello.gone ?? [];
    for (const topic of requested) if (!gone.includes(topic)) awaitingSnapshot.delete(topic);
  }

  return {
    get status() {
      return status;
    },
    get lastFrameAt() {
      return lastFrameAt;
    },
    get lastSeq() {
      return lastSeq;
    },
    get dropped() {
      return dropped;
    },
    on(topic, handler) {
      let set = handlers.get(topic);
      if (set === undefined) {
        set = new Set();
        handlers.set(topic, set);
        awaitingSnapshot.add(topic);
        regroup();
      }
      set.add(handler);
      const mine = set;
      return () => {
        mine.delete(handler);
        if (mine.size > 0 || handlers.get(topic) !== mine) return;
        handlers.delete(topic);
        awaitingSnapshot.delete(topic);
      };
    },
    start() {
      started = true;
      connect();
    },
    stop() {
      started = false;
      backoffTimer = cancel(backoffTimer);
      regroupTimer = cancel(regroupTimer);
      source?.close();
      source = null;
      setStatus("offline");
    },
    subscribe(fn) {
      listeners.add(fn);
      return () => {
        listeners.delete(fn);
      };
    },
  };
}
