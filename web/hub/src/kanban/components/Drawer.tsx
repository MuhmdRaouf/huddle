import { Button } from "@huddle/ui/button.tsx";
import { Sheet } from "@huddle/ui/sheet.tsx";
import type { JSX } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import { useMedia } from "../store.ts";
import { renderMarkdown } from "../markdown.ts";
import { COLUMN_IDS, type Card, type ColumnId, type FieldValue, type Ticket } from "../types.ts";
import "../panel.css";

export type DrawerProps = {
  card: Card;
  /** the board snapshot's cards: the dep titles and the Unblocks set read off it */
  cards: Card[];
  getTicket: (id: string) => Promise<Ticket>;
  onClose: () => void;
  onOpenDep: (id: string) => void;
};

/** The ticket ids a deep link may name: the same shape the board's own ids take. */
const ID = /^#\/t\/([A-Z][A-Z0-9]*(?:-[A-Z0-9]+)*)$/;

export function idFromHash(hash: string): string | null {
  const match = ID.exec(hash);
  return match === null ? null : (match[1] ?? null);
}

export type Selection = {
  openId: string | null;
  open: (id: string, opener: HTMLElement) => void;
  close: () => void;
  /** swap the drawer to another ticket without closing it or losing the opener to return to */
  openDep: (id: string) => void;
};

export function useDrawerSelection(): Selection {
  const [openId, setOpenId] = useState<string | null>(null);
  const opener = useRef<HTMLElement | null>(null);
  return {
    openId,
    open: (id, element) => {
      opener.current = element;
      setOpenId(id);
    },
    close: () => {
      setOpenId(null);
      const element = opener.current;
      opener.current = null;
      if (element !== null && document.contains(element)) element.focus();
    },
    openDep: (id) => setOpenId(id),
  };
}

// ---------------------------------------------------------------------------
// pure helpers, each exported for the tests
// ---------------------------------------------------------------------------

const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

/** A stamp the way the reader says it: "today 15:15", "yesterday 09:02", "3 days ago", else the
 *  date. Anything that is not a date reads as written. */
export function relativeTime(iso: string, now: Date): string {
  const at = new Date(iso);
  if (Number.isNaN(at.getTime())) return iso;
  const dayOf = (d: Date): number => Date.UTC(d.getFullYear(), d.getMonth(), d.getDate()) / 86_400_000;
  const days = dayOf(now) - dayOf(at);
  const clock = `${String(at.getHours()).padStart(2, "0")}:${String(at.getMinutes()).padStart(2, "0")}`;
  if (days === 0) return `today ${clock}`;
  if (days === 1) return `yesterday ${clock}`;
  if (days >= 2 && days <= 30) return `${days} days ago`;
  return `${at.getDate()} ${MONTHS[at.getMonth()] ?? ""} ${at.getFullYear()}`;
}

export type LogSplit = { body: string; log: string; entries: number };

