import { useCallback, useEffect, useRef, useState } from "preact/hooks";
import { type Density, type Filters, filterCards, hiddenDone, useMedia } from "../store.ts";
import {
  type Card as CardData,
  COLUMN_IDS,
  type ColumnId,
  type ColumnInfo,
  type Snapshot,
} from "../types.ts";
import type { CardProps } from "./Card.tsx";
import { Column, type ColumnGroups } from "./Column.tsx";
import { FlowBar } from "./FlowBar.tsx";

export type BoardProps = {
  snapshot: Snapshot | null;
  filters: Filters;
  changeSeq: number;
  changedIds: ReadonlySet<string>;
  onOpen: (id: string, opener: HTMLElement) => void;
  /** hands a card's tag chip up to the page's Domain filter */
  onTag?: ((tag: string) => void) | undefined;
  /** the card open in the panel: it holds the thread while no card is hovered or focused */
  openId?: string | null | undefined;
  /** drops the filters from the "No cards match these filters" line */
  onClearFilters?: (() => void) | undefined;
  /** turns hide-done off from the "N done hidden" line */
  onShowDone?: (() => void) | undefined;
  /** the board's density, handed to every card */
  density?: Density | undefined;
  /** tags every visible card carries; the header names them once, the cards never */
  sharedTags?: readonly string[] | undefined;
  /** the one cluster every visible card shares, named once in the header row */
  sharedCluster?: string | null | undefined;
  /** the backlog's groups, applied on a phone only: the desktop's columns stay flat */
  backlogGroups?: ColumnGroups | undefined;
};

/** Done shows its latest cards first, capped until the reader asks for the rest. */
const DONE_CAP = 20;

/** The track a column sits in: a rail's 40 px, or the card-holding minmax. */
const TRACK = "minmax(260px, 380px)";
const RAIL_TRACK = "40px";

/** The done cards latest first, ordering by finishedAt and falling back to updatedAt. */
function doneFirst(cards: CardData[]): CardData[] {
  const key = (card: CardData): string => (card.finishedAt !== "" ? card.finishedAt : card.updatedAt);
  return [...cards].sort((a, b) => (key(a) < key(b) ? 1 : -1));
}

/** A column's cards with its expedite cards first, each group keeping the snapshot's order. */
function expediteFirst(cards: CardData[]): CardData[] {
  return [...cards.filter((card) => card.expedite), ...cards.filter((card) => !card.expedite)];
}

/** Every column in flow order, counting its cards; done counts the whole column. */
function columnInfos(
  snap: Snapshot,
  perColumn: (id: ColumnId) => CardData[],
  doneTotal: number,
): ColumnInfo[] {
  return COLUMN_IDS.map((id) => ({
    id,
    title: snap.columns.find((col) => col.id === id)?.title ?? id,
    count: id === "done" ? doneTotal : perColumn(id).length,
  }));
}

/** The card the thread hangs from: the hovered or focused one, else the one open in the panel —
 *  and only while its own card is on the board. */
function activeThreadId(hovered: string | null, openId: string | null, visible: CardData[]): string | null {
  if (hovered !== null) return hovered;
  return openId !== null && visible.some((card) => card.id === openId) ? openId : null;
}

/** Builds the thread marker the board hands its cards, from the lead's dependency sets. */
function threadMarker(threadId: string | null, visible: CardData[]): (id: string) => CardProps["thread"] {
  const lead = threadId === null ? null : (visible.find((card) => card.id === threadId) ?? null);
  const waitsOn = new Set(lead?.deps.map((dep) => dep.id) ?? []);
  const waitedBy = new Set(
    lead === null
      ? []
      : visible.filter((card) => card.deps.some((dep) => dep.id === lead.id)).map((card) => card.id),
  );
  return (id: string): CardProps["thread"] => {
    if (lead === null) return null;
    if (id === lead.id) return "self";
    if (waitsOn.has(id)) return "waits-on";
    if (waitedBy.has(id)) return "waited-by";
    return "dim";
  };
}

/** A hand-set override wins: a collapsed-by-hand column stays a rail and an opened-by-hand column
 *  stays open, cards or none. Without one, done is a rail until it is opened and so is any column
 *  the filters emptied. A phone shows one column per screen, so nothing collapses there. */
