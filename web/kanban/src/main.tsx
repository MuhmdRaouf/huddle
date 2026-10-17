/**
 * The browser entry: one mutable ui (filters, open ticket, opener element) painted on every change,
 * a live store behind it in live mode, the export payload on the window in static mode. The URL owns
 * the filters (query) and the open ticket (hash), so a board state can be shared as a link.
 */

import "./styles.css";
import { render } from "preact";
import { browserIo } from "./api.ts";
import { Board } from "./components/Board.tsx";
import { Drawer, idFromHash } from "./components/Drawer.tsx";
import { Header } from "./components/Header.tsx";
import { Toolbar } from "./components/Toolbar.tsx";
import {
  BoardStore,
  DONE_HIDE_THRESHOLD,
  type Filters,
  filtersFromSearch,
  searchFromFilters,
  staticBoardState,
  togglePick,
} from "./store.ts";
import type { Snapshot, Ticket } from "./types.ts";

const queried = document.querySelector<HTMLElement>("#root");
if (queried === null) throw new Error("kanban: #root missing");
const root: HTMLElement = queried;
root.replaceChildren();

const staticBoard = window.__KANBAN__;
const staticState = staticBoardState(window);
const store = new BoardStore(browserIo);

const ui = {
  filters: filtersFromSearch(location.search),
  openId: idFromHash(location.hash) as string | null,
  opener: null as HTMLElement | null,
};

function getTicket(id: string): Promise<Ticket> {
  if (staticBoard === undefined) return store.ticket(id);
  const ticket = staticBoard.bodies[id];
  return ticket === undefined
    ? Promise.reject(new Error(`no exported body for ${id}`))
    : Promise.resolve(ticket);
}

function openTicket(id: string, opener: HTMLElement | null): void {
  ui.openId = id;
  ui.opener = opener;
  history.replaceState(null, "", `#/t/${id}`);
  paint();
}

function closeTicket(): void {
  const opener = ui.opener;
  ui.openId = null;
  ui.opener = null;
  history.replaceState(null, "", location.pathname + location.search);
  paint();
  if (opener !== null && document.contains(opener)) opener.focus();
}

function setFilters(filters: Filters): void {
  ui.filters = filters;
  filtersTouched = true;
  const state = staticState ?? store.getState();
  const doneCount = state.snapshot?.cards.filter((card) => card.column === "done").length ?? 0;
  const query = searchFromFilters(filters, doneCount);
  history.replaceState(null, "", query === "" ? location.pathname : `?${query}`);
  paint();
}

/** A tag chip on a card lands here: the tag joins (or leaves) the Domain filter. */
function pickTag(tag: string): void {
  setFilters(togglePick(ui.filters, "tag", tag));
}

let filtersTouched = false;
let doneDecided = false;

/** Once, on the first board: a URL that says nothing about done hides it on a done-heavy board.
 *  A reader who touched the filters, or a URL that named done, has already decided. */
function decideDoneDefault(snapshot: Snapshot): void {
  if (doneDecided) return;
  doneDecided = true;
  const heavy = snapshot.cards.filter((card) => card.column === "done").length > DONE_HIDE_THRESHOLD;
  if (heavy && !filtersTouched && !new URLSearchParams(location.search).has("done")) {
    ui.filters = { ...ui.filters, hideDone: true };
  }
}

function paint(): void {
  const state = staticState ?? store.getState();
  const snapshot = state.snapshot;
  if (snapshot !== null) decideDoneDefault(snapshot);
  const openCard = ui.openId === null ? null : (snapshot?.cards.find((c) => c.id === ui.openId) ?? null);
  render(
    <>
      <Header state={state} />
      <Toolbar cards={snapshot?.cards ?? []} filters={ui.filters} onChange={setFilters} />
      <Board
        snapshot={snapshot}
        filters={ui.filters}
        changeSeq={state.changeSeq}
        changedIds={state.changedIds}
        onOpen={openTicket}
        onTag={pickTag}
      />
      {openCard !== null && (
        <Drawer
          card={openCard}
          getTicket={getTicket}
          onClose={closeTicket}
          onOpenDep={(id) => openTicket(id, null)}
        />
      )}
    </>,
    root,
  );
}

window.addEventListener("hashchange", () => {
  const id = idFromHash(location.hash);
  if (id === null) {
    if (ui.openId !== null) closeTicket();
    return;
  }
  if (id !== ui.openId) {
    ui.openId = id;
    paint();
  }
});

paint();
if (staticState === null) {
  store.subscribe(paint);
  void store.start();
}
