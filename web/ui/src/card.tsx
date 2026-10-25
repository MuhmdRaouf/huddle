// card.tsx — one block of content on a surface: an optional header (title, a muted note, actions) over
// the body. The card is a named region, so a screen reader can jump between cards.

import type { ComponentChildren, JSX } from "preact";
import { useId } from "preact/hooks";

/** What a card holds. */
export type CardProps = {
  title?: ComponentChildren;
  /** A count or short note beside the title. */
  meta?: ComponentChildren;
  actions?: ComponentChildren;
  /** The region's name when the card has no title. */
  label?: string | undefined;
  children: ComponentChildren;
};

/** A card: named by its title, or by `label` when it has none. */
export function Card({ title, meta, actions, label, children }: CardProps): JSX.Element {
  const id = useId();
  const titled = title !== undefined;
  return (
    <section
      class="ui-card"
      aria-labelledby={titled ? id : undefined}
      aria-label={titled ? undefined : label}
    >
      {titled ? (
        <div class="ui-card-head">
          <h2 id={id} class="ui-card-title">
            {title}
          </h2>
          {meta !== undefined ? <span class="ui-card-meta">{meta}</span> : null}
          {actions ? <div class="ui-card-actions">{actions}</div> : null}
        </div>
      ) : null}
      <div class="ui-card-body">{children}</div>
    </section>
  );
}
