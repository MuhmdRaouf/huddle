// KanbanTab.tsx — the Kanban tab: a board picker over /api/kanban/boards and, for the chosen
// board, the kanban page's toolbar, board and drawer on a store of its own. The address owns the
// choice (#/kanban/<key>): a bare #/kanban adopts the first board's, a key the list does not know
// reads as a gone board. A key change stops the old store's stream before the new one starts, and
// an unmount stops it for good. The board list asks again on its own after a failure, and again
// whenever the page turns visible; the board's stream is replaced when the page turns visible after
// more than 30 s away, since a phone may resume it on a socket that never delivers again.

import { Button } from "@huddle/ui/button.tsx";
import type { JSX } from "preact";
import { useCallback, useEffect, useMemo, useRef, useState } from "preact/hooks";
import "./kanban.css";
import { Board } from "./components/Board.tsx";
import { BoardPicker } from "./components/BoardPicker.tsx";
import { Drawer, useDrawerSelection } from "./components/Drawer.tsx";
import { Toolbar } from "./components/Toolbar.tsx";
import { resumeOnReturn } from "../shared/watchdog.ts";
import { boardIo, type BoardRef, fetchBoards } from "./io.ts";
import {
  backoffMs,
  BoardStore,
  clearFilters,
  defaultView,
  DONE_HIDE_THRESHOLD,
  EMPTY_FILTERS,
  type Filters,
  filterCards,
  type Io,
  staticBoardState,
  togglePick,
  useBacklogView,
  useDensity,
} from "./store.ts";

const EMPTY_LINE = "No boards yet. A session working in a repo with .agents/kanban adds its board here.";
const GONE_LINE = "That board is gone. Pick another.";
const FAILED_LINE = "The board list is unreachable.";

/** The board list's read: loading until it answers, failed when it throws or runs past its deadline. */
type BoardsState = { kind: "loading" } | { kind: "failed" } | { kind: "ok"; boards: BoardRef[] };

/** How long one board-list request may run before it counts as failed. */
const LIST_DEADLINE_MS = 10_000;

/** How long the page may stay hidden before its return replaces the board's stream. */
const AWAY_MS = 30_000;

/**
 * The board list's loader. Each load aborts the one before it and gets its own deadline. Until a
 * list arrives, a failure reports failed and asks again on the backoff ladder; once one has
 * arrived, a failed refetch keeps it. `load` asks now, from the ladder's foot.
 */
function boardListLoader(report: (state: BoardsState) => void): { load: () => void; stop: () => void } {
  let attempt = 0;
  let listed = false;
  let current: AbortController | null = null;
  let deadline: ReturnType<typeof setTimeout> | undefined;
  let rung: ReturnType<typeof setTimeout> | undefined;

  const settle = (controller: AbortController): boolean => {
    if (current !== controller) return false;
    current = null;
    clearTimeout(deadline);
    return true;
  };
  const ask = (): void => {
    current?.abort();
    clearTimeout(deadline);
    clearTimeout(rung);
    const controller = new AbortController();
    current = controller;
    const failed = (): void => {
      if (!settle(controller)) return;
      controller.abort();
      if (listed) return;
      report({ kind: "failed" });
      rung = setTimeout(ask, backoffMs(attempt));
      attempt += 1;
    };
    deadline = setTimeout(failed, LIST_DEADLINE_MS);
    fetchBoards(controller.signal).then((boards) => {
      if (!settle(controller)) return;
      listed = true;
      attempt = 0;
      report({ kind: "ok", boards });
    }, failed);
  };
  return {
    load: () => {
      attempt = 0;
      ask();
    },
    stop: () => {
      const controller = current;
      current = null;
      controller?.abort();
      clearTimeout(deadline);
      clearTimeout(rung);
    },
  };
}

