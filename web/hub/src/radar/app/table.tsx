/**
 * Pieces every table view shares, on the web/ui tokens: one table with two layouts — dense rows under a
 * pinned head on a desk (fixed widths for the columns that ask, so long cells truncate instead of pushing
 * the first columns out of view), and under 640 px a card per row whose cells say their column's name
 * (each body cell carries `data-label`; `data-cell="title"` runs across the card) — focusable clickable
 * rows, sortable column headers, the CSV export button, and time cells in the reader's chosen mode.
 */

import type { ComponentChildren, Ref } from "preact";
import { fmtAgo, fmtCount, fmtTime } from "../fmt.ts";
import type { IconName } from "../icons.ts";
import type { ClientState, Sort } from "../state.ts";
import { useApp } from "./context.ts";
import { Icon } from "./Icon.tsx";

/** The header cell every table shares: quiet, on the well, pinned while the rows scroll under it. */
export const HEAD_CELL =
  "sticky top-0 z-[1] bg-[var(--ui-surface-2)] px-3 py-2 text-xs font-semibold text-[var(--ui-muted)]";

/** The chevron that folds a tree row (and the blank of the same size a leaf keeps): a quiet square, a
 *  thumb's target on a touch screen. */
export const TREE_TOGGLE =
  "inline-grid size-8 shrink-0 place-items-center rounded-[var(--ui-radius-sm)] text-[var(--ui-muted)] hover:bg-[var(--ui-surface-2)] hover:text-[var(--ui-text)] pointer-coarse:size-[var(--ui-tap)]";

/** A header cell's alignment: numbers to the end, the rest to the start. */
const headAlign = (numeric: boolean | undefined): string =>
  numeric === true ? "num text-right" : "text-left";

/** The table in both layouts. The desk: dense rows on the line token. The phone: no head, each row a
 *  bordered card of labelled cells in two columns, led by the title cell across it (as is a colspan
 *  cell), numbers read from the start. */
const TABLE = [
  "w-full border-collapse text-sm",
  "[&_td]:border-t [&_td]:border-[var(--ui-line)] [&_td]:px-3 [&_td]:py-2 [&_td]:align-middle",
  "max-sm:block max-sm:[&_thead]:hidden",
  "max-sm:[&_tbody]:grid max-sm:[&_tbody]:gap-2 max-sm:[&_tbody]:p-3",
  "max-sm:[&_tr]:grid max-sm:[&_tr]:grid-cols-[minmax(0,1fr)_minmax(0,1fr)] max-sm:[&_tr]:gap-x-3 max-sm:[&_tr]:gap-y-2",
  "max-sm:[&_tr]:rounded-[var(--ui-radius)] max-sm:[&_tr]:border max-sm:[&_tr]:border-[var(--ui-line)] max-sm:[&_tr]:p-3",
  "max-sm:[&_td]:block max-sm:[&_td]:min-w-0 max-sm:[&_td]:border-0 max-sm:[&_td]:p-0 max-sm:[&_td]:text-left",
  "max-sm:[&_tbody_th]:col-span-2 max-sm:[&_tbody_th]:block",
  "max-sm:[&_td[data-cell=title]]:order-first max-sm:[&_td[data-cell=title]]:col-span-2 max-sm:[&_td[colspan]]:col-span-2",
  "max-sm:[&_td[data-label]]:before:block max-sm:[&_td[data-label]]:before:text-xs max-sm:[&_td[data-label]]:before:text-[var(--ui-muted)] max-sm:[&_td[data-label]]:before:content-[attr(data-label)]",
  "max-sm:[&_td[data-label][data-cell=title]]:before:hidden",
].join(" ");

export type Column<K extends string> = {
  key: K;
  label: string;
  numeric?: boolean;
  /** a Tailwind width class fixing this column (`table-fixed`); columns without one share what is left */
  width?: string;
};

/** The table in its wrap: on a desk the wrap scrolls the rows under the pinned head; on the phone the
 *  cards run with the page, so nothing scrolls sideways. Columns with a `width` are laid out fixed so the
 *  flexible ones truncate; the wrap's element goes to `wrapRef` for views that track or reset its scroll. */
export function Table({
  dataKey,
  class: cls = "",
  columns,
  wrapRef,
  children,
}: {
  /** the marker the export and tests use to find this table's wrap */
  dataKey?: string;
  class?: string;
  columns?: { key: string; width?: string }[];
  /** receives the scrollable wrap element */
  wrapRef?: Ref<HTMLDivElement>;
  children?: ComponentChildren;
}) {
  return (
    <div
      {...(wrapRef === undefined ? {} : { ref: wrapRef })}
      class="sm:max-h-[calc(100dvh-17rem)] sm:min-h-64 sm:overflow-auto"
      {...(dataKey === undefined ? {} : { "data-key": dataKey })}
    >
      <table
        data-radar-table=""
        class={[TABLE, columns === undefined ? "" : "table-fixed", cls].filter(Boolean).join(" ")}
      >
        {columns !== undefined && (
          <colgroup>
            {columns.map((column) => (
              <col key={column.key} class={column.width || undefined} />
            ))}
          </colgroup>
        )}
        {children}
      </table>
    </div>
  );
}

