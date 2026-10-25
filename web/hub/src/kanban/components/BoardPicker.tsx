import { fuzzy } from "@huddle/ui/fuzzy.ts";
import type { JSX } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type { BoardRef } from "../io.ts";

export type BoardPickerProps = {
  /** every board the tab lists */
  boards: BoardRef[];
  /** the open board's key, or none; a key the list does not know keeps itself on the button */
  boardKey: string | null;
  /** hands the picked board's key up, as the address change that reopens the tab */
  onBoard: (key: string) => void;
};

/** The root's last two path segments, the picker's quiet disambiguator; the board's own
 *  `.agents/kanban` tail carries no name, so it is dropped first. */
export function rootTail(root: string): string {
  const parts = root.split("/").filter((part) => part !== "");
  const n = parts.length;
  if (parts[n - 1] === "kanban" && parts[n - 2] === ".agents") parts.length = n - 2;
  return parts.slice(-2).join("/") || root;
}

const POPOVER_ID = "kanban-board-picker-pop";
const LIST_ID = "kanban-board-picker";

/**
 * The board picker: the page title as a button naming the open board, opening a popover that
 * lists every board by name with the tail of its root beside it. Typing filters with the fuzzy
 * matcher, the arrows walk, Enter picks, Escape closes, and b opens it from anywhere on the tab
 * except inside an input.
 */
export function BoardPicker({ boards, boardKey, onBoard }: BoardPickerProps): JSX.Element {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [at, setAt] = useState(0);
  const button = useRef<HTMLButtonElement>(null);
  const box = useRef<HTMLInputElement>(null);

  const current = boards.find((board) => board.key === boardKey) ?? null;
  const items =
    query.trim() === ""
      ? boards
      : boards
          .map((board) => [fuzzy(query.trim(), `${board.name} ${board.root}`), board] as const)
          .filter((pair) => pair[0] >= 0)
          .sort((a, b) => b[0] - a[0])
          .map((pair) => pair[1]);

  const close = (toButton: boolean): void => {
    setOpen(false);
    setQuery("");
    if (toButton) button.current?.focus();
  };

  const pick = (board: BoardRef | undefined): void => {
    if (board === undefined) return;
    close(false);
    onBoard(board.key);
  };

  // a click outside closes without a pick; the timeout keeps the opening click out
  useEffect(() => {
    if (!open) return;
    const outside = (event: Event): void => {
      if (!document.getElementById(POPOVER_ID)?.contains(event.target as Node)) close(false);
    };
    const timer = setTimeout(() => document.addEventListener("click", outside, true), 0);
    return () => {
      clearTimeout(timer);
      document.removeEventListener("click", outside, true);
    };
  }, [open]);

  // opening hands the focus to the filter box, however the popover was opened
  useEffect(() => {
    if (open) box.current?.focus();
  }, [open]);

  // b opens the picker from anywhere on the tab except inside an input
  useEffect(() => {
    const onKey = (event: KeyboardEvent): void => {
      const typing =
        event.target instanceof HTMLElement &&
        event.target.matches("input, textarea, select, [contenteditable]");
      if (event.key !== "b" || typing || event.metaKey || event.ctrlKey || event.altKey) return;
      event.preventDefault();
      setAt(
        Math.max(
          0,
          boards.findIndex((board) => board.key === boardKey),
        ),
      );
      setQuery("");
      setOpen(true);
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [boards, boardKey]);

  const openList = (): void => {
    setAt(
      Math.max(
        0,
        boards.findIndex((board) => board.key === boardKey),
      ),
    );
    setQuery("");
    setOpen(true);
  };

  const onKeyDown = (event: KeyboardEvent): void => {
    if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      close(true);
      return;
    }
    if (event.key === "ArrowDown") {
      event.preventDefault();
      setAt(Math.min(at + 1, items.length - 1));
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      setAt(Math.max(at - 1, 0));
    } else if (event.key === "Home") {
      event.preventDefault();
      setAt(0);
    } else if (event.key === "End") {
      event.preventDefault();
      setAt(Math.max(0, items.length - 1));
    } else if (event.key === "Enter") {
      event.preventDefault();
      pick(items[at]);
    }
  };

  return (
    <span class="picker">
      <button
        ref={button}
        type="button"
        class="picker-button"
        aria-label="Board"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={open ? LIST_ID : undefined}
        onClick={() => (open ? close(true) : openList())}
      >
        {current?.name ?? boardKey ?? "Board"}
        <span class="chev" aria-hidden="true">
          ▾
        </span>
      </button>
      {open && (
        <span class="picker-pop" id={POPOVER_ID}>
          <input
            ref={box}
            type="text"
            class="ui-input"
            role="combobox"
            aria-label="Filter boards"
            aria-autocomplete="list"
            aria-controls={LIST_ID}
            aria-expanded="true"
            placeholder="Filter boards…"
            value={query}
            onInput={(event) => {
              setQuery(event.currentTarget.value);
              setAt(0);
            }}
            onKeyDown={onKeyDown}
          />
          <span class="picker-list" id={LIST_ID} role="listbox" aria-label="Boards">
            {items.length === 0 && <span class="picker-none">No board matches.</span>}
            {items.map((board, i) => (
              <button
                key={board.key}
                type="button"
                role="option"
                class={i === at ? "picker-opt active" : "picker-opt"}
                aria-selected={i === at}
                aria-label={`${board.name} ${rootTail(board.root)}`}
                onMouseDown={(event) => {
                  event.preventDefault();
                  pick(board);
                }}
              >
                <strong>{board.name}</strong>
                <span class="root">{rootTail(board.root)}</span>
              </button>
            ))}
          </span>
        </span>
      )}
    </span>
  );
}