/** The `## Log` section travels out of the body: what stays, what it held, how many entries. */
export function splitLog(markdown: string): LogSplit {
  const lines = markdown.split("\n");
  const start = lines.findIndex((line) => /^##\s+Log\s*$/.test(line));
  if (start === -1) return { body: markdown.trim(), log: "", entries: 0 };
  let end = lines.length;
  for (let i = start + 1; i < lines.length; i += 1) {
    if (/^#{1,4}\s+/.test(lines[i] ?? "")) {
      end = i;
      break;
    }
  }
  const log = lines.slice(start + 1, end);
  const entries = log.filter((line) => /^\s*(?:[-*]|\d+\.)\s+/.test(line)).length;
  return {
    body: [...lines.slice(0, start), ...lines.slice(end)].join("\n").trim(),
    log: log.join("\n").trim(),
    entries,
  };
}

export type RowKind = "text" | "date" | "chips";

/** One field row: the value renders as a stamp, chips or plain text, with an optional suffix. */
export type Row = {
  key: string;
  label: string;
  value: FieldValue;
  kind: RowKind;
  suffix?: string;
};

/** The known fields in their order, under the labels the panel shows. */
const SHOWN_ROWS: readonly { key: string; label: string }[] = [
  { key: "cluster", label: "Feature" },
  { key: "repos", label: "Repos" },
  { key: "tags", label: "Tags" },
  { key: "created", label: "Created" },
  { key: "updated", label: "Updated" },
  { key: "branch", label: "Branch" },
  { key: "worktree", label: "Worktree" },
  { key: "blocked", label: "Blocked" },
];

/** The front-matter fields the header already carries, plus deps (Waits on). */
const HEADER_FIELDS = new Set([
  "id",
  "title",
  "summary",
  "type",
  "priority",
  "est",
  "holder",
  "column",
  "depends_on",
]);

/** The fields the shown rows consumed; they never repeat under More fields. */
const SHOWN_SOURCES = new Set([
  "cluster",
  "repos",
  "tags",
  "branch",
  "worktree",
  "blocked",
  "created_at",
  "created_by",
]);

const ISO_DATE = /^\d{4}-\d{2}-\d{2}([T\s]\d{2}:\d{2})?/;

function rowKind(key: string, value: FieldValue): RowKind {
  if (Array.isArray(value)) return "chips";
  if (typeof value === "string" && (key.endsWith("_at") || ISO_DATE.test(value))) return "date";
  return "text";
}

/** A path or URL: the only values allowed to break a line anywhere. */
function wrapAny(value: FieldValue): boolean {
  return typeof value === "string" && /^(\/|\.\/|~\/|\w+:\/+)/.test(value);
}

function createdRow(fields: Record<string, FieldValue>): Row | null {
  const at = fields.created_at;
  if (typeof at !== "string" || at === "") return null;
  const by = fields.created_by;
  return {
    key: "created",
    label: "Created",
    value: at,
    kind: "date",
    ...(typeof by === "string" && by !== "" ? { suffix: `by ${by}` } : {}),
  };
}

function blockedRow(card: Card): Row | null {
  const blocked = card.blocked;
  if (blocked === null) return null;
  const suffix = `on ${blocked.on.join(", ")} until ${blocked.until}`;
  return { key: "blocked", label: "Blocked", value: blocked.reason, kind: "text", suffix };
}

/** The ticket's fields as the panel shows them: the known ones in order, the rest under more. */
export function fieldRows(ticket: Ticket): { shown: Row[]; more: Row[] } {
  const fields = ticket.fields;
  const shown: Row[] = [];
  for (const { key, label } of SHOWN_ROWS) {
    if (key === "created") {
      const row = createdRow(fields);
      if (row !== null) shown.push(row);
      continue;
    }
    if (key === "updated") {
      shown.push({ key, label, value: ticket.card.updatedAt, kind: "date" });
      continue;
    }
    if (key === "blocked") {
      const row = blockedRow(ticket.card);
      if (row !== null) shown.push(row);
      continue;
    }
    const value = fields[key];
    if (value === null || value === undefined || value === "") continue;
    if (Array.isArray(value) && value.length === 0) continue;
    shown.push({ key, label, value, kind: rowKind(key, value) });
  }
  const more = Object.entries(fields)
    .filter(
      ([key, value]) =>
        !SHOWN_SOURCES.has(key) && !HEADER_FIELDS.has(key) && value !== null && value !== undefined,
    )
    .map(([key, value]) => ({ key, label: key, value, kind: rowKind(key, value) }));
  return { shown, more };
}

/** The cards whose deps name this one: what unblocks when it lands. */
export function unblocks(id: string, cards: Card[]): Card[] {
  return cards.filter((card) => card.deps.some((dep) => dep.id === id));
}

/** The task-list items of a rendered body become checkboxes nobody can tick. The markdown has
 *  already escaped the text, so the only `[ ]` after an `<li>` is the writer's own. */
export function taskList(html: string): string {
  return html.replace(
    /(<li>)\[( |x)\] /gi,
    (_match, li: string, mark: string) =>
      `${li}<input type="checkbox" disabled${mark.toLowerCase() === "x" ? " checked" : ""}> `,
  );
}

// ---------------------------------------------------------------------------
// the panel width, remembered
// ---------------------------------------------------------------------------

export const PANEL_WIDTH_KEY = "kanban.panelWidth";
export const PANEL_MIN_WIDTH = 360;
export const PANEL_MAX_WIDTH = 720;
export const PANEL_DEFAULT_WIDTH = 440;

type WidthStore = {
  getItem: (key: string) => string | null;
  setItem: (key: string, value: string) => void;
};

function clampWidth(value: number): number {
  if (!Number.isFinite(value)) return PANEL_DEFAULT_WIDTH;
  return Math.min(PANEL_MAX_WIDTH, Math.max(PANEL_MIN_WIDTH, Math.round(value)));
}

/** The width the panel reopens at, clamped back into range; a refused read falls back to 440. */
export function readPanelWidth(store: WidthStore | null = localStorage): number {
  try {
    const raw = store?.getItem(PANEL_WIDTH_KEY) ?? null;
    return raw === null ? PANEL_DEFAULT_WIDTH : clampWidth(Number(raw));
  } catch {
    return PANEL_DEFAULT_WIDTH;
  }
}

/** Remembers the width; a refused write (private mode) costs nothing. */
export function writePanelWidth(width: number, store: WidthStore | null = localStorage): void {
  try {
    store?.setItem(PANEL_WIDTH_KEY, String(clampWidth(width)));
  } catch {
    // the width just does not persist
  }
}

// ---------------------------------------------------------------------------
// the panel
// ---------------------------------------------------------------------------

type TicketState = { id: string; ticket: Ticket | null; failed: boolean };

/** Fetch the ticket for the open card, dropping answers that arrive after the card moved on. */
function useTicket(
  getTicket: (id: string) => Promise<Ticket>,
  id: string,
): { ticket: Ticket | null; loading: boolean; failed: boolean; retry: () => void } {
  const fetcher = useRef(getTicket);
  fetcher.current = getTicket;
  const [state, setState] = useState<TicketState>({ id, ticket: null, failed: false });
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    let live = true;
    setState({ id, ticket: null, failed: false });
    fetcher.current(id).then(
      (ticket) => {
        if (live) setState({ id, ticket, failed: false });
      },
      () => {
        if (live) setState({ id, ticket: null, failed: true });
      },
    );
    return () => {
      live = false;
    };
  }, [id, attempt]);
  const lagging = state.id !== id;
  return {
    ticket: lagging ? null : state.ticket,
    loading: lagging || (state.ticket === null && !state.failed),
    failed: !lagging && state.failed,
    retry: () => setAttempt((attempt) => attempt + 1),
  };
}

