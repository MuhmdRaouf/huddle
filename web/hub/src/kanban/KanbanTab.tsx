// KanbanTab.tsx — the Kanban tab: a board picker over /api/kanban/boards and, for the chosen
// board, the kanban page's toolbar, board and drawer on a store of its own. The address owns the
// choice (#/kanban/<key>): a bare #/kanban adopts the first board's, a key the list does not know
// reads as a gone board. A key change stops the old store's stream before the new one starts, and
// an unmount stops it for good.

import type { JSX } from "preact";
import { useCallback, useEffect, useRef, useState } from "preact/hooks";
import "./kanban.css";
import { Board } from "./components/Board.tsx";
import { Drawer, useDrawerSelection } from "./components/Drawer.tsx";
import { Toolbar } from "./components/Toolbar.tsx";
import { type BoardRef, boardIo, fetchBoards } from "./io.ts";
import { BoardStore, DONE_HIDE_THRESHOLD, EMPTY_FILTERS, type Filters, togglePick } from "./store.ts";

const LOADING_LINE = "Loading the boards…";
const EMPTY_LINE = "No boards yet. A session working in a repo with .agents/kanban adds its board here.";
const GONE_LINE = "That board is gone. Pick another.";
const FAILED_LINE = "The board list is unreachable.";

/** The picker's label: the repo's name, disambiguated with its root once the name repeats. */
export function optionLabel(boards: BoardRef[], board: BoardRef): string {
  return boards.some((other) => other !== board && other.name === board.name)
    ? `${board.name} (${board.root})`
    : board.name;
}

/** The board list's read: loading until it answers, failed when it throws. */
type BoardsState = { kind: "loading" } | { kind: "failed" } | { kind: "ok"; boards: BoardRef[] };

function useBoards(): BoardsState {
  const [state, setState] = useState<BoardsState>({ kind: "loading" });
  useEffect(() => {
    let live = true;
    fetchBoards().then(
      (boards) => {
        if (live) setState({ kind: "ok", boards });
      },
      () => {
        if (live) setState({ kind: "failed" });
      },
    );
    return () => {
      live = false;
    };
  }, []);
  return state;
}

/** The one board's store: built and started per key, stopped on a key change or an unmount. */
function useBoardStore(key: string): BoardStore | null {
  const [, setTick] = useState(0);
  const [store, setStore] = useState<BoardStore | null>(null);
  useEffect(() => {
    const next = new BoardStore(boardIo(key));
    setStore(next);
    const release = next.subscribe(() => setTick((tick) => tick + 1));
    void next.start();
    return () => {
      release();
      next.stop();
    };
  }, [key]);
  return store;
}

/** The chosen board's page: the toolbar, the board and the drawer on the key's store. */
function BoardPane({ boardKey }: { boardKey: string }): JSX.Element {
  const store = useBoardStore(boardKey);
  const sel = useDrawerSelection();
  const [filters, setFiltersState] = useState<Filters>(EMPTY_FILTERS);
  const touched = useRef(false);
  const setFilters = (next: Filters): void => {
    touched.current = true;
    setFiltersState(next);
  };
  const pickTag = useCallback((tag: string): void => {
    touched.current = true;
    setFiltersState((current) => togglePick(current, "tag", tag));
  }, []);
  const snapshot = store?.getState().snapshot ?? null;
  // once, on the first board: a done-heavy board hides its done cards until the reader asks
  useEffect(() => {
    if (snapshot === null || touched.current) return;
    if (snapshot.cards.filter((card) => card.column === "done").length > DONE_HIDE_THRESHOLD) {
      setFiltersState((current) => ({ ...current, hideDone: true }));
    }
  }, [snapshot]);
  if (store === null) {
    return <Board snapshot={null} filters={filters} changeSeq={0} changedIds={new Set()} onOpen={sel.open} />;
  }
  const state = store.getState();
  const cards = snapshot?.cards ?? [];
  const openCard = sel.openId === null ? null : (cards.find((card) => card.id === sel.openId) ?? null);
  return (
    <>
      <Toolbar cards={cards} filters={filters} onChange={setFilters} />
      <Board
        snapshot={snapshot}
        filters={filters}
        changeSeq={state.changeSeq}
        changedIds={state.changedIds}
        onOpen={sel.open}
        onTag={pickTag}
      />
      {openCard !== null && (
        <Drawer
          card={openCard}
          getTicket={(id) => store.ticket(id)}
          onClose={sel.close}
          onOpenDep={sel.openDep}
        />
      )}
    </>
  );
}

/** The tab: the board picker, its status lines, and the chosen board's own page. */
export function KanbanTab({ boardKey }: { boardKey: string | null }): JSX.Element {
  const state = useBoards();
  const boards = state.kind === "ok" ? state.boards : [];
  const first = boards[0] ?? null;
  const known = boardKey !== null && boards.some((board) => board.key === boardKey);
  const gone = boardKey !== null && !known;
  const effective = known ? boardKey : boardKey === null ? (first?.key ?? null) : null;

  // a bare #/kanban adopts the first board's address; the hashchange repaints with the key in
  useEffect(() => {
    if (boardKey === null && first !== null) location.hash = `#/kanban/${first.key}`;
  }, [boardKey, first]);

  return (
    <div class="kanban-tab">
      {boards.length > 0 && (
        <select
          aria-label="Board"
          value={effective ?? ""}
          onChange={(event) => {
            location.hash = `#/kanban/${event.currentTarget.value}`;
          }}
        >
          {boards.map((board) => (
            <option key={board.key} value={board.key}>
              {optionLabel(boards, board)}
            </option>
          ))}
        </select>
      )}
      {state.kind === "loading" && <p role="status">{LOADING_LINE}</p>}
      {state.kind === "failed" && <p role="status">{FAILED_LINE}</p>}
      {state.kind === "ok" && boards.length === 0 && <p role="status">{EMPTY_LINE}</p>}
      {gone && <p role="status">{GONE_LINE}</p>}
      {effective !== null && <BoardPane boardKey={effective} />}
    </div>
  );
}
