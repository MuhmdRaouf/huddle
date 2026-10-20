// KanbanTab.tsx — the Kanban tab: a board picker over /api/kanban/boards and, for the chosen
// board, the kanban page's toolbar, board and drawer on a store of its own. The address owns the
// choice (#/kanban/<key>): a bare #/kanban adopts the first board's, a key the list does not know
// reads as a gone board. A key change stops the old store's stream before the new one starts, and
// an unmount stops it for good.

import type { JSX } from "preact";
import { useCallback, useEffect, useMemo, useRef, useState } from "preact/hooks";
import "./kanban.css";
import { Board } from "./components/Board.tsx";
import { BoardPicker } from "./components/BoardPicker.tsx";
import { Drawer, useDrawerSelection } from "./components/Drawer.tsx";
import { Toolbar } from "./components/Toolbar.tsx";
import { boardIo, type BoardRef, fetchBoards } from "./io.ts";
import {
  BoardStore,
  clearFilters,
  DONE_HIDE_THRESHOLD,
  EMPTY_FILTERS,
  type Filters,
  filterCards,
  togglePick,
  useDensity,
} from "./store.ts";

const EMPTY_LINE = "No boards yet. A session working in a repo with .agents/kanban adds its board here.";
const GONE_LINE = "That board is gone. Pick another.";
const FAILED_LINE = "The board list is unreachable.";

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

/** The values every visible card carries, so the cards can drop them for one header note. */
function sharedValues(visible: ReturnType<typeof filterCards>): {
  tags: string[];
  cluster: string | null;
} {
  if (visible.length < 2) return { tags: [], cluster: null };
  const counts = new Map<string, number>();
  for (const card of visible) {
    for (const tag of card.tags) counts.set(tag, (counts.get(tag) ?? 0) + 1);
  }
  const tags = [...counts.entries()].filter(([, n]) => n === visible.length).map(([tag]) => tag);
  const cluster = visible.every((card) => card.cluster === visible[0]?.cluster)
    ? (visible[0]?.cluster ?? null)
    : null;
  return { tags, cluster };
}

/** The chosen board's page: the toolbar, the board and the drawer on the key's store. */
function BoardPane({
  boardKey,
  boards,
  onBoard,
}: {
  boardKey: string;
  boards: BoardRef[];
  onBoard: (key: string) => void;
}): JSX.Element {
  const store = useBoardStore(boardKey);
  const sel = useDrawerSelection();
  const [filters, setFiltersState] = useState<Filters>(EMPTY_FILTERS);
  const [density, setDensity] = useDensity();
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
  const cards = useMemo(() => snapshot?.cards ?? [], [snapshot]);
  const visible = useMemo(() => filterCards(cards, filters), [cards, filters]);
  const shared = useMemo(() => sharedValues(visible), [visible]);
  if (store === null) {
    return <Board snapshot={null} filters={filters} changeSeq={0} changedIds={new Set()} onOpen={sel.open} />;
  }
  const state = store.getState();
  const openCard = sel.openId === null ? null : (cards.find((card) => card.id === sel.openId) ?? null);
  return (
    <>
      <Toolbar
        cards={cards}
        filters={filters}
        onChange={setFilters}
        boards={boards}
        boardKey={boardKey}
        onBoard={onBoard}
        epoch={snapshot?.epoch ?? 0}
        connected={state.connected}
        live={state.mode === "live"}
        density={density}
        onDensity={setDensity}
        sharedTags={shared.tags}
        sharedCluster={shared.cluster}
      />
      <div class="board-with-panel">
        <Board
          snapshot={snapshot}
          filters={filters}
          changeSeq={state.changeSeq}
          changedIds={state.changedIds}
          onOpen={sel.open}
          onTag={pickTag}
          openId={sel.openId}
          density={density}
          onClearFilters={() => setFilters(clearFilters(filters))}
          sharedTags={shared.tags}
          sharedCluster={shared.cluster}
        />
        {openCard !== null && (
          <Drawer
            card={openCard}
            cards={cards}
            getTicket={(id) => store.ticket(id)}
            onClose={sel.close}
            onOpenDep={sel.openDep}
          />
        )}
      </div>
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

  const toAddress = (key: string): void => {
    location.hash = `#/kanban/${key}`;
  };

  return (
    <div class="kanban-tab">
      {state.kind === "loading" && (
        <div class="loading" role="status" aria-label="Loading the boards">
          <div class="skeleton toolbar-skeleton" aria-hidden="true" />
          <div class="skeleton board-skeleton" aria-hidden="true" />
        </div>
      )}
      {state.kind === "failed" && <p role="status">{FAILED_LINE}</p>}
      {state.kind === "ok" && boards.length === 0 && <p role="status">{EMPTY_LINE}</p>}
      {gone && (
        <>
          {/* a gone key leaves the picker standing, so another board is one pick away */}
          <div class="toolbar">
            <BoardPicker boards={boards} boardKey={boardKey} onBoard={toAddress} />
          </div>
          <p role="status">{GONE_LINE}</p>
        </>
      )}
      {effective !== null && <BoardPane boardKey={effective} boards={boards} onBoard={toAddress} />}
    </div>
  );
}