function fieldText(value: FieldValue): string {
  if (value === null) return "";
  if (Array.isArray(value)) return value.join(", ");
  if (typeof value === "boolean") return value ? "true" : "false";
  return value;
}

const COLUMN_LABELS: Record<ColumnId, string> = {
  backlog: "Backlog",
  ready: "Ready",
  "in-progress": "In progress",
  review: "Review",
  testing: "Testing",
  blocked: "Blocked",
  done: "Done",
};

/** One header chip: the column chip carries its data-column for the hue. */
function HeaderChips({ card, featureClosed = false }: { card: Card; featureClosed?: boolean }) {
  const chips: { text: string; column?: ColumnId }[] = [
    ...(featureClosed ? [{ text: "Feature closed" }] : []),
    { text: COLUMN_LABELS[card.column], column: card.column },
    { text: card.priority },
    { text: card.type },
  ];
  if (card.est !== "") chips.push({ text: card.est });
  if (card.holder !== "") chips.push({ text: `@${card.holder}` });
  return (
    <ul class="chips">
      {chips.map((chip, index) => (
        <li class="chip" data-column={chip.column} key={`${index}:${chip.text}`}>
          {chip.text}
        </li>
      ))}
    </ul>
  );
}

type DepRow = {
  id: string;
  title: string | null;
  column: ColumnId | "missing" | null;
  text: boolean;
};

