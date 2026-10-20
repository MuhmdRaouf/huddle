/**
 * The board's state: the snapshot, the deltas applied to it, the filters over it, and the live/static
 * mode. Everything here is pure or injectable, so the tests drive it without a server.
 */

import { useEffect, useState } from "preact/hooks";
import type {
  Card,
  ColumnId,
  Delta,
  Snapshot,
  StaticBoard,
  Stream,
  StreamHandlers,
  Ticket,
} from "./types.ts";

export type BoardState = {
  mode: "live" | "static";
  snapshot: Snapshot | null;
  connected: boolean;
  /** bumped once per applied delta, so a card changed twice re-triggers its flash */
  changeSeq: number;
  changedIds: ReadonlySet<string>;
};

export const initialBoardState = (): BoardState => ({
  mode: "live",
  snapshot: null,
  connected: false,
  changeSeq: 0,
  changedIds: new Set(),
});

export type Io = {
  fetchSnapshot: () => Promise<Snapshot>;
  fetchTicket: (id: string) => Promise<Ticket>;
  connectEvents: (handlers: StreamHandlers) => Stream;
  setTimeout: (fn: () => void, ms: number) => unknown;
  clearTimeout: (handle: unknown) => void;
};

export type FilterKind = "cluster" | "tag" | "holder" | "type" | "priority";

export const FILTER_KINDS: FilterKind[] = ["cluster", "tag", "holder", "type", "priority"];

export type Filters = { q: string; picks: Record<FilterKind, string[]>; hideDone: boolean };

export const EMPTY_FILTERS: Filters = {
  q: "",
  picks: { cluster: [], tag: [], holder: [], type: [], priority: [] },
  hideDone: false,
};

/** Above this many done cards, a URL that says nothing about done means hide. */
export const DONE_HIDE_THRESHOLD = 50;

/** The filters with one kind's pick of a value added or dropped. */
export function togglePick(filters: Filters, kind: FilterKind, value: string): Filters {
  const picked = filters.picks[kind];
  const next = picked.includes(value) ? picked.filter((v) => v !== value) : [...picked, value];
  return { ...filters, picks: { ...filters.picks, [kind]: next } };
}

/** The filters with every pick dropped; the search and hide-done stay. */
export function clearPicks(filters: Filters): Filters {
  return { ...filters, picks: { ...EMPTY_FILTERS.picks } };
}

/** The filters with the picks and the search dropped; hide-done stays, being a mode. */
export function clearFilters(filters: Filters): Filters {
  return { q: "", picks: { ...EMPTY_FILTERS.picks }, hideDone: filters.hideDone };
}

/** The board's two densities: compact is the default, comfortable adds the summary and tags. */
export type Density = "compact" | "comfortable";

/** The localStorage key the density preference lives under. */
export const DENSITY_KEY = "kanban.density";

/** The stored density, compact for anything else; a storage that throws reads as compact. */
export function readDensity(storage: { getItem(key: string): string | null } | null | undefined): Density {
  try {
    return storage?.getItem(DENSITY_KEY) === "comfortable" ? "comfortable" : "compact";
  } catch {
    return "compact";
  }
}

/** Remembers the density; a storage that throws is left alone. */
export function writeDensity(
  storage: { setItem(key: string, value: string): void } | null | undefined,
  density: Density,
): void {
  try {
    storage?.setItem(DENSITY_KEY, density);
  } catch {
    // the toggle still holds for this visit
  }
}

/** The board's density with its toggle: read once, remembered on every change. */
export function useDensity(): [Density, (density: Density) => void] {
  const [density, setDensity] = useState<Density>(() => readDensity(window.localStorage));
  const set = (next: Density): void => {
    setDensity(next);
    writeDensity(window.localStorage, next);
  };
  return [density, set];
}

/** Apply one delta: upserts replace in place (order kept, new ids appended), removes drop out. */
export function applyDelta(snapshot: Snapshot, delta: Delta): Snapshot {
  const upserts = new Map(delta.upsert.map((card) => [card.id, card]));
  const removed = new Set(delta.remove);
  const cards = snapshot.cards
    .filter((card) => !removed.has(card.id))
    .map((card) => upserts.get(card.id) ?? card);
  for (const card of delta.upsert) {
    if (!snapshot.cards.some((existing) => existing.id === card.id)) cards.push(card);
  }
  const next: Snapshot = { ...snapshot, rev: delta.rev, cards };
  if (delta.meta !== undefined) {
    next.epoch = delta.meta.epoch;
    next.stopLine = delta.meta.stopLine;
    next.columns = delta.meta.columns;
  }
  return next;
}

