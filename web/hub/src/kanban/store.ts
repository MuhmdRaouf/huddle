/**
 * The board's state: the snapshot, the deltas applied to it, the filters over it, and the live/static
 * mode. Everything here is pure or injectable, so the tests drive it without a server.
 */

import { useEffect, useState } from "preact/hooks";
import { silenceWatchdog, type Watchdog } from "../shared/watchdog.ts";
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
  /** when the stream's last frame came (epoch ms), the ping included; null before the first */
  lastFrameAt: number | null;
  /** bumped once per applied delta, so a card changed twice re-triggers its flash */
  changeSeq: number;
  changedIds: ReadonlySet<string>;
};

export const initialBoardState = (): BoardState => ({
  mode: "live",
  snapshot: null,
  connected: false,
  lastFrameAt: null,
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

/** The empty filters: the picks and the search dropped and done shown, so a cleared board shows
 *  every card it holds. */
export function clearFilters(): Filters {
  return { ...EMPTY_FILTERS, picks: { ...EMPTY_FILTERS.picks } };
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

/** The window's localStorage, or none: the getter itself throws in a private window or a blocked
 *  partition, before any read or write could. */
function localStore(): Storage | null {
  try {
    return window.localStorage;
  } catch {
    return null;
  }
}

/** The board's density with its toggle: read once, remembered on every change. */
export function useDensity(): [Density, (density: Density) => void] {
  const [density, setDensity] = useState<Density>(() => readDensity(localStore()));
  const set = (next: Density): void => {
    setDensity(next);
    writeDensity(localStore(), next);
  };
  return [density, set];
}

/** How the phone's backlog groups its cards. */
export type Grouping = "priority" | "cluster";

/** The phone backlog's grouping and the keys of its open groups. */
export type BacklogView = { grouping: Grouping; open: string[] };

/** The localStorage key prefix the backlog view lives under, one entry per board. */
export const GROUPS_KEY = "kanban.groups";

/** A grouping's view before the reader opens anything: P0 open by priority, every cluster closed. */
export function defaultView(grouping: Grouping): BacklogView {
  return { grouping, open: grouping === "priority" ? ["P0"] : [] };
}

/** The board's stored backlog view; nothing stored, junk or a storage that throws reads as the
 *  priority default. */
export function readGroups(
  storage: { getItem(key: string): string | null } | null | undefined,
  board: string,
): BacklogView {
  try {
    const raw = storage?.getItem(`${GROUPS_KEY}.${board}`);
    if (raw === null || raw === undefined) return defaultView("priority");
    const parsed: unknown = JSON.parse(raw);
    if (typeof parsed !== "object" || parsed === null) return defaultView("priority");
    const { grouping, open } = parsed as { grouping?: unknown; open?: unknown };
    if (grouping !== "priority" && grouping !== "cluster") return defaultView("priority");
    if (!Array.isArray(open) || !open.every((key) => typeof key === "string")) return defaultView("priority");
    return { grouping, open: open as string[] };
  } catch {
    return defaultView("priority");
  }
}

/** Remembers the board's backlog view; a storage that throws is left alone. */
export function writeGroups(
  storage: { setItem(key: string, value: string): void } | null | undefined,
  board: string,
  view: BacklogView,
): void {
  try {
    storage?.setItem(`${GROUPS_KEY}.${board}`, JSON.stringify(view));
  } catch {
    // the view still holds for this visit
  }
}

/** The board's backlog view with its setter: read again whenever the board changes, remembered
 *  on every change. */
export function useBacklogView(board: string): [BacklogView, (view: BacklogView) => void] {
  const [held, setHeld] = useState(() => ({ board, view: readGroups(localStore(), board) }));
  const current = held.board === board ? held : { board, view: readGroups(localStore(), board) };
  if (current !== held) setHeld(current);
  const set = (view: BacklogView): void => {
    setHeld({ board, view });
    writeGroups(localStore(), board, view);
  };
  return [current.view, set];
}

/** One group of the phone's backlog: its key, the name its header shows, and its cards. */
export type CardGroup = { key: string; label: string; cards: Card[] };

/** The cards grouped by priority or cluster, the groups in key order and each keeping the cards'
 *  order; a card with no value groups under "No priority" or "No cluster". */
export function groupCards(cards: Card[], grouping: Grouping): CardGroup[] {
  const byKey = new Map<string, Card[]>();
  for (const card of cards) {
    const key = grouping === "priority" ? card.priority : card.cluster;
    byKey.set(key, [...(byKey.get(key) ?? []), card]);
  }
  const none = grouping === "priority" ? "No priority" : "No cluster";
  return [...byKey.entries()]
    .sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))
    .map(([key, members]) => ({ key, label: key === "" ? none : key, cards: members }));
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

/** How long a live stream may go without a frame before the store replaces it: the server pings
 *  every 15 s, so this is two missed pings and a margin. */
export const SILENCE_MS = 40_000;

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

/** How many done cards hide-done keeps off the board: the done cards every other filter lets
 *  through, and 0 while done is shown. */
export function hiddenDone(cards: Card[], filters: Filters): number {
  if (!filters.hideDone) return 0;
  const shown = { ...filters, hideDone: false };
  return cards.filter((card) => card.column === "done" && matchesFilters(card, shown)).length;
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
 * refetches; a stream error hands the schedule back to our own backoff ladder; a stream silent for
 * SILENCE_MS is replaced, since a half-open socket never errors.
 */
export class BoardStore {
  private state: BoardState;
  private readonly listeners = new Set<() => void>();
  private stream: Stream | null = null;
  private timer: unknown = null;
  private attempt = 0;
  private watchdog: Watchdog | null = null;
  private stopped = false;
  private readonly bodies = new Map<string, { at: string; ticket: Ticket }>();

  /** A seed past the live initial state puts the store straight into that mode — the static
   *  state an export carries, which has no stream to open and never schedules a reconnect. */
  constructor(
    private readonly io: Io,
    seed: BoardState | null = null,
  ) {
    this.state = seed ?? initialBoardState();
  }

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

  /** Reads the board, then opens the stream; a failed read leaves the opening to the ladder. */
  async start(): Promise<void> {
    if ((await this.loadSnapshot()) && !this.stopped) this.openStream();
  }

  /** Replaces the stream: the dot goes off, the board is read afresh and a new stream opens. The
   *  watchdog calls it on a silent stream, the tab on a page back from a long time away. */
  async resume(): Promise<void> {
    if (this.state.mode !== "live" || this.stopped) return;
    this.closeStream();
    if (this.timer !== null) {
      this.io.clearTimeout(this.timer);
      this.timer = null;
    }
    this.attempt = 0;
    this.state = { ...this.state, connected: false };
    this.emit();
    await this.start();
  }

  /** True once the board is read; a failed read schedules a reconnect and answers false. */
  private async loadSnapshot(): Promise<boolean> {
    try {
      const snapshot = await this.io.fetchSnapshot();
      this.state = { ...this.state, snapshot };
      this.emit();
      return true;
    } catch {
      this.scheduleReconnect();
      return false;
    }
  }

  private openStream(): void {
    this.closeStream();
    if (this.state.mode === "live") {
      this.watchdog = silenceWatchdog(this.io, SILENCE_MS, () => void this.resume());
    }
    // every frame is a sign of life, the ping that carries nothing included
    const alive = (): void => {
      this.watchdog?.feed();
      this.state = { ...this.state, lastFrameAt: Date.now() };
    };
    this.stream = this.io.connectEvents({
      onSnapshot: (snapshot) => {
        alive();
        this.attempt = 0;
        this.state = { ...this.state, snapshot, connected: true };
        this.emit();
      },
      onDelta: (delta) => {
        alive();
        this.receive(delta);
      },
      onReload: () => {
        alive();
        void this.loadSnapshot();
      },
      onPing: () => {
        alive();
        this.emit();
      },
      onOpen: () => {
        alive();
        this.attempt = 0;
        this.state = { ...this.state, connected: true };
        this.emit();
      },
      onError: () => this.scheduleReconnect(),
    });
  }

  private closeStream(): void {
    this.watchdog?.stop();
    this.watchdog = null;
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
    if (this.stopped) return;
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
    this.stopped = true;
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