function isCollapsed(
  id: ColumnId,
  narrow: boolean,
  overrides: ReadonlyMap<ColumnId, boolean>,
  infos: ColumnInfo[],
): boolean {
  if (narrow) return false;
  const byHand = overrides.get(id);
  if (byHand !== undefined) return byHand;
  return id === "done" || (infos.find((col) => col.id === id)?.count ?? 0) === 0;
}

/** The line a board hiding its done cards carries: how many it hides, and the way to show them.
 *  A board hiding nothing carries no line. */
function DoneHidden({ count, onShow }: { count: number; onShow?: (() => void) | undefined }) {
  if (count === 0) return null;
  return (
    <p class="done-hidden" role="status">
      {count} done hidden{" "}
      {onShow !== undefined && (
        <button type="button" class="show-done" onClick={onShow}>
          Show done
        </button>
      )}
    </p>
  );
}

/** The two empty lines: a board with no cards yet, and filters that hide everything. */
function BoardEmpty({
  empty,
  onClearFilters,
}: {
  empty: boolean;
  onClearFilters?: (() => void) | undefined;
}) {
  return (
    <p class="board-empty" role="status">
      {empty ? (
        <>
          No cards yet. Add one with <code>kanban new</code> in this repo.
        </>
      ) : (
        <>
          No cards match these filters.{" "}
          {onClearFilters !== undefined && (
            <button type="button" class="clear-filters" onClick={onClearFilters}>
              Clear filters
            </button>
          )}
        </>
      )}
    </p>
  );
}

