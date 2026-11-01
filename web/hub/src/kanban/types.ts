/**
 * The kanban page's own vocabulary around the wire: the static payload an exported board boots
 * from, the shapes of the stream a store follows, and the field values a ticket's front matter
 * shows. The wire shapes themselves — the snapshot, its cards and the deltas the server pushes —
 * decode through the schemas in api/schema/kanban.ts and are re-exported from there.
 */

import type { Delta, Snapshot, Ticket } from "../api/schema/kanban.ts";

export { COLUMN_IDS, type ColumnId } from "../api/schema/kanban.ts";
export type {
  BadgeSpec,
  Blocked,
  Card,
  Closed,
  ClosedFeature,
  ColumnInfo,
  Delta,
  DeltaMeta,
  Dep,
  Lint,
  Snapshot,
  StopLine,
  Ticket,
} from "../api/schema/kanban.ts";

export type FieldValue = string | string[] | boolean | null;

/** The export payload: a static board reads its snapshot and its ticket bodies from window.__KANBAN__. */
export type StaticBoard = { snapshot: Snapshot; bodies: Record<string, Ticket> };

export type StreamHandlers = {
  onSnapshot: (snapshot: Snapshot) => void;
  onDelta: (delta: Delta) => void;
  onReload: () => void;
  /** the server's keepalive frame: no board change, only a sign the socket still delivers */
  onPing: () => void;
  onOpen: () => void;
  onError: () => void;
};

export type Stream = { close: () => void };

declare global {
  interface Window {
    __KANBAN__?: StaticBoard;
  }
}
