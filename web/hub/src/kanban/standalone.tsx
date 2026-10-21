// standalone.tsx — the board page's own entry: the hub's Kanban pane over the io the page picks.
// An export payload on the window reads the board from the page itself
// (staticIo: nothing fetched, no stream, no reconnecting); a page without one — the board
// server's own — follows the board's live API at the root (ioAt("")). One kanban-tab pane,
// no board picker: the page names its one board.

import "./standalone.css";
import "./kanban.css";
import "./panel.css";
import { render } from "preact";
import { BoardPane } from "./KanbanTab.tsx";
import { ioAt, staticIo } from "./io.ts";

const queried = document.querySelector<HTMLElement>("#root");
if (queried === null) throw new Error("kanban: #root missing");
const root: HTMLElement = queried;
root.replaceChildren();

const io = window.__KANBAN__ !== undefined ? staticIo(window.__KANBAN__) : ioAt("");

render(
  <div class="kanban-tab">
    <BoardPane io={io} />
  </div>,
  root,
);
