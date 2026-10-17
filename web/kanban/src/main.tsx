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
import { BoardStore, type Filters, filtersFromSearch, searchFromFilters, staticBoardState } from "./store.ts";
import type { Ticket } from "./types.ts";

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
  const query = searchFromFilters(filters);
  history.replaceState(null, "", query === "" ? location.pathname : `?${query}`);
  paint();
}

function paint(): void {
  const state = staticState ?? store.getState();
  const snapshot = state.snapshot;
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