/** A delta that does not follow the snapshot exactly (a gap or a repeat) means we missed something. */
export function isRevGap(snapshot: Snapshot, delta: Delta): boolean {
  return delta.rev !== snapshot.rev + 1;
}

/** The ids whose updatedAt the delta changed: the cards that flash. A card first seen does not flash. */
export function changedIds(cards: Card[], delta: Delta): Set<string> {
  const before = new Map(cards.map((card) => [card.id, card.updatedAt]));
  const ids = new Set<string>();
  for (const card of delta.upsert) {
    const was = before.get(card.id);
    if (was !== undefined && was !== card.updatedAt) ids.add(card.id);
  }
  return ids;
}

/** 1, 2, 4, 8, 8... seconds: the reconnect ladder, capped at 8. */
export function backoffMs(attempt: number): number {
  return Math.min(1000 * 2 ** attempt, 8000);
}

/** Static mode: the export payload on the window decides it; there is nothing to connect to. */
export function staticBoardState(win: { __KANBAN__?: StaticBoard } | null | undefined): BoardState | null {
  const board = win?.__KANBAN__;
  if (board === undefined) return null;
  return { ...initialBoardState(), mode: "static", snapshot: board.snapshot };
}

/** The values a card carries for one filter kind. */
export function valuesOfKind(card: Card, kind: FilterKind): string[] {
  switch (kind) {
    case "cluster":
      return [card.cluster];
    case "tag":
      return card.tags;
    case "holder":
      return card.holder === "" ? [] : [card.holder];
    case "type":
      return [card.type];
    case "priority":
      return [card.priority];
  }
}

/** Within a kind any picked value matches; across kinds every picked kind must match. */
export function matchesFilters(card: Card, filters: Filters): boolean {
  const q = filters.q.trim().toLowerCase();
  if (q !== "") {
    const haystack = [card.id, card.title, card.summary, ...card.tags];
    if (!haystack.some((text) => text.toLowerCase().includes(q))) return false;
  }
  for (const kind of FILTER_KINDS) {
    const picked = filters.picks[kind];
    if (picked.length === 0) continue;
    const values = valuesOfKind(card, kind);
    if (!picked.some((value) => values.includes(value))) return false;
  }
  return !(filters.hideDone && card.column === "done");
}

export function filterCards(cards: Card[], filters: Filters): Card[] {
  return cards.filter((card) => matchesFilters(card, filters));
}

/** The cards a facet menu counts: they pass every filter except this kind's own picks. */
function facetBase(cards: Card[], filters: Filters, kind: FilterKind): Card[] {
  return cards.filter((card) =>
    matchesFilters(card, { ...filters, picks: { ...filters.picks, [kind]: [] } }),
  );
}

export type Facet = { value: string; count: number };

/** One kind's menu values over the other filters' cards: a value's count, sorted by count then value. */
export function facetValues(cards: Card[], filters: Filters, kind: FilterKind): Facet[] {
  const counts = new Map<string, number>();
  for (const card of facetBase(cards, filters, kind)) {
    for (const value of valuesOfKind(card, kind)) counts.set(value, (counts.get(value) ?? 0) + 1);
  }
  return [...counts.entries()]
    .map(([value, count]) => ({ value, count }))
    .sort((a, b) => b.count - a.count || (a.value < b.value ? -1 : 1));
}

/** The columns whose holders a session is working right now; the rest read as earlier. */
const WORKING_COLUMNS: readonly ColumnId[] = ["ready", "in-progress", "review", "testing", "blocked"];

/** True when the holder works a card now among the cards that pass the other filters. */
export function holderWorkingNow(cards: Card[], filters: Filters, value: string): boolean {
  return facetBase(cards, filters, "holder").some(
    (card) => card.holder === value && WORKING_COLUMNS.includes(card.column),
  );
}

/** The filters a URL query carries, with or without the leading question mark. A URL that says
 *  nothing about done hides it on a done-heavy board (`doneCount`, 0 while the board is unknown). */
export function filtersFromSearch(search: string, doneCount = 0): Filters {
  const params = new URLSearchParams(search);
  const picks: Record<FilterKind, string[]> = { ...EMPTY_FILTERS.picks };
  for (const kind of FILTER_KINDS) {
    const raw = params.get(kind);
    if (raw !== null)
      picks[kind] = raw
        .split(",")
        .map(decodeURIComponent)
        .filter((v) => v !== "");
  }
  const done = params.get("done");
  const hideDone = done === "hide" ? true : done === "show" ? false : doneCount > DONE_HIDE_THRESHOLD;
  return { q: params.get("q") ?? "", picks, hideDone };
}

