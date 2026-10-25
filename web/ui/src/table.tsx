// table.tsx — a data table in its own scroll region, so a wide table scrolls inside itself and never
// widens the page. Columns name their cells; numbers align to the end in the mono face.

import type { ComponentChildren, JSX } from "preact";

/** One column: its key, its header, how a row fills it, and how it aligns. */
export type Column<T> = {
  key: string;
  label: string;
  cell: (row: T) => ComponentChildren;
  align?: "start" | "end" | undefined;
  /** Commands, ids and numbers read in the mono face. */
  mono?: boolean | undefined;
};

/** What a table shows: its name, columns, rows and the line it shows when there are none. */
export type TableProps<T> = {
  label: string;
  columns: readonly Column<T>[];
  rows: readonly T[];
  rowKey: (row: T) => string;
  empty?: ComponentChildren;
};

/** The cell classes a column asks for. */
const cellClass = <T,>(col: Column<T>): string =>
  [col.align === "end" ? "ui-end" : "", col.mono ? "ui-mono" : ""].filter(Boolean).join(" ");

/** The table, captioned by its label inside a focusable region a keyboard can scroll. */
export function Table<T>({
  label,
  columns,
  rows,
  rowKey,
  empty = "Nothing here yet",
}: TableProps<T>): JSX.Element {
  return (
    // the region scrolls on its own, so a keyboard must be able to reach it
    // oxlint-disable-next-line jsx-a11y/no-noninteractive-tabindex
    <div class="ui-table-wrap" role="region" aria-label={label} tabIndex={0}>
      <table class="ui-table">
        <caption class="ui-sr">{label}</caption>
        <thead>
          <tr>
            {columns.map((col) => (
              <th key={col.key} scope="col" class={col.align === "end" ? "ui-end" : ""}>
                {col.label}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.length === 0 ? (
            <tr>
              <td colSpan={columns.length} class="ui-table-empty">
                {empty}
              </td>
            </tr>
          ) : (
            rows.map((row) => (
              <tr key={rowKey(row)}>
                {columns.map((col) => (
                  <td key={col.key} class={cellClass(col)}>
                    {col.cell(row)}
                  </td>
                ))}
              </tr>
            ))
          )}
        </tbody>
      </table>
    </div>
  );
}
