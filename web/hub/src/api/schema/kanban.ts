// kanban.ts — the kanban wire: one schema per payload the board's Go side marshals — the board
// list (kanbantab's boards answer), the snapshot, the delta and the ticket body (watch and the
// board server), the feature action's answer (kanbantab's flowAnswer and errorAnswer) — and the
// backlog view a board stores in localStorage. A field is optional only where the Go struct's
// JSON tag says omitempty.
import * as v from "valibot";

/** The seven columns, in the flow order the board lays its lanes out in. */
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

const ColumnIdSchema = v.picklist(COLUMN_IDS);

/** One board huddle knows: its key (the API's address for it), the repo root it lives under. */
export const BoardRefSchema = v.object({
  key: v.string(),
  root: v.string(),
  name: v.string(),
});
export type BoardRef = v.InferOutput<typeof BoardRefSchema>;

/** The boards answer: the list as /api/kanban/boards sends it, never null. */
export const BoardsAnswerSchema = v.object({ boards: v.array(BoardRefSchema) });

const StopLineSchema = v.object({
  epoch: v.number(),
  actor: v.string(),
  note: v.string(),
  ts: v.string(),
});
export type StopLine = v.InferOutput<typeof StopLineSchema>;

/** One badge as the server derives it (SPEC §4.4 order); `kind` picks the colour, `label` is the text. */
const BadgeSpecSchema = v.object({ kind: v.string(), label: v.string() });
export type BadgeSpec = v.InferOutput<typeof BadgeSpecSchema>;

/** A dependency: `state` is the dependency's column, or "missing", or "text" for plain-text entries. */
const DepSchema = v.object({ id: v.string(), state: v.string() });
export type Dep = v.InferOutput<typeof DepSchema>;

const BlockedSchema = v.object({
  reason: v.string(),
  on: v.array(v.string()),
  until: v.string(),
  from: v.string(),
});
export type Blocked = v.InferOutput<typeof BlockedSchema>;

const LintSchema = v.object({ failures: v.array(v.string()), warnings: v.array(v.string()) });
export type Lint = v.InferOutput<typeof LintSchema>;

const CardSchema = v.object({
  id: v.string(),
  title: v.string(),
  column: ColumnIdSchema,
  summary: v.string(),
  type: v.string(),
  priority: v.string(),
  est: v.string(),
  cluster: v.string(),
  tags: v.array(v.string()),
  expedite: v.boolean(),
  holder: v.string(),
  author: v.string(),
  deps: v.array(DepSchema),
  blocked: v.nullable(BlockedSchema),
  // Badges() answers nil for a done card whose sparse front matter carries only warnings, and a nil
  // slice marshals as null; the page reads none.
  badges: v.pipe(
    v.nullable(v.array(BadgeSpecSchema)),
    v.transform((badges) => badges ?? []),
  ),
  lint: LintSchema,
  movedByHand: v.string(),
  branch: v.string(),
  worktree: v.string(),
  updatedAt: v.string(),
  // when the ticket reached done, empty unless done; done orders by it, falling back to updatedAt
  finishedAt: v.string(),
  // true when the card's feature is closed: the card is off the board until the feature reopens
  feature_closed: v.boolean(),
});
export type Card = v.InferOutput<typeof CardSchema>;

/** A feature the board has closed: its cards are gone from the board until it reopens. */
const ClosedFeatureSchema = v.object({
  name: v.string(),
  // when the feature was closed, empty when unknown
  closedAt: v.string(),
  // how many cards the feature holds, closed
  cards: v.number(),
});
export type ClosedFeature = v.InferOutput<typeof ClosedFeatureSchema>;

/** The closed features a snapshot or a delta meta carries. */
const ClosedSchema = v.object({ closed: v.array(ClosedFeatureSchema) });
export type Closed = v.InferOutput<typeof ClosedSchema>;

const ColumnInfoSchema = v.object({ id: ColumnIdSchema, title: v.string(), count: v.number() });
export type ColumnInfo = v.InferOutput<typeof ColumnInfoSchema>;

export const SnapshotSchema = v.object({
  rev: v.number(),
  board: v.string(),
  project: v.string(),
  epoch: v.number(),
  stopLine: v.nullable(StopLineSchema),
  // the features the board has closed
  features: ClosedSchema,
  columns: v.array(ColumnInfoSchema),
  cards: v.array(CardSchema),
  generatedAt: v.string(),
});
export type Snapshot = v.InferOutput<typeof SnapshotSchema>;

const DeltaMetaSchema = v.object({
  epoch: v.number(),
  stopLine: v.nullable(StopLineSchema),
  features: ClosedSchema,
  columns: v.array(ColumnInfoSchema),
});
export type DeltaMeta = v.InferOutput<typeof DeltaMetaSchema>;

export const DeltaSchema = v.object({
  rev: v.number(),
  upsert: v.array(CardSchema),
  remove: v.array(v.string()),
  meta: v.optional(DeltaMetaSchema),
});
export type Delta = v.InferOutput<typeof DeltaSchema>;

// Fields marshals as map[string]string on the Go side; the record keeps the drawer's FieldValue
// domain, the type its row rendering is written against.
const FieldValueSchema = v.union([v.string(), v.array(v.string()), v.boolean(), v.null()]);

export const TicketSchema = v.object({
  card: CardSchema,
  fields: v.record(v.string(), FieldValueSchema),
  body: v.string(),
  path: v.string(),
});
export type Ticket = v.InferOutput<typeof TicketSchema>;

/** The answer a close or reopen gives: the server's lines, or its refusal with the error. */
export const FeatureAnswerSchema = v.object({
  lines: v.optional(v.array(v.string())),
  error: v.optional(v.string()),
});

/** The phone's backlog grouping a board stores in localStorage, as readGroups keeps it. */
export const BacklogViewSchema = v.object({
  grouping: v.picklist(["priority", "cluster"]),
  open: v.array(v.string()),
});
export type BacklogView = v.InferOutput<typeof BacklogViewSchema>;
