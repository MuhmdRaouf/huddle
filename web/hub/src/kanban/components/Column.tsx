import type { ComponentChildren } from "preact";
import type { Density } from "../store.ts";
import type { Card as CardData, ColumnId } from "../types.ts";
import type { CardProps } from "./Card.tsx";
import { CardView } from "./Card.tsx";

export type ColumnProps = {
  id: ColumnId;
  title: string;
  cards: CardData[];
  changeSeq: number;
  changedIds: ReadonlySet<string>;
  onOpen: (id: string, opener: HTMLElement) => void;
  /** hands a card's tag chip up to the page's Domain filter */
  onTag?: ((tag: string) => void) | undefined;
  /** the done column's "show all N" button, rendered under the capped list */
  extra?: ComponentChildren;
  /** the column's whole count, when the cards shown are capped (done) */
  count?: number | undefined;
  /** a collapsed column is a 40 px rail naming itself and its count; a click opens it */
  collapsed?: boolean | undefined;
  /** opens the rail's column; absent on a phone, where there are no rails */
  onExpand?: (() => void) | undefined;
  /** the board's density, handed to every card */
  density?: Density | undefined;
  /** hands the thread's lead up when a card is entered, left or focused */
  onThread?: ((id: string | null) => void) | undefined;
  /** the thread mark this column's cards carry, decided by the board */
  threadOf?: ((id: string) => CardProps["thread"]) | undefined;
  /** what every visible card carries, hidden on the cards themselves */
  sharedTags?: readonly string[] | undefined;
  sharedCluster?: string | null | undefined;
};

/** One column: its heading and its cards — or, collapsed, a 40 px rail. */
export function Column({
  id,
  title,
  cards,
  changeSeq,
  changedIds,
  onOpen,
  onTag,
  extra,
  count,
  collapsed = false,
  onExpand,
  density = "compact",
  onThread,
  threadOf,
  sharedTags = [],
  sharedCluster = null,
}: ColumnProps) {
  const cardProps = (card: CardData) => ({
    card,
    flash: changedIds.has(card.id) ? changeSeq : 0,
    onOpen,
    onTag,
    density,
    onThread,
    thread: threadOf?.(card.id),
    sharedTags,
    sharedCluster,
  });
  if (collapsed) {
    const label = `${title}, ${count ?? cards.length} cards, show column`;
    return (
      <section class="col rail" data-column={id}>
        <button type="button" class="rail-btn" aria-expanded="false" aria-label={label} onClick={onExpand}>
          <span class="rail-name">{title}</span>
          <span class="count">{count ?? cards.length}</span>
        </button>
      </section>
    );
  }
  return (
    <section class="col" data-column={id}>
      <h2>
        <span class="col-title">{title} </span>
        <span class="count">{count ?? cards.length}</span>
      </h2>
      {cards.map((card) => (
        <CardView key={card.id} {...cardProps(card)} />
      ))}
      {extra}
    </section>
  );
}
