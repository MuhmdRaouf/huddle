import { useEffect, useRef, useState } from "preact/hooks";
import { type BoardRef, optionLabel } from "../io.ts";
import { type FilterKind, type Filters, clearPicks, togglePick } from "../store.ts";
import type { Card } from "../types.ts";
import { FilterMenu } from "./FilterMenu.tsx";

export type ToolbarProps = {
  cards: Card[];
  filters: Filters;
  onChange: (filters: Filters) => void;
  /** the boards the picker lists, empty while the list is on its way */
  boards: BoardRef[];
  /** the open board's key, or none before one is adopted */
  boardKey: string | null;
  /** hands a picker choice up, as the address change that reopens the tab */
  onBoard: (key: string) => void;
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

/** The toolbar as one wrapping flex row: the board picker, search, one menu per filter kind, the
 *  hide-done toggle with its label beside it — and the picks as pills below. `/` jumps to the
 *  search box, but never while a menu holds the keyboard. */
export function Toolbar({ cards, filters, onChange, boards, boardKey, onBoard }: ToolbarProps) {
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
        {boards.length > 0 && (
          <select
            class="board-select"
            aria-label="Board"
            value={boardKey ?? ""}
            onChange={(event) => onBoard(event.currentTarget.value)}
          >
            {boards.map((board) => (
              <option key={board.key} value={board.key}>
                {optionLabel(boards, board)}
              </option>
            ))}
          </select>
        )}
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
        <label class="hide-done">
          <input
            type="checkbox"
            class="toggle"
            checked={filters.hideDone}
            aria-checked={filters.hideDone ? "true" : "false"}
            onChange={() => onChange({ ...filters, hideDone: !filters.hideDone })}
          />
          <span>Hide done</span>
        </label>
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
