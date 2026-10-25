import type { Card, ColumnInfo, Snapshot, Ticket } from "../../src/kanban/types.ts";
import snapshotJson from "./fixtures/snapshot.json" with { type: "json" };
import ticketApiJson from "./fixtures/ticket-KBN-API.json" with { type: "json" };
import ticketUiJson from "./fixtures/ticket-KBN-UI.json" with { type: "json" };

export const snapshot = snapshotJson as unknown as Snapshot;
export const ticketUi = ticketUiJson as unknown as Ticket;
export const ticketApi = ticketApiJson as unknown as Ticket;

/** A fresh copy, so one test's mutation cannot leak into the next. */
export function clone<T>(value: T): T {
  return structuredClone(value);
}

/** The fixture's card with that id, or a throw: tests name cards that must exist. */
export function cardOf(id: string): Card {
  const card = snapshot.cards.find((c) => c.id === id);
  if (card === undefined) throw new Error(`no fixture card ${id}`);
  return card;
}

/** A card with sane defaults, overridable field by field. */
export function makeCard(over: Partial<Card> = {}): Card {
  return { ...clone(cardOf("KBN-API")), id: "T-1", ...over };
}

/** Column titles and counts derived from the cards, in flow order. */
export function countColumns(cards: Card[]): ColumnInfo[] {
  return snapshot.columns.map((col) => ({
    id: col.id,
    title: col.title,
    count: cards.filter((c) => c.column === col.id).length,
  }));
}

/** A snapshot holding exactly these cards, counts derived to match. */
export function makeSnapshot(cards: Card[], over: Partial<Snapshot> = {}): Snapshot {
  return { ...clone(snapshot), cards, columns: countColumns(cards), ...over };
}

/** The daisyUI classes the page never wears: buttons, toggles, tabs, glass, badges, skeletons and the
 *  theme colours. A ui-* class from the design system never counts. */
const LEGACY_CLASS =
  /^(btn(-[a-z]+)?|toggle(-[a-z]+)?|tabs?(-[a-z]+)?|glass|badge(-[a-z]+)?|skeleton|(bg|text|border)-(base-\d+|base-content|primary|secondary|accent|neutral|info|success|warning|error)(-content)?)$/;

/** Every daisyUI class worn under `root`, root included, in document order. */
export function legacyClasses(root: Element): string[] {
  return [root, ...root.querySelectorAll("[class]")]
    .flatMap((el) => [...el.classList])
    .filter((name) => !name.startsWith("ui-") && LEGACY_CLASS.test(name));
}
