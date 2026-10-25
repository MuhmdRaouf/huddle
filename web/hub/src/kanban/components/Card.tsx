import { memo } from "preact/compat";
import { useEffect, useState } from "preact/hooks";
import type { Density } from "../store.ts";
import { useMedia } from "../store.ts";
import type { Card as CardData } from "../types.ts";
import { Badge } from "./Badge.tsx";

export type CardProps = {
  card: CardData;
  /** the changeSeq of the delta that changed this card; 0 when it did not change */
  flash: number;
  onOpen: (id: string, opener: HTMLElement) => void;
  /** hands a tag chip's tag up, so the page can add it to the Domain filter */
  onTag?: ((tag: string) => void) | undefined;
  /** compact is the board's default face; comfortable adds the summary and the tags */
  density?: Density | undefined;
  /** this card's role in the dependency thread, or none when no thread runs */
  thread?: "self" | "waits-on" | "waited-by" | "dim" | null | undefined;
  /** hands the thread's lead up, so the board can follow the hovered or focused card */
  onThread?: ((id: string | null) => void) | undefined;
  /** tags every visible card carries; they show once in the header row, not here */
  sharedTags?: readonly string[] | undefined;
  /** the one cluster every visible card shares, named in the header row instead */
  sharedCluster?: string | null | undefined;
};

/** The repo tags, each with its own accent, leading a mixed board's tags. */
export const REPO_TAGS: readonly string[] = ["huddle", "cc-plugins"];

/** A card's tags for its face: the repo tags first, the rest in the ticket's order. */
export function orderedTags(tags: string[]): string[] {
  const lead = REPO_TAGS.filter((repo) => tags.includes(repo));
  return [...lead, ...tags.filter((tag) => !REPO_TAGS.includes(tag))];
}

/** The accent class a repo tag chip carries; a plain tag has none. */
export function tagClass(tag: string): string {
  return REPO_TAGS.includes(tag) ? `repo-${tag}` : "";
}

/** The holder's initials: one letter per dash-, underscore- or space-separated segment, two at most. */
export function initials(holder: string): string {
  return holder
    .split(/[-_\s]+/)
    .filter((part) => part !== "")
    .slice(0, 2)
    .map((part) => (part[0] ?? "").toUpperCase())
    .join("");
}

/** The badges that carry state: the ones row 3 keeps after the edge and row 3 itself. */
const STATE_BADGES: readonly string[] = ["waits", "blocked", "incomplete", "hand"];

/**
 * Memoised on the card's identity: applyDelta keeps an untouched card's reference, so a delta
 * re-renders only the cards it carries.
 */
export const CardView = memo(CardViewBase);

function CardViewBase({
  card,
  flash,
  onOpen,
  onTag,
  density = "compact",
  thread = null,
  onThread,
  sharedTags = [],
  sharedCluster = null,
}: CardProps) {
  const reduced = useMedia("(prefers-reduced-motion: reduce)");
  const [lit, setLit] = useState(false);
  useEffect(() => {
    if (flash <= 0 || reduced) {
      setLit(false);
      return;
    }
    setLit(true);
    const timer = setTimeout(() => setLit(false), 1500);
    return () => clearTimeout(timer);
  }, [flash, reduced]);
  const comfortable = density === "comfortable";
  const tags = orderedTags(card.tags).filter((tag) => !sharedTags.includes(tag));
  const cluster = card.cluster !== "" && card.cluster !== sharedCluster ? card.cluster : "";
  return (
    <article
      class={lit ? "card changed" : "card"}
      data-id={card.id}
      data-priority={card.priority}
      data-thread={thread ?? undefined}
      onMouseEnter={() => onThread?.(card.id)}
      onMouseLeave={() => onThread?.(null)}
      onFocusIn={() => onThread?.(card.id)}
      onFocusOut={() => onThread?.(null)}
    >
      <button
        type="button"
        class="card-open"
        aria-label={`${card.id}: ${card.title}`}
        onClick={(event) => onOpen(card.id, event.currentTarget)}
      />
      <header class="card-head">
        {cluster !== "" && <span class="epic">{cluster}</span>}
        <span class="cid">{card.id}</span>
        {card.holder !== "" && (
          <span class="holder" title={card.holder}>
            {initials(card.holder)}
          </span>
        )}
      </header>
      <h3 class="title">{card.title}</h3>
      {comfortable && card.summary !== "" && <p class="summary">{card.summary}</p>}
      <ul class="meta">
        <li class="est">{card.est}</li>
        <li class="type">{card.type}</li>
        {card.expedite && <li class="badge b-expedite">Expedite</li>}
        {card.badges
          .filter((badge) => STATE_BADGES.includes(badge.kind))
          .map((badge) => (
            <Badge key={`${badge.kind}:${badge.label}`} badge={badge} />
          ))}
      </ul>
      {comfortable && tags.length > 0 && (
        <ul class="tags">
          {tags.map((tag) => (
            <li key={tag}>
              <button type="button" class={tagClass(tag)} onClick={() => onTag?.(tag)}>
                {tag}
              </button>
            </li>
          ))}
        </ul>
      )}
    </article>
  );
}
