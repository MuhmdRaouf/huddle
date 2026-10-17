import { useEffect, useRef, useState } from "preact/hooks";
import { type FilterKind, type Filters, clearPicks, togglePick } from "../store.ts";
import type { Card } from "../types.ts";
import { FilterMenu } from "./FilterMenu.tsx";

export type ToolbarProps = {
  cards: Card[];
  filters: Filters;
  onChange: (filters: Filters) => void;
};

/** The row's kinds in their standing order: Domain is the tag kind, Epic the cluster. */
const KINDS: readonly { kind: FilterKind; label: string }[] = [
  { kind: "tag", label: "Domain" },
  { kind: "cluster", label: "Epic" },
  { kind: "holder", label: "Holder" },
  { kind: "type", label: "Type" },
  { kind: "priority", label: "Priority" },
];

/** One kind's menu button and, when open, its menu. */
function KindMenu({
  kind,
  label,
  cards,
  filters,
  open,
  onOpen,
  onClose,
  onChange,
}: {
  kind: FilterKind;
  label: string;
  cards: Card[];
  filters: Filters;
  open: boolean;
  onOpen: () => void;
  onClose: () => void;
  onChange: (filters: Filters) => void;
}) {
  const button = useRef<HTMLButtonElement>(null);
  const count = filters.picks[kind].length;
  return (
    <span class="menu-wrap">
      <button
        ref={button}
        type="button"
        class="menu-btn"
        data-kind={kind}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => (open ? onClose() : onOpen())}
      >
        {label}
        {count > 0 ? `: ${count}` : ""}
      </button>
      {open && (
        <FilterMenu
          kind={kind}
          label={label}
          cards={cards}
          filters={filters}
          buttonRef={button}
          onToggle={(value) => onChange(togglePick(filters, kind, value))}
          onClose={onClose}
        />
      )}
    </span>
  );
}

/** Search, one menu per filter kind, the hide-done toggle, and the picks as pills below.
 *  `/` jumps to the search box, but never while a menu holds the keyboard. */
export function Toolbar({ cards, filters, onChange }: ToolbarProps) {
  const search = useRef<HTMLInputElement>(null);
  const [openKind, setOpenKind] = useState<FilterKind | null>(null);

  useEffect(() => {
    const onKey = (event: KeyboardEvent): void => {
      const typing = event.target instanceof HTMLElement && event.target.matches("input, textarea, select");
      if (event.key !== "/" || typing || openKind !== null || event.metaKey || event.ctrlKey || event.altKey)
        return;
      event.preventDefault();
      search.current?.focus();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [openKind]);

  const kinds = KINDS.filter(({ kind }) => kind !== "tag" || cards.some((card) => card.tags.length > 0));
  const picks = KINDS.flatMap(({ kind, label }) =>
    filters.picks[kind].map((value) => ({ kind, label, value })),
  );

  return (
    <>
      <div class="toolbar">
        <input
          ref={search}
          type="search"
          aria-label="Search"
          placeholder="Search (press /)"
          value={filters.q}
          onInput={(event) => onChange({ ...filters, q: event.currentTarget.value })}
        />
        {kinds.map((entry) => (
          <KindMenu
            key={entry.kind}
            kind={entry.kind}
            label={entry.label}
            cards={cards}
            filters={filters}
            open={openKind === entry.kind}
            onOpen={() => setOpenKind(entry.kind)}
            onClose={() => setOpenKind(null)}
            onChange={onChange}
          />
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
      {picks.length > 0 && (
        <div class="pills">
          {picks.map((pick) => (
            <button
              key={`${pick.kind}:${pick.value}`}
              type="button"
              class="pill"
              aria-label={`Remove ${pick.label} ${pick.value}`}
              onClick={() => onChange(togglePick(filters, pick.kind, pick.value))}
            >
              {pick.label}: {pick.value} <span aria-hidden="true">×</span>
            </button>
          ))}
          <button type="button" class="pill clear" onClick={() => onChange(clearPicks(filters))}>
            Clear all
          </button>
        </div>
      )}
    </>
  );
}
