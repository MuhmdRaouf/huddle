import type { BoardState } from "../store.ts";

/**
 * The board's masthead: project, per-column counts, epoch, the live dot (absent in static mode) and
 * the stop-line banner while the line is stopped.
 */
export function Header({ state }: { state: BoardState }) {
  const snap = state.snapshot;
  return (
    <header class="header">
      <div class="head-row">
        <h1>{snap?.project ?? "Kanban"}</h1>
        {snap !== null && <p class="epoch">epoch {snap.epoch}</p>}
        {state.mode === "live" && (
          <span
            class={`dot ${state.connected ? "on" : "off"}`}
            title={state.connected ? "live" : "reconnecting"}
          >
            <span class="sr-only">{state.connected ? "live" : "reconnecting"}</span>
          </span>
        )}
      </div>
      {snap !== null && (
        <p class="counts">{snap.columns.map((col) => `${col.title} ${col.count}`).join(" · ")}</p>
      )}
      {snap?.stopLine != null && (
        <p role="alert" class="stopline">
          Line stopped by {snap.stopLine.actor}: {snap.stopLine.note}
        </p>
      )}
    </header>
  );
}
