import { Button } from "@huddle/ui/button.tsx";
import { useEffect, useRef, useState } from "preact/hooks";
import type { Density, Grouping } from "../store.ts";
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
  /** the board's density and its button */
  density: Density;
  onDensity: (density: Density) => void;
  /** tags every visible card carries, named once here instead of on every card */
  sharedTags?: readonly string[] | undefined;
  /** the one cluster every visible card shares, named once here */
  sharedCluster?: string | null | undefined;
  /** the phone backlog's grouping and its toggle, shown in the phone's folded row only */
  grouping?: Grouping | undefined;
  onGrouping?: ((grouping: Grouping) => void) | undefined;
};

/** The row's kinds in their standing order: Domain is the tag kind, Feature the cluster. */
const KINDS: readonly { kind: FilterKind; label: string }[] = [
  { kind: "tag", label: "Domain" },
  { kind: "cluster", label: "Feature" },
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
        class="chip"
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
 *  Reconnecting once the stream has been down for DOWN_MS. The age sits outside any live region,
 *  so a screen reader is not told every second; the polite region speaks only Reconnecting. */
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
  const reconnecting = downSince !== null && now - downSince >= DOWN_MS;
  const age =
    reconnecting || lastFrameAt === null
      ? ""
      : `Updated ${Math.max(0, Math.floor((now - lastFrameAt) / 1000))} s ago`;
  return (
    <span class="stale-line">
      <span aria-live="off">{age}</span>
      <span role="status">{reconnecting ? "Reconnecting" : ""}</span>
    </span>
  );
}

/** The live dot, its title naming the epoch and the connection, and the status line beside it. */
function LiveDot({
  epoch,
  connected,
  lastFrameAt,
}: {
  epoch: number;
  connected: boolean;
  lastFrameAt: number | null;
}) {
  const liveWord = connected ? "live" : "reconnecting";
  return (
    <>
      <span class={`live-dot ${connected ? "on" : "off"}`} title={`epoch ${epoch}, ${liveWord}`}>
        <span class="ui-sr">{liveWord}</span>
      </span>
      <StaleLine connected={connected} lastFrameAt={lastFrameAt} />
    </>
  );
}

/** One `All cards: …` note, for a shared tag or the shared cluster. */
function SharedNote({ value }: { value: string }) {
  return <span class="shared-note">All cards: {value}</span>;
}

/** The density button names what a press does: compact cards expand, expanded ones compact. */
const DENSITY_ACTION: Record<Density, string> = { compact: "Expand cards", comfortable: "Compact cards" };

/** The grouping toggle's word for each grouping. */
const GROUPING_WORD: Record<Grouping, string> = { priority: "Priority", cluster: "Feature" };

/** The filter controls: one menu per kind, the shared notes, the hide-done switch, the density
 *  button and, when handed one, the backlog's grouping toggle. The desktop shows them in the
 *  header row, the phone in its folded row. */
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
  grouping,
  onGrouping,
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
  grouping?: Grouping | undefined;
  onGrouping?: ((grouping: Grouping) => void) | undefined;
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
      {/* the checkbox covers its whole label, so the label's tap target is the control's own */}
      <label class="hide-done">
        <input
          type="checkbox"
          checked={filters.hideDone}
          aria-checked={filters.hideDone ? "true" : "false"}
          onChange={() => onChange({ ...filters, hideDone: !filters.hideDone })}
        />
        <span class="switch" aria-hidden="true" />
        <span>{hidden > 0 ? `Hide done (${hidden})` : "Hide done"}</span>
      </label>
      <Button
        variant="secondary"
        class="density"
        onClick={() => onDensity(density === "compact" ? "comfortable" : "compact")}
      >
        {DENSITY_ACTION[density]}
      </Button>
      {grouping !== undefined && onGrouping !== undefined && (
        <Button
          variant="secondary"
          class="grouping"
          onClick={() => onGrouping(grouping === "priority" ? "cluster" : "priority")}
        >
          Group: {GROUPING_WORD[grouping]}
        </Button>
      )}
    </>
  );
}

/** One picked value, as its pill names it. */
type Pick = { kind: FilterKind; label: string; value: string };

/** The picks as removable pills, with a Clear all beside them; nothing while nothing is picked. */
function Pills({
  picks,
  filters,
  onChange,
}: {
  picks: Pick[];
  filters: Filters;
  onChange: (filters: Filters) => void;
}) {
  if (picks.length === 0) return null;
  return (
    <div class="pills">
      {picks.map((pick) => (
        <button
          key={`${pick.kind}:${pick.value}`}
          type="button"
          class="chip pill"
          aria-label={`Remove ${pick.label} ${pick.value}`}
          onClick={() => onChange(togglePick(filters, pick.kind, pick.value))}
        >
          {pick.label}: {pick.value} <span aria-hidden="true">×</span>
        </button>
      ))}
      <button type="button" class="chip pill clear" onClick={() => onChange(clearPicks(filters))}>
        Clear all
      </button>
    </div>
  );
}

/** The id of the phone's folded filters row, named by the Filters button's aria-controls. */
const FILTERS_ID = "kanban-filters";

/** The header row: the picker as the page title, the live dot, search, one menu per filter kind,
 *  the shared note, the hide-done switch and the density button at the right. `/` jumps to the
 *  search box and b opens the picker, but never while a menu holds the keyboard. The picks sit
 *  as pills below. On a phone the menus, the switch, the buttons and the pills fold behind one Filters
 *  button that counts the picks and Hide done, so the header stays one row high; the folded row
 *  also carries the backlog's grouping toggle, which the desktop's flat columns have no use for. */
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
  grouping,
  onGrouping,
}: ToolbarProps) {
  const search = useRef<HTMLInputElement>(null);
  const [openKind, setOpenKind] = useState<FilterKind | null>(null);
  const narrow = useMedia("(max-width: 639px)");
  // a touch screen has no / key to press, so its hint is the plain word
  const touch = useMedia("(pointer: coarse)");
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

  const picks: Pick[] = KINDS.flatMap(({ kind, label }) =>
    filters.picks[kind].map((value) => ({ kind, label, value })),
  );
  const active = picks.length + (filters.hideDone ? 1 : 0);
  const showFilters = !narrow || unfolded;

  const controls = (
    <FilterControls
      grouping={narrow ? grouping : undefined}
      onGrouping={onGrouping}
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
        {live && <LiveDot epoch={epoch} connected={connected} lastFrameAt={lastFrameAt} />}
        <input
          ref={search}
          type="search"
          class="ui-input"
          aria-label="Search"
          placeholder={touch ? "Search" : "Search (press /)"}
          value={filters.q}
          onInput={(event) => onChange({ ...filters, q: event.currentTarget.value })}
        />
        {narrow && (
          <button
            type="button"
            class="chip filters"
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
      {showFilters && <Pills picks={picks} filters={filters} onChange={onChange} />}
    </>
  );
}