/** The board list, loaded on mount, again whenever the page turns visible, and on `retry`. */
function useBoards(): { state: BoardsState; retry: () => void } {
  const [state, setState] = useState<BoardsState>({ kind: "loading" });
  const loader = useRef<ReturnType<typeof boardListLoader> | null>(null);
  useEffect(() => {
    const own = boardListLoader(setState);
    loader.current = own;
    // a phone resuming the page may hold a request that never answers; a fresh ask replaces it
    const onVisibility = (): void => {
      if (document.visibilityState === "visible") own.load();
    };
    document.addEventListener("visibilitychange", onVisibility);
    own.load();
    return () => {
      document.removeEventListener("visibilitychange", onVisibility);
      own.stop();
      loader.current = null;
    };
  }, []);
  const retry = useCallback((): void => loader.current?.load(), []);
  return { state, retry };
}

/** The one board's store: built over the io its caller hands in, seeded static when the page
 *  carries an export payload, started once, resumed on a return after AWAY_MS hidden, stopped on
 *  an io change or an unmount. */
function useBoardStore(io: Io): BoardStore | null {
  const [, setTick] = useState(0);
  const [store, setStore] = useState<BoardStore | null>(null);
  useEffect(() => {
    const next = new BoardStore(io, staticBoardState(window));
    setStore(next);
    const release = next.subscribe(() => setTick((tick) => tick + 1));
    const releaseReturn = resumeOnReturn(
      document,
      AWAY_MS,
      () => Date.now(),
      () => void next.resume(),
    );
    void next.start();
    return () => {
      release();
      releaseReturn();
      next.stop();
    };
  }, [io]);
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

/** The chosen board's page: the toolbar, the board and the drawer on the io's store. The picker
 *  rides along only when the caller hands one in — the standalone page has none. */
export function BoardPane({
  io,
  picker,
}: {
  io: Io;
  picker?: { boards: BoardRef[]; boardKey: string; onBoard: (key: string) => void };
}): JSX.Element {
  const store = useBoardStore(io);
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
  const [view, setView] = useBacklogView(picker?.boardKey ?? snapshot?.board ?? "");
  const cards = useMemo(() => snapshot?.cards ?? [], [snapshot]);
  const visible = useMemo(() => filterCards(cards, filters), [cards, filters]);
  const shared = useMemo(() => sharedValues(visible), [visible]);
  if (store === null) {
    return <Board snapshot={null} filters={filters} changeSeq={0} changedIds={new Set()} onOpen={sel.open} />;
  }
  const state = store.getState();
  const backlogGroups = {
    grouping: view.grouping,
    open: new Set(view.open),
    onToggle: (key: string): void =>
      setView({
        ...view,
        open: view.open.includes(key) ? view.open.filter((open) => open !== key) : [...view.open, key],
      }),
  };
  const openCard = sel.openId === null ? null : (cards.find((card) => card.id === sel.openId) ?? null);
  return (
    <>
      <Toolbar
        cards={cards}
        filters={filters}
        onChange={setFilters}
        {...picker}
        epoch={snapshot?.epoch ?? 0}
        connected={state.connected}
        live={state.mode === "live"}
        lastFrameAt={state.lastFrameAt}
        density={density}
        onDensity={setDensity}
        sharedTags={shared.tags}
        sharedCluster={shared.cluster}
        grouping={view.grouping}
        onGrouping={(grouping) => setView(defaultView(grouping))}
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
          onClearFilters={() => setFilters(clearFilters())}
          onShowDone={() => setFilters({ ...filters, hideDone: false })}
          sharedTags={shared.tags}
          sharedCluster={shared.cluster}
          backlogGroups={backlogGroups}
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
  const { state, retry } = useBoards();
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

  // the io changes only with the board, so the pane's store follows the address alone
  const io = useMemo(() => (effective === null ? null : boardIo(effective)), [effective]);

  return (
    <div class="kanban-tab">
      {state.kind === "loading" && (
        <div class="kanban-loading" role="status" aria-label="Loading the boards">
          <div class="kanban-skeleton toolbar-skeleton" aria-hidden="true" />
          <div class="kanban-skeleton board-skeleton" aria-hidden="true" />
        </div>
      )}
      {state.kind === "failed" && (
        <>
          <p role="status">{FAILED_LINE}</p>
          <Button variant="secondary" class="retry" onClick={retry}>
            Retry
          </Button>
        </>
      )}
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
      {effective !== null && io !== null && (
        <BoardPane io={io} picker={{ boards, boardKey: effective, onBoard: toAddress }} />
      )}
    </div>
  );
}
