import { useEffect, useRef } from "preact/hooks";
import { FILTER_KINDS, type FilterKind, type Filters } from "../store.ts";
import type { Card } from "../types.ts";

export type ToolbarProps = {
  cards: Card[];
  filters: Filters;
  onChange: (filters: Filters) => void;
};

/** The values a card carries for one filter kind: what the chips of that kind offer. */
const valuesOfKind: Record<FilterKind, (card: Card) => string[]> = {
  cluster: (card) => [card.cluster],
  tag: (card) => card.tags,
  holder: (card) => (card.holder === "" ? [] : [card.holder]),
  type: (card) => [card.type],
  priority: (card) => [card.priority],
};

function optionsFor(cards: Card[], kind: FilterKind): string[] {
  const valuesOf = valuesOfKind[kind];
  if (valuesOf === undefined) return [];
  const values = new Set<string>();
  for (const card of cards) {
    for (const value of valuesOf(card)) values.add(value);
  }
  return [...values].sort();
}

/** Search, one chip row per filter kind, and the hide-done toggle. `/` jumps to the search box. */
export function Toolbar({ cards, filters, onChange }: ToolbarProps) {
  const search = useRef<HTMLInputElement>(null);
  useEffect(() => {
    const onKey = (event: KeyboardEvent): void => {
      const typing = event.target instanceof HTMLElement && event.target.matches("input, textarea, select");
      if (event.key !== "/" || typing || event.metaKey || event.ctrlKey || event.altKey) return;
      event.preventDefault();
      search.current?.focus();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, []);

  const toggle = (kind: FilterKind, value: string): void => {
    const picked = filters.picks[kind];
    const next = picked.includes(value) ? picked.filter((v) => v !== value) : [...picked, value];
    onChange({ ...filters, picks: { ...filters.picks, [kind]: next } });
  };

  return (
    <div class="toolbar">
      <input
        ref={search}
        type="search"
        aria-label="Search"
        placeholder="Search (press /)"
        value={filters.q}
        onInput={(event) => onChange({ ...filters, q: event.currentTarget.value })}
      />
      {FILTER_KINDS.map((kind) => (
        <div class="chips" data-kind={kind} key={kind}>
          {optionsFor(cards, kind).map((value) => {
            const picked = filters.picks[kind].includes(value);
            return (
              <button
                key={value}
                type="button"
                class={picked ? "chip picked" : "chip"}
                aria-pressed={picked}
                onClick={() => toggle(kind, value)}
              >
                {value}
              </button>
            );
          })}
        </div>
      ))}
      <button
        type="button"
        class="toggle"
        aria-pressed={filters.hideDone}
        onClick={() => onChange({ ...filters, hideDone: !filters.hideDone })}
      >
        Hide done
      </button>
    </div>
  );
}
