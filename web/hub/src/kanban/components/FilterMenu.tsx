import { type TargetedKeyboardEvent } from "preact";
import { useEffect, useLayoutEffect, useRef, useState } from "preact/hooks";
import { type Facet, type FilterKind, type Filters, facetValues, holderWorkingNow } from "../store.ts";
import type { Card } from "../types.ts";

export type FilterMenuProps = {
  kind: FilterKind;
  /** the kind's word for headings and the filter box's label */
  label: string;
  cards: Card[];
  filters: Filters;
  /** the kind button that opened the menu; Escape hands the focus back to it */
  buttonRef: { current: HTMLButtonElement | null };
  onToggle: (value: string) => void;
  onClose: () => void;
};

/** One block of the menu: a heading (folded or not) over its values. */
type Group = { name: string; fold: boolean; facets: Facet[] };

/** The holder kind splits into Working now and a folded Earlier; the rest are one list. */
function groupsOf(kind: FilterKind, cards: Card[], filters: Filters, facets: Facet[]): Group[] {
  if (kind !== "holder") return [{ name: "", fold: false, facets }];
  const working: Facet[] = [];
  const earlier: Facet[] = [];
  for (const facet of facets) {
    (holderWorkingNow(cards, filters, facet.value) ? working : earlier).push(facet);
  }
  const groups: Group[] = [];
  if (working.length > 0) groups.push({ name: "Working now", fold: false, facets: working });
  if (earlier.length > 0) groups.push({ name: "Earlier", fold: true, facets: earlier });
  return groups;
}

/** Every focusable control of the menu, in DOM order: the filter box, the folds, the values. */
const focusables = (menu: HTMLDivElement): HTMLElement[] => [
  ...menu.querySelectorAll<HTMLElement>("input, button"),
];

/** A menu of one filter kind's values: faceted counts, a filter box on long lists, checkboxes
 *  the keyboard drives. Escape and a click outside close it; Escape refocuses the kind button. */
export function FilterMenu({ kind, label, cards, filters, buttonRef, onToggle, onClose }: FilterMenuProps) {
  const menu = useRef<HTMLDivElement>(null);
  const [query, setQuery] = useState("");
  const [earlierOpen, setEarlierOpen] = useState(false);

  const facets = facetValues(cards, filters, kind);
  const needle = query.trim().toLowerCase();
  const shown = facets.filter((f) => needle === "" || f.value.toLowerCase().includes(needle));
  const groups = groupsOf(kind, cards, filters, shown);
  const picked = filters.picks[kind];

  // the checked value starts focused, else the filter box, else the first value; a layout effect,
  // so the focus lands with the commit and never a tick later
  useLayoutEffect(() => {
    const root = menu.current;
    const first =
      root?.querySelector<HTMLElement>("[aria-checked='true']") ??
      root?.querySelector<HTMLElement>("input") ??
      root?.querySelector<HTMLElement>("button");
    first?.focus();
  }, []);

  // a click anywhere outside closes without a pick; the timeout keeps the opening click out
  useEffect(() => {
    const outside = (event: Event): void => {
      if (!menu.current?.contains(event.target as Node)) onClose();
    };
    const timer = setTimeout(() => document.addEventListener("click", outside, true), 0);
    return () => {
      clearTimeout(timer);
      document.removeEventListener("click", outside, true);
    };
  }, [onClose]);

  const onKeyDown = (event: TargetedKeyboardEvent<HTMLDivElement>): void => {
    const root = menu.current;
    if (root === null) return;
    if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      onClose();
      buttonRef.current?.focus();
      return;
    }
    if (event.key === "Tab") {
      onClose();
      return;
    }
    if (!["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) return;
    event.preventDefault();
    const controls = focusables(root);
    const at = controls.indexOf(document.activeElement as HTMLElement);
    const step = event.key === "ArrowDown" ? 1 : event.key === "ArrowUp" ? -1 : 0;
    const to = event.key === "Home" ? 0 : event.key === "End" ? controls.length - 1 : at + step;
    const target = controls[((to % controls.length) + controls.length) % controls.length];
    target?.focus();
  };

  return (
    <div ref={menu} class="fmenu" role="menu" aria-label={label} onKeyDown={onKeyDown}>
      {facets.length > 8 && (
        <input
          type="search"
          aria-label={`Filter ${label} values`}
          placeholder="Filter…"
          value={query}
          onInput={(event) => setQuery(event.currentTarget.value)}
        />
      )}
      {groups.length === 0 && <p class="empty">No values</p>}
      {groups.map((group) => (
        <section key={group.name}>
          {group.name !== "" && group.fold && (
            <button
              type="button"
              class="fold"
              aria-expanded={earlierOpen}
              onClick={() => setEarlierOpen(!earlierOpen)}
            >
              {group.name} <span class="count">{group.facets.length}</span>
            </button>
          )}
          {group.name !== "" && !group.fold && <p class="group">{group.name}</p>}
          {(!group.fold || earlierOpen) &&
            group.facets.map((facet) => (
              <button
                key={facet.value}
                type="button"
                role="menuitemcheckbox"
                aria-checked={picked.includes(facet.value) ? "true" : "false"}
                class="opt"
                onClick={() => onToggle(facet.value)}
              >
                {facet.value} <span class="c">{facet.count}</span>
              </button>
            ))}
        </section>
      ))}
    </div>
  );
}