/** The query for the filters, holding only what is set: the empty filter has no query. On a
 *  done-heavy board silence reads as hide, so showing done has to say so. */
export function searchFromFilters(filters: Filters, doneCount = 0): string {
  const params = new URLSearchParams();
  if (filters.q !== "") params.set("q", filters.q);
  for (const kind of FILTER_KINDS) {
    const picked = filters.picks[kind].filter((value) => value !== "");
    if (picked.length > 0) params.set(kind, picked.map(encodeURIComponent).join(","));
  }
  if (filters.hideDone) params.set("done", "hide");
  else if (doneCount > DONE_HIDE_THRESHOLD) params.set("done", "show");
  return params.toString();
}

/**
 * The live board: one snapshot fetched, one stream followed. Deltas apply in place; a gap or a reload
 * refetches; a stream error hands the schedule back to our own backoff ladder.
 */
export class BoardStore {
  private state = initialBoardState();
  private readonly listeners = new Set<() => void>();
  private stream: Stream | null = null;
  private timer: unknown = null;
  private attempt = 0;
  private readonly bodies = new Map<string, { at: string; ticket: Ticket }>();

  constructor(private readonly io: Io) {}

  getState(): BoardState {
    return this.state;
  }

  subscribe(fn: () => void): () => void {
    this.listeners.add(fn);
    return () => {
      this.listeners.delete(fn);
    };
  }

  private emit(): void {
    for (const fn of this.listeners) fn();
  }

  async start(): Promise<void> {
    await this.loadSnapshot();
    this.openStream();
  }

  private async loadSnapshot(): Promise<void> {
    try {
      const snapshot = await this.io.fetchSnapshot();
      this.state = { ...this.state, snapshot };
      this.emit();
    } catch {
      this.scheduleReconnect();
    }
  }

  private openStream(): void {
    this.closeStream();
    this.stream = this.io.connectEvents({
      onSnapshot: (snapshot) => {
        this.attempt = 0;
        this.state = { ...this.state, snapshot, connected: true };
        this.emit();
      },
      onDelta: (delta) => this.receive(delta),
      onReload: () => {
        void this.loadSnapshot();
      },
      onOpen: () => {
        this.attempt = 0;
        this.state = { ...this.state, connected: true };
        this.emit();
      },
      onError: () => this.scheduleReconnect(),
    });
  }

  private closeStream(): void {
    this.stream?.close();
    this.stream = null;
  }

  private receive(delta: Delta): void {
    const snap = this.state.snapshot;
    if (snap === null || isRevGap(snap, delta)) {
      void this.loadSnapshot();
      return;
    }
    this.state = {
      ...this.state,
      snapshot: applyDelta(snap, delta),
      changeSeq: this.state.changeSeq + 1,
      changedIds: changedIds(snap.cards, delta),
    };
    this.emit();
  }

  private scheduleReconnect(): void {
    this.closeStream();
    if (this.timer !== null) this.io.clearTimeout(this.timer);
    const ms = backoffMs(this.attempt);
    this.attempt += 1;
    this.state = { ...this.state, connected: false };
    this.emit();
    this.timer = this.io.setTimeout(() => {
      this.timer = null;
      this.openStream();
    }, ms);
  }

  /** The ticket's body, cached while the card's updatedAt holds. */
  async ticket(id: string): Promise<Ticket> {
    const card = this.state.snapshot?.cards.find((c) => c.id === id) ?? null;
    const cached = this.bodies.get(id);
    if (cached !== undefined && (card === null || card.updatedAt === cached.at)) return cached.ticket;
    const ticket = await this.io.fetchTicket(id);
    this.bodies.set(id, { at: ticket.card.updatedAt, ticket });
    return ticket;
  }

  /** Stops following: the stream closes and a pending reconnect never fires (a board switch or
   *  a tab unmount, where the store has no owner left to hand the schedule to). */
  stop(): void {
    this.closeStream();
    if (this.timer !== null) {
      this.io.clearTimeout(this.timer);
      this.timer = null;
    }
    this.listeners.clear();
  }
}

/** A media query as reactive state: the phone tab bar and reduced motion both read through this. */
export function useMedia(query: string): boolean {
  const [matches, setMatches] = useState(false);
  useEffect(() => {
    const queryList = window.matchMedia(query);
    const update = (): void => setMatches(queryList.matches);
    update();
    queryList.addEventListener("change", update);
    return () => queryList.removeEventListener("change", update);
  }, [query]);
  return matches;
}
