import type { ComponentChildren } from "preact";
import type { Card as CardData, ColumnId } from "../types.ts";
import { CardView } from "./Card.tsx";

export type ColumnProps = {
  id: ColumnId;
  title: string;
  cards: CardData[];
  changeSeq: number;
  changedIds: ReadonlySet<string>;
  onOpen: (id: string, opener: HTMLElement) => void;
  /** the done column's "show all N" button, rendered under the capped list */
  extra?: ComponentChildren;
};

export function Column({ id, title, cards, changeSeq, changedIds, onOpen, extra }: ColumnProps) {
  return (
    <section class="col" data-column={id}>
      <h2>
        {title} <span class="count">{cards.length}</span>
      </h2>
      {cards.map((card) => (
        <CardView key={card.id} card={card} flash={changedIds.has(card.id) ? changeSeq : 0} onOpen={onOpen} />
      ))}
      {extra}
    </section>
  );
}
