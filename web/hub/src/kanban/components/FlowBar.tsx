import type { ColumnId, ColumnInfo } from "../types.ts";

export type FlowBarProps = {
  /** Every column of the board in flow order, done included; it has a label but no segment */
  columns: ColumnInfo[];
  /** hands the column a label stands for up: the board scrolls to it, or switches on a phone */
  onPick: (id: ColumnId) => void;
  /** on a phone the labels are the column tabs: the active one carries aria-selected */
  activeId?: ColumnId | undefined;
};

/** The columns the bar draws segments for: every one but done that holds cards. */
const SEGMENTED = (columns: ColumnInfo[]): ColumnInfo[] =>
  columns.filter((col) => col.id !== "done" && col.count > 0);

/**
 * The flow bar: one 6 px segment per non-done column with cards, each as wide as its share of
 * the board, and the counts as plain labels under it. On a phone the labels are the column
 * tabs — the one place the two rows meet.
 */
export function FlowBar({ columns, onPick, activeId }: FlowBarProps) {
  const tabMode = activeId !== undefined;
  const label = (col: ColumnInfo): string => `${col.title} ${col.count}`;
  return (
    <div class="flow">
      <div class="flow-bar" role="img" aria-label={columns.map(label).join(", ")}>
        {SEGMENTED(columns).map((col) => (
          <span key={col.id} class="flow-seg" data-column={col.id} style={{ flexGrow: col.count }} />
        ))}
      </div>
      <div class={tabMode ? "flow-labels tabs" : "flow-labels"}>
        {columns.map((col) =>
          tabMode ? (
            <button
              key={col.id}
              type="button"
              role="tab"
              class={col.id === activeId ? "flow-label active" : "flow-label"}
              aria-selected={col.id === activeId}
              onClick={() => onPick(col.id)}
            >
              {label(col)}
            </button>
          ) : (
            <button
              key={col.id}
              type="button"
              class="flow-label"
              data-column={col.id}
              onClick={() => onPick(col.id)}
            >
              {label(col)}
              {col.id === "done" && (
                <span class="more" aria-hidden="true">
                  ▸
                </span>
              )}
            </button>
          ),
        )}
      </div>
    </div>
  );
}