/** Waits on from the card's deps, Unblocks computed off the snapshot. */
function dependencyRows(card: Card, cards: Card[]): { waits: DepRow[]; unblocks: DepRow[] } {
  const waits = card.deps.map((dep): DepRow => {
    if (dep.state === "text") return { id: dep.id, title: null, column: null, text: true };
    const column: ColumnId | "missing" = (COLUMN_IDS as readonly string[]).includes(dep.state)
      ? (dep.state as ColumnId)
      : "missing";
    return {
      id: dep.id,
      title: cards.find((c) => c.id === dep.id)?.title ?? null,
      column,
      text: false,
    };
  });
  const after = unblocks(card.id, cards).map((dep) => ({
    id: dep.id,
    title: dep.title,
    column: dep.column,
    text: false,
  }));
  return { waits, unblocks: after };
}

function ColumnChip({ column }: { column: ColumnId | "missing" }) {
  if (column === "missing") return <span class="chip muted">missing</span>;
  return (
    <span class="chip" data-column={column}>
      {COLUMN_LABELS[column]}
    </span>
  );
}

function DepGroup({
  label,
  rows,
  onOpenDep,
}: {
  label: string;
  rows: DepRow[];
  onOpenDep: (id: string) => void;
}) {
  return (
    <dl class="rows dep-group">
      {rows.map((row, index) => (
        <div class="row dep-row" key={row.id}>
          <dt>{index === 0 ? label : ""}</dt>
          <dd>
            {row.text ? (
              <span class="dep-id plain">{row.id}</span>
            ) : (
              <button type="button" class="dep-id" onClick={() => onOpenDep(row.id)}>
                {row.id}
              </button>
            )}
            {row.title !== null && <span class="dep-title">{row.title}</span>}
            {row.column !== null && <ColumnChip column={row.column} />}
          </dd>
        </div>
      ))}
    </dl>
  );
}

function fieldValue(row: Row): JSX.Element {
  if (row.kind === "date" && typeof row.value === "string") {
    return (
      <>
        <time datetime={row.value} title={row.value}>
          {relativeTime(row.value, new Date())}
        </time>
        {row.suffix === undefined ? null : <span class="dim">{` ${row.suffix}`}</span>}
      </>
    );
  }
  if (row.kind === "chips" && Array.isArray(row.value)) {
    return (
      <span class="chipset">
        {row.value.map((item) => (
          <span class="chip" key={item}>
            {item}
          </span>
        ))}
      </span>
    );
  }
  return (
    <span class={wrapAny(row.value) ? "wrap" : undefined}>
      {fieldText(row.value)}
      {row.suffix === undefined ? "" : ` ${row.suffix}`}
    </span>
  );
}

function FieldRows({ rows }: { rows: Row[] }) {
  return (
    <dl class="rows">
      {rows.map((row) => (
        <div class="row" key={row.key}>
          <dt>{row.label}</dt>
          <dd>{fieldValue(row)}</dd>
        </div>
      ))}
    </dl>
  );
}

const focusableSelector = "button, [href], input, select, textarea";

function focusablesIn(panel: HTMLElement): HTMLElement[] {
  return [...panel.querySelectorAll<HTMLElement>(focusableSelector)].filter(
    (el) => !el.hasAttribute("disabled"),
  );
}

/** Where Tab (or Shift+Tab) moves focus inside the drawer, wrapping at both ends. */
function nextOnTab(list: HTMLElement[], shift: boolean): HTMLElement | null {
  if (list.length === 0) return null;
  const index = list.indexOf(document.activeElement as HTMLElement);
  if (shift) return list[index <= 0 ? list.length - 1 : index - 1] ?? null;
  return list[index === -1 || index === list.length - 1 ? 0 : index + 1] ?? null;
}

