/**
 * The timeline view: what happened and what the owner decided, one time-ordered list — radar's
 * events (sessions starting, agents spawning, compactions, errors) beside huddle's audit log
 * (allows, answers, sends, pairings, sign-ins), newest first, with filter chips for Events and
 * Decisions. The audit page reloads while the view is open; the events arrive on the stream.
 */

import type { JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import { AUDIT_PAGE, type AuditRow, auditTs, fetchAudit } from "../../audit.ts";
import { fmtCount, sessionName } from "../../fmt.ts";
import { eventMeta, STATUS_COLOR } from "../../palette.ts";
import type { EventRecord } from "../../shared/model.ts";
import { type ClientState, visibleEvents } from "../../state.ts";
import { useApp } from "../context.ts";
import { Icon } from "../Icon.tsx";
import { Code, EmptyState, Panel } from "../kit.tsx";
import { timeText } from "../table.tsx";

function nameOfSession(state: ClientState, id: string | null): string {
  if (id === null) return "No session";
  const item = state.sessions.find((s) => s.id === id);
  return item === undefined ? id.slice(0, 8) : sessionName(item);
}

/** One event on the timeline: icon on the spine, what happened to the right, when on the left. */
function TimelineRow({ event, first, last }: { event: EventRecord; first: boolean; last: boolean }) {
  const { state, now } = useApp();
  const meta = eventMeta(event.kind);
  const session = nameOfSession(state, event.sessionId);
  const when = timeText(state, event.ts, now);
  return (
    <li>
      {!first && <hr class="bg-base-content/15" />}
      <div class="timeline-middle">
        <span
          class="inline-grid size-8 place-items-center rounded-field"
          style={`color:${STATUS_COLOR[meta.tone]};background:color-mix(in srgb, ${STATUS_COLOR[meta.tone]} 15%, transparent)`}
          aria-hidden="true"
        >
          <Icon name={meta.icon} class="icon" />
        </span>
      </div>
      {!last && <hr class="bg-base-content/15" />}
      <div class="timeline-end min-w-0">
        <div class="flex flex-wrap items-baseline gap-x-2.5">
          <span class="text-sm font-medium" title={event.kind} data-kind-label>
            {meta.label}
          </span>
          {event.label !== null && event.label !== "" && (
            <span class="text-sm text-base-content/80" data-event-label>
              {event.label}
            </span>
          )}
        </div>
        <div
          class="mt-0.5 flex flex-wrap items-center gap-x-3 text-meta text-base-content/60"
          data-event-where
        >
          <span>{session}</span>
          {event.sessionId !== null && (
            <Code text={event.sessionId.slice(0, 8)} class="text-base-content/60" />
          )}
          <Code text={event.agentId ?? "main"} class="text-base-content/60" />
        </div>
      </div>
      <time
        class="timeline-start hidden md:block text-meta text-base-content/60 whitespace-nowrap"
        title={when.title}
        datetime={new Date(event.ts).toISOString()}
      >
        {when.text}
      </time>
    </li>
  );
}

/** The words a decision's action reads as; an unknown one keeps its own name. */
const ACTION_LABEL: Record<string, string> = {
  signin: "Signed in",
  allow: "Allowed a tool",
  deny: "Denied a tool",
  answer: "Answered a question",
  send: "Sent work",
  link: "Linked sessions",
  unlink: "Unlinked sessions",
  pair: "Paired a device",
  remove: "Removed",
  pause: "Paused",
  resume: "Resumed",
};

/** One decision on the timeline: the shield tile, who did what to what, when. */
function AuditTimelineRow({ row, first, last }: { row: AuditRow; first: boolean; last: boolean }) {
  const { state, now } = useApp();
  const ts = auditTs(row);
  const when = timeText(state, Number.isNaN(ts) ? now : ts, now);
  const label = ACTION_LABEL[row.action] ?? row.action;
  return (
    <li>
      {!first && <hr class="bg-base-content/15" />}
      <div class="timeline-middle">
        <span
          class="inline-grid size-8 place-items-center rounded-field"
          style={`color:${STATUS_COLOR.info};background:color-mix(in srgb, ${STATUS_COLOR.info} 15%, transparent)`}
          aria-hidden="true"
        >
          <Icon name="check" class="icon" />
        </span>
      </div>
      {!last && <hr class="bg-base-content/15" />}
      <div class="timeline-end min-w-0">
        <div class="flex flex-wrap items-baseline gap-x-2.5">
          <span class="text-sm font-medium" data-kind-label>
            {label}
          </span>
          <span class="text-sm text-base-content/80" data-event-label>
            {row.target}
          </span>
        </div>
        <div
          class="mt-0.5 flex flex-wrap items-center gap-x-3 text-meta text-base-content/60"
          data-event-where
        >
          <span>{row.actor}</span>
          <span>decision</span>
        </div>
      </div>
      <time
        class="timeline-start hidden md:block text-meta text-base-content/60 whitespace-nowrap"
        title={when.title}
        datetime={row.at}
      >
        {when.text}
      </time>
    </li>
  );
}

/** Which side of the merged list a chip keeps: both, the events, or the decisions. */
type Chip = "all" | "events" | "decisions";

const CHIPS: { id: Chip; label: string }[] = [
  { id: "all", label: "All" },
  { id: "events", label: "Events" },
  { id: "decisions", label: "Decisions" },
];

/** The timeline tab: radar's events and the audit log's decisions in one order, chips to narrow. */
export function TimelineView({
  auditFetch = fetchAudit,
  auditEveryMs = 30_000,
}: {
  /** The audit read, injectable so tests answer it without a server. */
  auditFetch?: typeof fetchAudit;
  auditEveryMs?: number;
}): JSX.Element {
  const { state } = useApp();
  const [audit, setAudit] = useState<{ rows: AuditRow[]; refused: string | null } | null>(null);
  const [chip, setChip] = useState<Chip>("all");

  useEffect(() => {
    let live = true;
    const read = (): void => {
      void auditFetch().then((page) => {
        if (live) setAudit({ rows: page.rows, refused: page.refused });
      });
    };
    read();
    if (auditEveryMs === 0)
      return () => {
        live = false;
      };
    const timer = setInterval(read, auditEveryMs);
    return () => {
      live = false;
      clearInterval(timer);
    };
  }, [auditFetch, auditEveryMs]);

  const total = visibleEvents(state);
  const events = total.slice(0, 200); // the newest 200; a huge session must not sink the page
  const decisions = (audit?.rows ?? []).slice(0, AUDIT_PAGE).filter((row) => !Number.isNaN(auditTs(row)));
  const merged: (
    | { kind: "event"; event: EventRecord; ts: number }
    | { kind: "audit"; row: AuditRow; ts: number }
  )[] = [
    ...(chip === "decisions" ? [] : events.map((event) => ({ kind: "event" as const, event, ts: event.ts }))),
    ...(chip === "events" ? [] : decisions.map((row) => ({ kind: "audit" as const, row, ts: auditTs(row) }))),
  ].sort((a, b) => b.ts - a.ts);
  const count = `${fmtCount(total.length, "event")}${decisions.length > 0 ? `, ${fmtCount(decisions.length, "decision")}` : ""}, newest first`;
  return (
    <Panel
      icon="history"
      title="Timeline"
      subtitle={count}
      actions={
        <fieldset class="m-0 flex gap-1.5 border-0 p-0" aria-label="Filter the timeline">
          {CHIPS.map((entry) => (
            <button
              key={entry.id}
              type="button"
              data-chip={entry.id}
              aria-pressed={chip === entry.id ? "true" : "false"}
              class={chip === entry.id ? "btn btn-soft btn-primary btn-sm" : "btn btn-ghost btn-sm"}
              onClick={() => setChip(entry.id)}
            >
              {entry.label}
            </button>
          ))}
        </fieldset>
      }
    >
      {merged.length === 0 ? (
        <EmptyState
          title={chip === "decisions" ? "No decisions yet" : "Nothing has happened yet"}
          hint={
            chip === "events"
              ? "Sessions starting, agents spawning, compactions and errors land here in order as they happen."
              : "Sessions starting, agents spawning, compactions and errors, beside every allow, answer, send and pairing, land here in order."
          }
          icon="history"
        />
      ) : (
        <ol
          class="timeline timeline-vertical timeline-compact"
          aria-label="Events and decisions, newest first"
        >
          {merged.map((row, index) =>
            row.kind === "event" ? (
              <TimelineRow
                key={`event/${row.event.ts}/${row.event.kind}/${row.event.sessionId}/${index}`}
                event={row.event}
                first={index === 0}
                last={index === merged.length - 1}
              />
            ) : (
              <AuditTimelineRow
                key={`audit/${row.row.id}`}
                row={row.row}
                first={index === 0}
                last={index === merged.length - 1}
              />
            ),
          )}
        </ol>
      )}
      {audit?.refused !== null && audit?.refused !== undefined && (
        <p class="mb-0 mt-4 text-meta text-base-content/60" data-audit-refused>
          {audit.refused}
        </p>
      )}
    </Panel>
  );
}
