// list.tsx — a list of rows on a surface: each row a title with a muted line under it, an optional
// leading mark and trailing part. A row with onClick is one button, a full-width tap target.

import type { ComponentChildren, JSX } from "preact";

/** A named list of ListItem rows. */
export function List({ label, children }: { label: string; children: ComponentChildren }): JSX.Element {
  return (
    <ul class="ui-list" aria-label={label}>
      {children}
    </ul>
  );
}

/** What one row shows, and what a tap on it does. */
export type ListItemProps = {
  title: ComponentChildren;
  meta?: ComponentChildren;
  leading?: ComponentChildren;
  trailing?: ComponentChildren;
  onClick?: (() => void) | undefined;
};

/** One row: a button when it can be tapped, plain otherwise. */
export function ListItem({ title, meta, leading, trailing, onClick }: ListItemProps): JSX.Element {
  const parts = (
    <>
      {leading !== undefined ? <span class="ui-list-leading">{leading}</span> : null}
      <span class="ui-list-text">
        <span class="ui-list-title">{title}</span>
        {meta !== undefined ? <span class="ui-list-meta">{meta}</span> : null}
      </span>
      {trailing !== undefined ? <span class="ui-list-trailing">{trailing}</span> : null}
    </>
  );
  return (
    <li class="ui-list-item">
      {onClick ? (
        <button type="button" class="ui-list-row" onClick={onClick}>
          {parts}
        </button>
      ) : (
        <div class="ui-list-row">{parts}</div>
      )}
    </li>
  );
}