/** The header: the id with its copy button, the title, the summary, the chips. A closed feature's
 *  card wears the badge beside its id. */
function PanelHeader({
  card,
  copied,
  onCopy,
  onClose,
  featureClosed = false,
}: {
  card: Card;
  copied: boolean;
  onCopy: () => void;
  onClose: () => void;
  featureClosed?: boolean;
}): JSX.Element {
  return (
    <header class="panel-head">
      <div class="panel-top">
        <div class="panel-id">
          <p class="cid">{card.id}</p>
          {featureClosed && <span class="chip feature-closed">Feature closed</span>}
        </div>
        <Button variant="secondary" class="copy" label={`Copy ${card.id}`} onClick={onCopy}>
          {copied ? "Copied" : <span aria-hidden="true">⧉</span>}
        </Button>
        <Button variant="secondary" class="close" onClick={onClose}>
          Close
        </Button>
      </div>
      <h2 class="panel-title" tabIndex={-1}>
        {card.title}
      </h2>
      {card.summary !== "" && <p class="panel-summary">{card.summary}</p>}
      <HeaderChips card={card} />
    </header>
  );
}

/** What the ticket answers for: the fields, the body and the collapsed log — or the skeleton
 *  while it loads, or the failure line with its way back. */
function PanelContent({
  loading,
  failed,
  retry,
  ticket,
}: {
  loading: boolean;
  failed: boolean;
  retry: () => void;
  ticket: Ticket | null;
}): JSX.Element | null {
  if (loading) {
    return (
      <div class="panel-loading" role="status">
        <span class="sr-only">Loading the ticket…</span>
        {[0, 1, 2, 3].map((row) => (
          <div
            class="pending-bar h-[0.93333rem] my-[0.66667rem] rounded-[0.4rem] bg-[var(--ui-surface-2)]"
            aria-hidden="true"
            key={row}
          />
        ))}
      </div>
    );
  }
  if (failed) {
    return (
      <div class="panel-error" role="alert">
        <p>This card could not be loaded.</p>
        <Button variant="secondary" onClick={retry}>
          Try again
        </Button>
      </div>
    );
  }
  if (ticket === null) return null;
  const { shown, more } = fieldRows(ticket);
  const split = splitLog(ticket.body);
  return (
    <>
      {shown.length > 0 && <FieldRows rows={shown} />}
      {more.length > 0 && (
        <details class="more">
          <summary>{`More fields (${more.length})`}</summary>
          <FieldRows rows={more} />
        </details>
      )}
      <div class="panel-body" dangerouslySetInnerHTML={{ __html: taskList(renderMarkdown(split.body)) }} />
      {split.entries > 0 && (
        <details class="log">
          <summary>{`Log (${split.entries} ${split.entries === 1 ? "entry" : "entries"})`}</summary>
          <div class="panel-body" dangerouslySetInnerHTML={{ __html: taskList(renderMarkdown(split.log)) }} />
        </details>
      )}
    </>
  );
}

/** The ticket panel: the card's header, its dependencies, every other field, the body as
 *  markdown and the log collapsed — docked beside the board on a wide window, a sheet over a
 *  scrim on a tablet, and on a phone the design system's sheet rising from the bottom, read only. */
