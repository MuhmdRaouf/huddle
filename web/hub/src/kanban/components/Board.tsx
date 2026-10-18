import type { ComponentChildren } from "preact";
import { useState } from "preact/hooks";
import { type Filters, filterCards, useMedia } from "../store.ts";
import {
  type Card as CardData,
  COLUMN_IDS,
  type ColumnId,
  type ColumnInfo,
  type Snapshot,
} from "../types.ts";
import { CardView } from "./Card.tsx";
import { Column } from "./Column.tsx";

/** The phone layout's column picker: one tab per column, its count a small badge beside its name. */
function TabBar({
  columns,
  active,
  onPick,
}: {
  columns: ColumnInfo[];
  active: ColumnId;
  onPick: (id: ColumnId) => void;
}) {
  return (
    <div class="tabs" role="tablist" aria-label="Columns">
      {columns.map((col) => (
        <button
          key={col.id}
          type="button"
          role="tab"
          class={col.id === active ? "tab active" : "tab"}
          aria-selected={col.id === active}
          onClick={() => onPick(col.id)}
        >
          <span>{col.title}</span>
          <span class="count-badge">{col.count}</span>
        </button>
      ))}
    </div>
  );
}

export type BoardProps = {
  snapshot: Snapshot | null;
  filters: Filters;
  changeSeq: number;
  changedIds: ReadonlySet<string>;
  onOpen: (id: string, opener: HTMLElement) => void;
  /** hands a card's tag chip up to the page's Domain filter */
  onTag?: ((tag: string) => void) | undefined;
};

/** Done shows its latest cards first, capped until the reader asks for the rest. */
const DONE_CAP = 20;

export function Board({ snapshot, filters, changeSeq, changedIds, onOpen, onTag }: BoardProps) {
  const narrow = useMedia("(max-width: 639px)");
  const [showAllDone, setShowAllDone] = useState(false);
  const [tab, setTab] = useState<ColumnId | null>(null);
  if (snapshot === null) {
    return (
      <div class="board" data-layout="loading">
        <div class="loading" role="status" aria-label="Loading the board">
          <div class="skeleton board-skeleton" aria-hidden="true" />
        </div>
      </div>
    );
  }
  const snap = snapshot;
  const visible = filterCards(snap.cards, filters);
  const expedite = visible.filter((card) => card.expedite && card.column !== "done");
  const lifted = new Set(expedite.map((card) => card.id));
  const inColumn = (id: ColumnId) => visible.filter((c) => c.column === id && !lifted.has(c.id));

  const doneKey = (card: CardData): string => (card.finishedAt !== "" ? card.finishedAt : card.updatedAt);
  const doneAll = [...inColumn("done")].sort((a, b) => (doneKey(a) < doneKey(b) ? 1 : -1));
  const doneShown = showAllDone ? doneAll : doneAll.slice(0, DONE_CAP);
  const doneExtra: ComponentChildren =
    !showAllDone && doneAll.length > DONE_CAP ? (
      <button type="button" class="show-all" onClick={() => setShowAllDone(true)}>
        show all {doneAll.length}
      </button>
    ) : null;

  const titleOf = (id: ColumnId): string => snap.columns.find((col) => col.id === id)?.title ?? id;
  const countOf = (id: ColumnId): number => (id === "done" ? doneShown.length : inColumn(id).length);
  const columnInfos = COLUMN_IDS.map((id) => ({ id, title: titleOf(id), count: countOf(id) }));
  const active: ColumnId = tab ?? columnInfos.find((col) => col.count > 0)?.id ?? "backlog";
  const columnProps = (id: ColumnId) => ({
    id,
    title: titleOf(id),
    cards: id === "done" ? doneShown : inColumn(id),
    changeSeq,
    changedIds,
    onOpen,
    onTag,
    extra: id === "done" ? doneExtra : null,
  });

  return (
    <div class="board" data-layout={narrow ? "tabs" : "columns"}>
      {expedite.length > 0 && (
        <section class="expedite" aria-label="Expedite">
          <h2>
            Expedite <span class="count">{expedite.length}</span>
          </h2>
          {expedite.map((card) => (
            <CardView
              key={card.id}
              card={card}
              flash={changedIds.has(card.id) ? changeSeq : 0}
              onOpen={onOpen}
              onTag={onTag}
            />
          ))}
        </section>
      )}
      {narrow ? (
        <>
          <TabBar columns={columnInfos} active={active} onPick={setTab} />
          <Column {...columnProps(active)} />
        </>
      ) : (
        <div class="columns">
          {COLUMN_IDS.map((id) => (
            <Column key={id} {...columnProps(id)} />
          ))}
        </div>
      )}
    </div>
  );
}
