import { memo } from "preact/compat";
import { useEffect, useState } from "preact/hooks";
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

/**
 * Memoised on the card's identity: applyDelta keeps an untouched card's reference, so a delta
 * re-renders only the cards it carries.
 */
export const CardView = memo(CardViewBase);

function CardViewBase({ card, flash, onOpen, onTag }: CardProps) {
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
  const tags = orderedTags(card.tags);
  return (
    <article class={lit ? "card changed" : "card"} data-id={card.id}>
      <button
        type="button"
        class="card-open"
        aria-label={`${card.id}: ${card.title}`}
        onClick={(event) => onOpen(card.id, event.currentTarget)}
      />
      <header class="card-head">
        <span class="epic">{card.cluster}</span>
        <span class="cid">{card.id}</span>
      </header>
      <h3 class="title">{card.title}</h3>
      {card.summary !== "" && <p class="summary">{card.summary}</p>}
      <ul class="badges">
        {card.badges.map((badge) => (
          <Badge key={`${badge.kind}:${badge.label}`} badge={badge} />
        ))}
      </ul>
      {tags.length > 0 && (
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
      {card.holder !== "" && (
        <span class="holder" title={card.holder}>
          {initials(card.holder)}
        </span>
      )}
    </article>
  );
}
