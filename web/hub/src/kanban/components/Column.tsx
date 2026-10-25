import type { ComponentChildren } from "preact";
import { type Density, type Grouping, groupCards } from "../store.ts";
import type { Card as CardData, ColumnId } from "../types.ts";
import type { CardProps } from "./Card.tsx";
import { CardView } from "./Card.tsx";

/** The phone backlog's groups: how the cards group, which groups are open, and a header's tap. */
export type ColumnGroups = {
  grouping: Grouping;
  open: ReadonlySet<string>;
  onToggle: (key: string) => void;
};

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
  /** collapses the open column back into its rail; absent on a phone, where nothing collapses */
  onCollapse?: (() => void) | undefined;
  /** the board's density, handed to every card */
  density?: Density | undefined;
  /** hands the thread's lead up when a card is entered, left or focused */
  onThread?: ((id: string | null) => void) | undefined;
  /** the thread mark this column's cards carry, decided by the board */
  threadOf?: ((id: string) => CardProps["thread"]) | undefined;
  /** what every visible card carries, hidden on the cards themselves */
  sharedTags?: readonly string[] | undefined;
  sharedCluster?: string | null | undefined;
  /** groups the cards under tappable headers (the phone's backlog); absent, the column is flat */
  groups?: ColumnGroups | undefined;
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
  onCollapse,
  density = "compact",
  onThread,
  threadOf,
  sharedTags = [],
  sharedCluster = null,
  groups,
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
    const total = count ?? cards.length;
    // the name comes from the button's own text, not an aria-label, so the visible name and
    // count stay part of what a screen reader and a voice command hear
    return (
      <section class="col rail" data-column={id}>
        <button type="button" class="rail-btn" aria-expanded="false" onClick={onExpand}>
          <span class="rail-name" aria-hidden="true">
            {title}
          </span>
          <span class="count" aria-hidden="true">
            {total}
          </span>
          <span class="sr-only">{`${title}, ${total} cards, show column`}</span>
        </button>
      </section>
    );
  }
  return (
    <section class="col" data-column={id}>
      <h2>
        <span class="col-head">
          <span class="col-title">{title} </span>
          <span class="count">{count ?? cards.length}</span>
        </span>
        {onCollapse !== undefined && (
          <button
            type="button"
            class="col-collapse"
            aria-expanded="true"
            aria-label={`Collapse ${title}`}
            title="Collapse column"
            onClick={onCollapse}
          >
            <svg
              width="14"
              height="14"
              viewBox="0 0 16 16"
              fill="none"
              stroke="currentColor"
              stroke-width="2"
              stroke-linecap="round"
              stroke-linejoin="round"
              aria-hidden="true"
            >
              <path d="M10 3 5 8l5 5" />
            </svg>
          </button>
        )}
      </h2>
      {groups === undefined ? (
        cards.map((card) => <CardView key={card.id} {...cardProps(card)} />)
      ) : (
        <Groups
          cards={cards}
          groups={groups}
          render={(card) => <CardView key={card.id} {...cardProps(card)} />}
        />
      )}
      {extra}
    </section>
  );
}

/** The grouped cards: expedite cards first and always shown, then one header per group with its
 *  count, the open groups' cards under their headers. */
function Groups({
  cards,
  groups,
  render,
}: {
  cards: CardData[];
  groups: ColumnGroups;
  render: (card: CardData) => ComponentChildren;
}) {
  const grouped = groupCards(
    cards.filter((card) => !card.expedite),
    groups.grouping,
  );
  return (
    <>
      {cards.filter((card) => card.expedite).map(render)}
      {grouped.map(({ key, label, cards: members }) => {
        const open = groups.open.has(key);
        return (
          <div key={key} class="group">
            <button
              type="button"
              class="group-head"
              aria-expanded={open ? "true" : "false"}
              onClick={() => groups.onToggle(key)}
            >
              <span class="group-name">{label}</span> <span class="count">{members.length}</span>
            </button>
            {open && members.map(render)}
          </div>
        );
      })}
    </>
  );
}
