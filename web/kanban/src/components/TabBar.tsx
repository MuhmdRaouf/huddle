import type { ColumnId, ColumnInfo } from "../types.ts";

export type TabBarProps = {
  columns: ColumnInfo[];
  active: ColumnId;
  onPick: (id: ColumnId) => void;
};

/** The phone layout's column picker: one tab per column, its count beside its name. */
export function TabBar({ columns, active, onPick }: TabBarProps) {
  return (
    <div class="tabs" role="tablist" aria-label="Columns">
      {columns.map((col) => (
        <button
          key={col.id}
          type="button"
          role="tab"
          class={col.id === active ? "tab active" : "tab"}
          aria-selected={col.id === active}
          onClick={() => onPick(col.id)}
        >
          {col.title} <span class="count">{col.count}</span>
        </button>
      ))}
    </div>
  );
}