/**
 * One table row. Clickable rows focus (tabIndex 0), open on Enter or Space, and carry the pointer
 * affordance; the rest stay plain. The classes carry the row's state: failed (the fail tone's soft fill),
 * the open row (the accent at 10%, with the view drawing the 3px accent bar on its first cell), a row that
 * just arrived, an ended one in the muted ink. `class` carries extras a view needs on top.
 */
export function TableRow({
  onOpen,
  failed = false,
  active = false,
  fresh = false,
  dimmed = false,
  /** emitted as data-action, for rows the controller finds by delegation */
  action,
  value,
  /** emitted as aria-label */
  label,
  class: cls = "",
  children,
}: {
  /** opening the row (click, Enter, Space) */
  onOpen?: (() => void) | undefined;
  failed?: boolean;
  active?: boolean;
  fresh?: boolean;
  /** an ended row (an agent that finished): still readable, quieter than the rest */
  dimmed?: boolean;
  action?: string | undefined;
  value?: string | undefined;
  label?: string | undefined;
  class?: string;
  children?: ComponentChildren;
}) {
  const state = [
    "row",
    failed ? "bg-[var(--ui-fail-soft)]" : "",
    active ? "bg-[color-mix(in_oklab,var(--ui-accent)_10%,transparent)]" : "",
    fresh ? "row-new" : "",
    // the muted ink, not an opacity: a faded row's quieter cells would drop under AA
    dimmed ? "text-[var(--ui-muted)]" : "",
    cls,
  ]
    .filter(Boolean)
    .join(" ");
  const shared = {
    ...(action === undefined ? {} : { "data-action": action }),
    ...(value === undefined ? {} : { "data-value": value }),
    ...(label === undefined ? {} : { "aria-label": label }),
  };
  if (onOpen === undefined)
    return (
      <tr class={state} {...shared}>
        {children}
      </tr>
    );
  return (
    <tr
      class={`${state} cursor-pointer transition-colors hover:bg-[var(--ui-surface-2)] focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-[var(--ui-accent)]`}
      tabIndex={0}
      onClick={() => onOpen()}
      onKeyDown={(event) => {
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault();
          onOpen();
        }
      }}
      {...shared}
    >
      {children}
    </tr>
  );
}

function SortHeader<K extends string>({
  column,
  sort,
  action,
}: {
  column: Column<K>;
  sort: Sort<K>;
  action: string;
}) {
  const { act } = useApp();
  const on = sort.key === column.key;
  const ascending = on && sort.dir === "asc";
  const arrow: IconName = !on ? "chevronsUpDown" : ascending ? "chevronUp" : "chevronDown";
  return (
    <th
      class={`${HEAD_CELL} ${headAlign(column.numeric)}`}
      scope="col"
      aria-sort={!on ? "none" : ascending ? "ascending" : "descending"}
    >
      <button
        type="button"
        class={`sort-btn -mx-1.5 inline-flex min-h-8 items-center gap-1 rounded-[var(--ui-radius-sm)] px-1.5 hover:text-[var(--ui-text)] pointer-coarse:min-h-[var(--ui-tap)]${column.numeric === true ? " flex-row-reverse" : ""}${on ? " sort-on text-[var(--ui-text)]" : ""}`}
        data-action={action}
        data-value={column.key}
        title={`Sort by ${column.label.toLowerCase()}`}
        onClick={() => act(action, column.key)}
      >
        <span>{column.label}</span>
        <Icon name={arrow} class={`size-3 shrink-0${on ? " text-[var(--ui-accent)]" : " opacity-60"}`} />
      </button>
    </th>
  );
}

/** Sortable column headers: each label is a button; the sorted column carries aria-sort and an arrow. */
export function TableHead<K extends string>({
  columns,
  sort,
  action,
}: {
  columns: Column<K>[];
  sort: Sort<K>;
  action: string;
}) {
  return (
    <thead>
      <tr>
        {columns.map((column) => (
          <SortHeader key={column.key} column={column} sort={sort} action={action} />
        ))}
      </tr>
    </thead>
  );
}

/** Static column headers (no sort): the same quiet header row the sortable one draws. */
export function TableHeadStatic({
  columns,
}: {
  columns: { key: string; label: string; numeric?: boolean }[];
}) {
  return (
    <thead>
      <tr>
        {columns.map((column) => (
          <th key={column.key} class={`${HEAD_CELL} ${headAlign(column.numeric)}`} scope="col">
            {column.label}
          </th>
        ))}
      </tr>
    </thead>
  );
}

/** A time cell in the reader's chosen mode, with the other mode as its tooltip. */
export function timeText(state: ClientState, ts: number, now: number): { text: string; title: string } {
  const clock = fmtTime(ts);
  const ago = fmtAgo(ts, now);
  return state.timeMode === "absolute" ? { text: clock, title: ago } : { text: ago, title: clock };
}

export function ExportButton({ kind, count }: { kind: string; count: number }) {
  const { act } = useApp();
  return (
    <button
      type="button"
      class="ui-btn ui-btn-ghost"
      data-action="export"
      data-value={kind}
      title={`Download the ${fmtCount(count, "row")} shown as CSV`}
      onClick={() => act("export", kind)}
    >
      <Icon name="download" class="ui-icon" />
      <span>Export CSV</span>
    </button>
  );
}
