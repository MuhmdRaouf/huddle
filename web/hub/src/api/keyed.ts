// keyed.ts — the store the hub stream's frames land in: rows held by id, changed by snapshot,
// upsert and remove messages, read by id or as a list, and followed by subscribers. useKeyed
// repaints a component only when the value it selected from the store changes.
import { useEffect, useRef, useState } from "preact/hooks";

/** One row of a snapshot: the row's id and the row. */
export interface KeyedRow<T> {
  id: string;
  data: T;
}

/** The fields every stream message carries. */
interface KeyedFrame {
  topic: string;
  seq: number;
  id: string;
}

/** A topic's whole state; it replaces every row the store holds. */
export interface SnapshotMessage<T> extends KeyedFrame {
  op: "snapshot";
  data: { rows: KeyedRow<T>[] };
}

/** One row, named by the message's id; it replaces the row held under that id. */
export interface UpsertMessage<T> extends KeyedFrame {
  op: "upsert";
  data: T;
}

/** The row the message's id names goes away. */
export interface RemoveMessage extends KeyedFrame {
  op: "remove";
  data?: undefined;
}

/** A change rows cannot carry; the holder of the view refetches it. A store ignores it. */
export interface TouchMessage extends KeyedFrame {
  op: "touch";
  data?: undefined;
}

/** A stream message as the wire spells it: topic, seq, op, id and, left off remove and touch, data. */
export type KeyedMessage<T> = SnapshotMessage<T> | UpsertMessage<T> | RemoveMessage | TouchMessage;

/** Rows by id, in the order they first arrived. */
export interface Keyed<T> {
  /** Counts the messages that changed the rows. */
  readonly version: number;
  /** Changes the rows by the message. A touch, and a remove of an absent id, change nothing. */
  apply: (msg: KeyedMessage<T>) => void;
  get: (id: string) => T | undefined;
  /** Every row; the same array until the next change. */
  values: () => readonly T[];
  /** Calls the function once per applied change; the returned function unsubscribes it. */
  subscribe: (fn: () => void) => () => void;
}

/** An empty store. */
export function createKeyed<T>(): Keyed<T> {
  let rows = new Map<string, T>();
  let list: readonly T[] | undefined;
  let version = 0;
  const subscribers = new Set<() => void>();

  function changed(): void {
    list = undefined;
    version++;
    for (const fn of subscribers) fn();
  }

  return {
    get version() {
      return version;
    },
    apply(msg) {
      switch (msg.op) {
        case "snapshot":
          rows = new Map(msg.data.rows.map((row) => [row.id, row.data]));
          break;
        case "upsert":
          rows.set(msg.id, msg.data);
          break;
        case "remove":
          if (!rows.delete(msg.id)) return;
          break;
        case "touch":
          return;
      }
      changed();
    },
    get: (id) => rows.get(id),
    values() {
      list ??= [...rows.values()];
      return list;
    },
    subscribe(fn) {
      subscribers.add(fn);
      return () => {
        subscribers.delete(fn);
      };
    },
  };
}

/** The value the selector reads from the store, repainting only when a change makes it differ (Object.is). */
export function useKeyed<T, S>(store: Keyed<T>, selector: (store: Keyed<T>) => S): S {
  const [, setPaints] = useState(0);
  const selected = selector(store);
  const shown = useRef(selected);
  const select = useRef(selector);
  shown.current = selected;
  select.current = selector;

  useEffect(() => {
    const check = () => {
      if (!Object.is(select.current(store), shown.current)) setPaints((n) => n + 1);
    };
    check();
    return store.subscribe(check);
  }, [store]);

  return selected;
}
