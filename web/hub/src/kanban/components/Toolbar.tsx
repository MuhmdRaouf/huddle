import { useEffect, useRef, useState } from "preact/hooks";
import type { Density } from "../store.ts";
import { type FilterKind, type Filters, clearPicks, hiddenDone, togglePick, useMedia } from "../store.ts";
import type { Card } from "../types.ts";
import { BoardPicker } from "./BoardPicker.tsx";
import { FilterMenu } from "./FilterMenu.tsx";
import type { BoardRef } from "../io.ts";

export type ToolbarProps = {
  cards: Card[];
  filters: Filters;
  onChange: (filters: Filters) => void;
  /** the boards the picker lists; the picker stands only when the boards are given (the
   *  standalone page names its one board and carries no picker) */
  boards?: BoardRef[];
  /** the open board's key, or none before one is adopted */
  boardKey?: string | null;
  /** hands a picker choice up, as the address change that reopens the tab */
  onBoard?: (key: string) => void;
  /** the board's epoch, named by the live dot */
  epoch: number;
  /** whether the stream is up, read by the live dot's title */
  connected: boolean;
  /** live boards carry the dot; a static board has nothing to be live about */
  live: boolean;
  /** when the stream's last frame came (epoch ms), read by the status line beside the dot */
  lastFrameAt?: number | null;
  /** the board's density and its toggle */
  density: Density;
  onDensity: (density: Density) => void;
  /** tags every visible card carries, named once here instead of on every card */
  sharedTags?: readonly string[] | undefined;
  /** the one cluster every visible card shares, named once here */
  sharedCluster?: string | null | undefined;
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

/** How long the stream may be down before the status line says so; a shorter blip stays quiet. */
const DOWN_MS = 10_000;

/** The status line beside the live dot: the last frame's age, ticking each second, or
 *  Reconnecting once the stream has been down for DOWN_MS. */
function StaleLine({ connected, lastFrameAt }: { connected: boolean; lastFrameAt: number | null }) {
  const [now, setNow] = useState(() => Date.now());
  const [downSince, setDownSince] = useState<number | null>(() => (connected ? null : Date.now()));
  useEffect(() => {
    const tick = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(tick);
  }, []);
  useEffect(() => {
    setDownSince(connected ? null : Date.now());
  }, [connected]);
  let text = "";
  if (downSince !== null && now - downSince >= DOWN_MS) text = "Reconnecting";
  else if (lastFrameAt !== null)
    text = `Updated ${Math.max(0, Math.floor((now - lastFrameAt) / 1000))} s ago`;
  return (
    <span class="stale-line" role="status">
      {text}
    </span>
  );
}

/** One `All cards: …` note, for a shared tag or the shared cluster. */
function SharedNote({ value }: { value: string }) {
  return <span class="shared-note">All cards: {value}</span>;
}

/** The filter controls: one menu per kind, the shared notes, the hide-done toggle and the
 *  density toggle. The desktop shows them in the header row, the phone in its folded row. */
function FilterControls({
  cards,
  filters,
  onChange,
  openKind,
  onOpenKind,
  density,
  onDensity,
  sharedTags,
  sharedCluster,
}: {
  cards: Card[];
  filters: Filters;
  onChange: (filters: Filters) => void;
  openKind: FilterKind | null;
  onOpenKind: (kind: FilterKind | null) => void;
  density: Density;
  onDensity: (density: Density) => void;
  sharedTags: readonly string[];
  sharedCluster: string | null;
}) {
  const kinds = KINDS.filter(({ kind }) => kind !== "tag" || cards.some((card) => card.tags.length > 0));
  const hidden = hiddenDone(cards, filters);
  return (
    <>
      {kinds.map((entry) => (
        <KindMenu
          key={entry.kind}
          kind={entry.kind}
          label={entry.label}
          cards={cards}
          filters={filters}
          open={openKind === entry.kind}
          onOpen={() => onOpenKind(entry.kind)}
          onClose={() => onOpenKind(null)}
          onChange={onChange}
        />
      ))}
      {sharedTags.map((tag) => (
        <SharedNote key={tag} value={tag} />
      ))}
      {sharedCluster !== null && sharedCluster !== "" && <SharedNote value={sharedCluster} />}
      <label class="hide-done">
        <input
          type="checkbox"
          class="toggle"
          checked={filters.hideDone}
          aria-checked={filters.hideDone ? "true" : "false"}
          onChange={() => onChange({ ...filters, hideDone: !filters.hideDone })}
        />
        <span>{hidden > 0 ? `Hide done (${hidden})` : "Hide done"}</span>
      </label>
      <button
        type="button"
        class="density"
        aria-pressed={density === "comfortable"}
        onClick={() => onDensity(density === "compact" ? "comfortable" : "compact")}
      >
        {density === "compact" ? "Compact" : "Comfortable"}
      </button>
    </>
  );
}

/** The id of the phone's folded filters row, named by the Filters button's aria-controls. */
const FILTERS_ID = "kanban-filters";

/** The header row: the picker as the page title, the live dot, search, one menu per filter kind,
 *  the shared note, the hide-done toggle and the density toggle at the right. `/` jumps to the
 *  search box and b opens the picker, but never while a menu holds the keyboard. The picks sit
 *  as pills below. On a phone the menus, the toggles and the pills fold behind one Filters
 *  button that counts the picks and Hide done, so the header stays one row high. */
export function Toolbar({
  cards,
  filters,
  onChange,
  boards,
  boardKey,
  onBoard,
  epoch,
  connected,
  live,
  lastFrameAt = null,
  density,
  onDensity,
  sharedTags = [],
  sharedCluster = null,
}: ToolbarProps) {
  const search = useRef<HTMLInputElement>(null);
  const [openKind, setOpenKind] = useState<FilterKind | null>(null);
  const narrow = useMedia("(max-width: 639px)");
  const [unfolded, setUnfolded] = useState(false);

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

  const picks = KINDS.flatMap(({ kind, label }) =>
    filters.picks[kind].map((value) => ({ kind, label, value })),
  );
  const liveWord = connected ? "live" : "reconnecting";
  const active = picks.length + (filters.hideDone ? 1 : 0);
  const showFilters = !narrow || unfolded;

  const controls = (
    <FilterControls
      cards={cards}
      filters={filters}
      onChange={onChange}
      openKind={openKind}
      onOpenKind={setOpenKind}
      density={density}
      onDensity={onDensity}
      sharedTags={sharedTags}
      sharedCluster={sharedCluster}
    />
  );

  return (
    <>
      <div class="toolbar">
        {boards !== undefined && boards.length > 0 && onBoard !== undefined && (
          <BoardPicker boards={boards} boardKey={boardKey ?? null} onBoard={onBoard} />
        )}
        {live && (
          <span class={`live-dot ${connected ? "on" : "off"}`} title={`epoch ${epoch}, ${liveWord}`}>
            <span class="sr-only">{liveWord}</span>
          </span>
        )}
        {live && <StaleLine connected={connected} lastFrameAt={lastFrameAt} />}
        <input
          ref={search}
          type="search"
          aria-label="Search"
          placeholder="Search (press /)"
          value={filters.q}
          onInput={(event) => onChange({ ...filters, q: event.currentTarget.value })}
        />
        {narrow && (
          <button
            type="button"
            class="filters-btn"
            aria-expanded={unfolded}
            aria-controls={unfolded ? FILTERS_ID : undefined}
            onClick={() => setUnfolded(!unfolded)}
          >
            Filters{active > 0 ? `: ${active}` : ""}
          </button>
        )}
        {!narrow && controls}
        {narrow && unfolded && (
          <div class="filters-row" id={FILTERS_ID}>
            {controls}
          </div>
        )}
      </div>
      {showFilters && picks.length > 0 && (
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
