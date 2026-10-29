// health.ts — what /health says about the hub's parts, decoded at the edge so a malformed field
// falls back instead of throwing, and the tiny store the shell's chrome and the Settings page
// read it through: the update watch's fetch and Settings' own reads feed setHealth, useHealth
// repaints the readers.

import { useEffect, useState } from "preact/hooks";

/** One part's state, as the server computes it: failing while the last error is the latest,
 *  stale when a loop missed its beats, unknown until its first report. */
export type HealthState = "ok" | "failing" | "stale" | "unknown";

/** One component of the hub process: a store's open or a loop's beat, with its last success,
 *  its last error and how many errors it has counted. Epoch ms are 0 while unknown. */
export interface HealthPart {
  name: string;
  kind: "store" | "loop";
  state: HealthState;
  lastOkAt: number;
  lastErrAt: number;
  lastError: string;
  errors: number;
}

/** The slice of /health the UI reads: whether any part fails, and every part of this process. */
export interface HealthView {
  healthy: boolean;
  parts: HealthPart[];
}

/** The states the server names; anything else decodes as unknown. */
const STATES: readonly string[] = ["ok", "failing", "stale"];

/** A number field as the wire holds it: a real number stands, anything else reads 0. */
function num(v: unknown): number {
  return typeof v === "number" && Number.isFinite(v) ? v : 0;
}

function partOf(raw: unknown): HealthPart {
  const p = (raw ?? {}) as Record<string, unknown>;
  return {
    name: typeof p.name === "string" ? p.name : "",
    kind: p.kind === "loop" ? "loop" : "store",
    state: STATES.includes(p.state as string) ? (p.state as HealthState) : "unknown",
    lastOkAt: num(p.lastOkAt),
    lastErrAt: num(p.lastErrAt),
    lastError: typeof p.lastError === "string" ? p.lastError : "",
    errors: num(p.errors),
  };
}

/** Decodes /health's answer at the edge: a missing or malformed field falls back (an absent
 *  `healthy` reads true, an unreadable part list reads empty), and it never throws. */
export function decodeHealth(raw: unknown): HealthView {
  const h = (raw ?? {}) as { healthy?: unknown; components?: unknown };
  return {
    healthy: typeof h.healthy === "boolean" ? h.healthy : true,
    parts: Array.isArray(h.components) ? h.components.map(partOf) : [],
  };
}

/** The parts a reader must hear about: the failing and the stale ones, in the answer's order. */
export function failingParts(v: HealthView): HealthPart[] {
  return v.parts.filter((p) => p.state === "failing" || p.state === "stale");
}

/** The components' reader-facing names; a store or loop the table does not know stays itself. */
const LABELS: Readonly<Record<string, string>> = {
  "hub.db": "Hub registry",
  "knowledge.db": "Shared knowledge",
  "router.db": "Request bodies",
  "history.db": "Radar history",
  "ledger.db": "Radar ledger",
  "radar.history-prune": "History cleanup",
  "radar.ledger-prune": "Ledger cleanup",
  "radar.history-writer": "History writer",
};

/** The reader's name for a component: a channel's or a board's part names what it carries. */
export function partLabel(name: string): string {
  if (name.startsWith("channel:")) return `Channel ${name.slice("channel:".length)}`;
  if (name.startsWith("kanban:")) return `Board ${name.slice("kanban:".length)}`;
  return LABELS[name] ?? name;
}

/** The last answer, healthy until one lands, so no dot shows before the first read. */
let view: HealthView = { healthy: true, parts: [] };

const readers = new Set<(v: HealthView) => void>();

/** Feeds the store the latest answer; every useHealth reader repaints. */
export function setHealth(v: HealthView): void {
  view = v;
  for (const read of readers) read(v);
}

/** The latest answer, repainting on every setHealth. */
export function useHealth(): HealthView {
  const [current, setCurrent] = useState(view);
  useEffect(() => {
    setCurrent(view);
    readers.add(setCurrent);
    return () => {
      readers.delete(setCurrent);
    };
  }, []);
  return current;
}