export function Drawer({ card, cards, getTicket, onClose, onOpenDep }: DrawerProps) {
  const docked = useMedia("(min-width: 75rem)");
  const phone = useMedia("(max-width: 39.9375rem)");
  const [width, setWidth] = useState(readPanelWidth);
  const { ticket, loading, failed, retry } = useTicket(getTicket, card.id);
  const panel = useRef<HTMLDivElement>(null);
  const closeRef = useRef(onClose);
  closeRef.current = onClose;
  const drag = useRef<{ x: number; width: number } | null>(null);
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    const panelEl = panel.current;
    if (panelEl === null) return;
    panelEl.querySelector<HTMLElement>("h2")?.focus();
    const onKey = (event: KeyboardEvent): void => {
      if (event.key === "Escape") {
        event.preventDefault();
        closeRef.current();
        return;
      }
      if (event.key !== "Tab") return;
      const next = nextOnTab(focusablesIn(panelEl), event.shiftKey);
      if (next === null) return;
      event.preventDefault();
      next.focus();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [card.id]);

  useEffect(() => {
    if (!copied) return;
    const timer = window.setTimeout(() => setCopied(false), 1500);
    return () => window.clearTimeout(timer);
  }, [copied]);

  const applyWidth = (next: number): void => {
    const clamped = clampWidth(next);
    setWidth(clamped);
    writePanelWidth(clamped);
  };

  const onHandleKey = (event: KeyboardEvent): void => {
    if (event.key === "ArrowLeft") {
      event.preventDefault();
      applyWidth(width + 24);
    } else if (event.key === "ArrowRight") {
      event.preventDefault();
      applyWidth(width - 24);
    }
  };

  const onHandlePointerDown = (event: PointerEvent): void => {
    drag.current = { x: event.clientX, width };
    (event.currentTarget as HTMLElement).setPointerCapture(event.pointerId);
  };

  const onHandlePointerMove = (event: PointerEvent): void => {
    const state = drag.current;
    if (state === null) return;
    applyWidth(state.width + (state.x - event.clientX));
  };

  const endDrag = (): void => {
    drag.current = null;
  };

  const copy = (): void => {
    const written = navigator.clipboard?.writeText(card.id);
    if (written !== undefined)
      void written.then(
        () => setCopied(true),
        () => undefined,
      );
  };

  const deps = dependencyRows(card, cards);
  const depGroups = (deps.waits.length > 0 || deps.unblocks.length > 0) && (
    <div class="panel-deps">
      {deps.waits.length > 0 && <DepGroup label="Waits on" rows={deps.waits} onOpenDep={onOpenDep} />}
      {deps.unblocks.length > 0 && <DepGroup label="Unblocks" rows={deps.unblocks} onOpenDep={onOpenDep} />}
    </div>
  );
  if (phone) {
    return (
      <Sheet
        open
        onClose={onClose}
        title={
          <>
            <span class="ui-mono">{card.id}:</span> {card.title}
          </>
        }
        actions={
          <Button variant="ghost" label={`Copy ${card.id}`} onClick={copy}>
            {copied ? "Copied" : "Copy"}
          </Button>
        }
      >
        <div class="panel in-sheet">
          {card.summary !== "" && <p class="panel-summary">{card.summary}</p>}
          <HeaderChips card={card} featureClosed={ticket?.card.feature_closed === true} />
          {depGroups}
          <PanelContent loading={loading} failed={failed} retry={retry} ticket={ticket} />
        </div>
      </Sheet>
    );
  }
  return (
    <>
      {!docked && <div class="panel-scrim" aria-hidden="true" onClick={onClose} />}
      <div
        class={docked ? "panel docked" : "panel sheet"}
        role="dialog"
        aria-modal="true"
        aria-label={`${card.id}: ${card.title}`}
        data-drawer
        ref={panel}
        style={docked ? { width: `${width}px` } : undefined}
      >
        <PanelHeader
          card={card}
          copied={copied}
          onCopy={copy}
          onClose={onClose}
          featureClosed={ticket?.card.feature_closed === true}
        />
        {depGroups}
        <PanelContent loading={loading} failed={failed} retry={retry} ticket={ticket} />
        {docked && (
          <div
            role="separator"
            class="handle"
            aria-orientation="vertical"
            aria-label="Panel width"
            aria-valuenow={width}
            aria-valuemin={PANEL_MIN_WIDTH}
            aria-valuemax={PANEL_MAX_WIDTH}
            tabIndex={0}
            onKeyDown={onHandleKey}
            onPointerDown={onHandlePointerDown}
            onPointerMove={onHandlePointerMove}
            onPointerUp={endDrag}
            onPointerCancel={endDrag}
          />
        )}
      </div>
    </>
  );
}