export function Board({
  snapshot,
  filters,
  changeSeq,
  changedIds,
  onOpen,
  onTag,
  openId = null,
  onClearFilters,
  onShowDone,
  density = "compact",
  sharedTags = [],
  sharedCluster = null,
  backlogGroups,
}: BoardProps) {
  const narrow = useMedia("(max-width: 639px)");
  const reduced = useMedia("(prefers-reduced-motion: reduce)");
  const [showAllDone, setShowAllDone] = useState(false);
  const [tab, setTab] = useState<ColumnId | null>(null);
  const [overrides, setOverrides] = useState<ReadonlyMap<ColumnId, boolean>>(new Map());
  const [hovered, setHovered] = useState<string | null>(null);
  const columnsRef = useRef<HTMLDivElement>(null);
  const lanesRef = useRef<HTMLDivElement | null>(null);
  // the Kanban header's height above the phone's strip, kept on the tab as --kanban-head: the
  // strip's height changes with the header's, so watching the strip catches every change
  const unwatchHead = useRef<(() => void) | null>(null);
  const mountLanes = useCallback((lanes: HTMLDivElement | null) => {
    lanesRef.current = lanes;
    unwatchHead.current?.();
    unwatchHead.current = null;
    const tab = lanes?.closest<HTMLElement>(".kanban-tab");
    if (lanes == null || tab == null) return;
    const watch = new ResizeObserver(() => {
      const head = lanes.getBoundingClientRect().top - tab.getBoundingClientRect().top;
      tab.style.setProperty("--kanban-head", `${Math.round(head)}px`);
    });
    watch.observe(lanes);
    unwatchHead.current = () => {
      watch.disconnect();
      tab.style.removeProperty("--kanban-head");
    };
  }, []);
  // the phone's strip opens on the first column with cards, once per mount of the strip
  const opening = useRef<ColumnId | null>(null);
  const placed = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    const lanes = lanesRef.current;
    if (lanes === null || placed.current === lanes || opening.current === null) return;
    placed.current = lanes;
    lanes.scrollLeft = COLUMN_IDS.indexOf(opening.current) * lanes.clientWidth;
  });

  // a hand-set collapse or open stays until the next filter change; resetting in render, on the
  // changed identity, keeps the reset in the same commit as the filters that caused it
  const [seenFilters, setSeenFilters] = useState(filters);
  if (seenFilters !== filters) {
    setSeenFilters(filters);
    setOverrides(new Map());
  }

  const expand = useCallback((id: ColumnId) => {
    setOverrides((current) => new Map(current).set(id, false));
  }, []);
  const collapse = useCallback((id: ColumnId) => {
    setOverrides((current) => new Map(current).set(id, true));
  }, []);
  const onThread = useCallback((id: string | null) => setHovered(id), []);

  if (snapshot === null) {
    return (
      <div class="board" data-layout="loading">
        <div class="kanban-loading" role="status" aria-label="Loading the board">
          <div class="skeleton board-skeleton" aria-hidden="true" />
        </div>
      </div>
    );
  }
  const snap = snapshot;
  const visible = filterCards(snap.cards, filters);
  const hidden = hiddenDone(snap.cards, filters);
  const hiddenLine = <DoneHidden count={hidden} onShow={onShowDone} />;
  if (snap.cards.length === 0 || visible.length === 0) {
    return (
      <div class="board" data-layout="columns">
        <FlowBar columns={columnInfos(snap, () => [], hidden)} onPick={() => undefined} />
        {hiddenLine}
        <BoardEmpty empty={snap.cards.length === 0} onClearFilters={onClearFilters} />
      </div>
    );
  }

  const inColumn = (id: ColumnId) => expediteFirst(visible.filter((c) => c.column === id));
  const doneAll = doneFirst(inColumn("done"));
  const doneShown = showAllDone ? doneAll : doneAll.slice(0, DONE_CAP);
  const doneExtra =
    !showAllDone && doneAll.length > DONE_CAP ? (
      <button type="button" class="show-all" onClick={() => setShowAllDone(true)}>
        Show all {doneAll.length}
      </button>
    ) : null;

  // done counts its hidden cards too, so a hiding board never reads as finished-and-empty
  const doneTotal = doneAll.length + hidden;
  const infos = columnInfos(snap, inColumn, doneTotal);
  const active: ColumnId = tab ?? infos.find((col) => col.count > 0)?.id ?? "backlog";
  const collapsedOf = (id: ColumnId) => isCollapsed(id, narrow, overrides, infos);
  const threadOf = threadMarker(activeThreadId(hovered, openId, visible), visible);
  const columnProps = (id: ColumnId) => ({
    id,
    title: snap.columns.find((col) => col.id === id)?.title ?? id,
    cards: id === "done" ? doneShown : inColumn(id),
    count: id === "done" ? doneTotal : inColumn(id).length,
    changeSeq,
    changedIds,
    onOpen,
    onTag,
    extra: id === "done" ? doneExtra : null,
    collapsed: collapsedOf(id),
    onExpand: () => expand(id),
    onCollapse: narrow ? undefined : () => collapse(id),
    density,
    onThread,
    threadOf,
    sharedTags,
    sharedCluster,
    groups: narrow && id === "backlog" ? backlogGroups : undefined,
  });

  opening.current = active;
  const behavior = reduced ? "auto" : "smooth";

  // a flow label hands its column up: on a phone it moves the strip to it, here it scrolls the grid
  const pickColumn = (id: ColumnId): void => {
    if (narrow) {
      setTab(id);
      const lanes = lanesRef.current;
      lanes?.scrollTo({ left: COLUMN_IDS.indexOf(id) * lanes.clientWidth, behavior });
      return;
    }
    columnsRef.current
      ?.querySelector(`section[data-column="${id}"]`)
      ?.scrollIntoView({ behavior, block: "nearest", inline: "start" });
  };

  // a swipe settles the strip on one screen: the column under it is the selected tab
  const onLanesScroll = (event: Event): void => {
    const lanes = event.currentTarget as HTMLElement;
    if (lanes.clientWidth === 0) return;
    const shown = COLUMN_IDS[Math.round(lanes.scrollLeft / lanes.clientWidth)];
    if (shown !== undefined && shown !== active) setTab(shown);
  };

  return (
    <div class="board" data-layout={narrow ? "lanes" : "columns"}>
      <FlowBar columns={infos} onPick={pickColumn} activeId={narrow ? active : undefined} />
      {hiddenLine}
      {narrow ? (
        <div ref={mountLanes} class="lanes" onScroll={onLanesScroll}>
          {COLUMN_IDS.map((id) => (
            <Column key={id} {...columnProps(id)} />
          ))}
        </div>
      ) : (
        <div
          ref={columnsRef}
          class="columns"
          style={{
            gridTemplateColumns: COLUMN_IDS.map((id) => (collapsedOf(id) ? RAIL_TRACK : TRACK)).join(" "),
          }}
        >
          {COLUMN_IDS.map((id) => (
            <Column key={id} {...columnProps(id)} />
          ))}
        </div>
      )}
    </div>
  );
}
