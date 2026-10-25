/**
 * The wire shapes: the snapshot, its cards and the deltas the server pushes, exactly as the API sends
 * them. The body is never in the snapshot; it is fetched per ticket and cached by updatedAt.
 */

export const COLUMN_IDS = [
  "backlog",
  "ready",
  "in-progress",
  "review",
  "testing",
  "blocked",
  "done",
] as const;

export type ColumnId = (typeof COLUMN_IDS)[number];

export type ColumnInfo = { id: ColumnId; title: string; count: number };

export type StopLine = { epoch: number; actor: string; note: string; ts: string };

/** One badge as the server derives it (SPEC §4.4 order); `kind` picks the colour, `label` is the text. */
export type BadgeSpec = { kind: string; label: string };

/** A dependency: `state` is the dependency's column, or "missing", or "text" for plain-text entries. */
export type Dep = { id: string; state: string };

export type Blocked = { reason: string; on: string[]; until: string; from: string };

export type Lint = { failures: string[]; warnings: string[] };

export type Card = {
  id: string;
  title: string;
  column: ColumnId;
  summary: string;
  type: string;
  priority: string;
  est: string;
  cluster: string;
  tags: string[];
  expedite: boolean;
  holder: string;
  author: string;
  deps: Dep[];
  blocked: Blocked | null;
  badges: BadgeSpec[];
  lint: Lint;
  movedByHand: string;
  branch: string;
  worktree: string;
  updatedAt: string;
  /** when the ticket reached done, empty unless done; done orders by it, falling back to updatedAt */
  finishedAt: string;
};

export type Snapshot = {
  rev: number;
  board: string;
  project: string;
  epoch: number;
  stopLine: StopLine | null;
  columns: ColumnInfo[];
  cards: Card[];
  generatedAt: string;
};

export type DeltaMeta = { epoch: number; stopLine: StopLine | null; columns: ColumnInfo[] };

export type Delta = {
  rev: number;
  upsert: Card[];
  remove: string[];
  meta?: DeltaMeta;
};

export type FieldValue = string | string[] | boolean | null;

export type Ticket = {
  card: Card;
  fields: Record<string, FieldValue>;
  body: string;
  path: string;
};

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
