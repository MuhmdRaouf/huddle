// Inbox.tsx — the Inbox page: only what needs the owner, from one read of GET /attention — asks
// to you (quick replies and a text reply), tasks gated on your approval (plain or with a note),
// paused sessions with resume, blocked tasks with unblock and reassign — plus, when Radar runs,
// the approval requests and alerts extras.js added, with dismiss / dismiss all / pause. Each
// card carries its one or two actions and leaves the list once handled; focus moves to the card
// now in its place, and the list ends on "All clear". (inbox.js, extras.js inboxHTML/wireInbox)

import { Badge } from "@huddle/ui/badge.tsx";
import { Button } from "@huddle/ui/button.tsx";
import { Field } from "@huddle/ui/field.tsx";
import { Markdown } from "@huddle/ui/markdown.tsx";
import type { ToastFn } from "@huddle/ui/toast.tsx";
import type { ComponentChildren, JSX } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type { Api } from "../api.ts";
import { Icon } from "../icons.tsx";
import { Avatar, Skeleton, Time } from "../kit.tsx";
import { STATM, type TaskStatus, whoL } from "../status.ts";
import {
  type Approval,
  type AskItem,
  type BlockedItem,
  type Extras,
  type GateItem,
  type HuddleState,
  type HuddleStore,
  type PausedItem,
  type RadarAlert,
  type RosterSession,
  usd,
} from "../store.ts";

/** The way core.js spelled a query-string value. */
const enc = encodeURIComponent;

/** The quick replies every ask offers (inbox.js QUICK). */
export const QUICK: readonly string[] = ["Yes, go ahead", "No, stop", "Wait for me"];

/** Radar's alert kinds in words (extras.js KIND). */
export const ALERT_KIND: Record<string, string> = {
  stuck: "Stuck",
  loop: "Looping",
  retry_storm: "Retry storm",
  budget: "Budget",
  context: "Context filling up",
};

/** An alert's kind in words, or the raw kind when the table does not know it. */
export const alertKind = (kind: string): string => ALERT_KIND[kind] ?? kind;

/** A counted noun: "3 questions", "1 question"; "" for none. */
const many = (n: number, word: string): string => (n ? `${n} ${word}${n > 1 ? "s" : ""}` : "");

/** How many of the extras need the owner: approvals plus Radar alerts, for this channel. */
export function extrasNeeds(extras: Extras | null | undefined, ch: string): number {
  if (!extras || extras.ch !== ch) return 0;
  return extras.approvals.length + (extras.obs?.alerts?.length ?? 0);
}

/** "2 permission requests · 1 Radar alert", or "" when there is nothing or the channel differs. */
export function extrasNeedsText(extras: Extras | null | undefined, ch: string): string {
  if (!extras || extras.ch !== ch) return "";
  return [
    many(extras.approvals.length, "permission request"),
    many(extras.obs?.alerts?.length ?? 0, "Radar alert"),
  ]
    .filter(Boolean)
    .join(" · ");
}

/** The Inbox count: asks + gates + paused + blocked, plus the extras' needs. */
export function inboxCountOf(state: HuddleState, ch: string): number {
  const a = state.attention;
  return (
    (a?.asks?.length ?? 0) +
    (a?.gates?.length ?? 0) +
    (a?.paused?.length ?? 0) +
    (a?.blocked?.length ?? 0) +
    extrasNeeds(state.extras, ch)
  );
}

/** The header's roll-up: "2 questions · 1 approval · 1 paused · 1 permission request". */
export function summaryOf(state: HuddleState, ch: string): string {
  const paused = state.attention?.paused ?? [];
  const blocked = state.attention?.blocked ?? [];
  return [
    many(state.attention?.asks?.length ?? 0, "question"),
    many(state.attention?.gates?.length ?? 0, "approval"),
    paused.length ? `${paused.length} paused` : "",
    blocked.length ? `${blocked.length} blocked` : "",
    extrasNeedsText(state.extras, ch),
  ]
    .filter(Boolean)
    .join(" · ");
}

/** Who a blocked task can move to: the sessions that stayed, minus its current owner. */
export function reassignOwners(
  sessions: readonly RosterSession[] | null | undefined,
  owner: string | null | undefined,
): string[] {
  const names = new Set<string>();
  for (const s of sessions ?? []) {
    if (s.state === "left" || s.parent) continue;
    names.add(s.name);
  }
  return [...names].filter((n) => n !== owner);
}

/** What the composer is asked to open with (compose.js's openCompose prefill). */
export type ComposePrefill = {
  to?: string | undefined;
  mode?: "msg" | "ask" | "task" | undefined;
};

/** The Inbox page's props: the store's snapshot and the API client, plus the shell's ways out. */
export type InboxProps = {
  /** The open channel's data: attention, sessions, board/byId and the extras. */
  state: HuddleState;
  /** The signed-in API client. */
  api: Api;
  /** The open channel. */
  ch: string;
  /** The render clock, epoch ms; relative times are deterministic against it. */
  now: number;
  /** The toast stack (core.js's toast). */
  toast: ToastFn;
  /** Opens the composer with a prefill (compose.js's openCompose). */
  onCompose: (prefill: ComposePrefill) => void;
  /** Opens a task (app.js's openTask). */
  onOpenTask: (id: string) => void;
  /** Opens a session (app.js's openSession). */
  onOpenSession: (name: string) => void;
  /** The store, when the shell hands it over: handled cards reload attention through its
   *  loaders, the way data.js's loadAtt did. Without it the page still calls the same ops and
   *  the live stream's refills catch up. */
  store?: HuddleStore | undefined;
};

/** The li wrapper every card shares, carrying its handled key (inbox.js item). */
function Card({
  k,
  hidden,
  children,
}: {
  k: string;
  hidden: boolean;
  children: ComponentChildren;
}): JSX.Element {
  return (
    <li class="ui-card" data-item={k} hidden={hidden}>
      {children}
    </li>
  );
}

/** A titled, counted section; nothing when the count is zero (inbox.js section, extras.js section). */
function Section({
  id,
  title,
  n,
  extra,
  children,
}: {
  id: string;
  title: string;
  n: number;
  extra?: ComponentChildren | undefined;
  children: ComponentChildren;
}): JSX.Element | null {
  if (!n) return null;
  return (
    <section class="flex flex-col gap-2" aria-labelledby={`ih-${id}`}>
      <h2 class="text-sm font-semibold flex items-center gap-2 px-1" id={`ih-${id}`}>
        {title}
        <span class="text-xs font-medium text-[var(--ui-muted)] tabular-nums">{n}</span>
        {extra ? (
          <>
            <span class="flex-1" />
            {extra}
          </>
        ) : null}
      </h2>
      <ul class="flex flex-col gap-2">{children}</ul>
    </section>
  );
}

/** A task link chip: the id in mono and the truncated title (inbox.js tlink). */
function TaskLink({
  t,
  onOpenTask,
}: {
  t: { id: string; href: string; title: string };
  onOpenTask: (id: string) => void;
}): JSX.Element {
  return (
    <a
      class="inline-flex max-w-full items-center gap-1.5 hover:underline"
      href={t.href}
      onClick={(e) => {
        e.preventDefault();
        onOpenTask(t.id);
      }}
    >
      <Badge tone="neutral">
        <span class="ui-mono">{t.id}</span>
      </Badge>
      <span class="truncate">{t.title}</span>
    </a>
  );
}

/** A question card: who asks, the message, the quick replies and a text reply (inbox.js askItem). */
function AskCard({
  a,
  k,
  hidden,
  now,
  task,
  sending,
  onOpenTask,
  onReply,
}: {
  a: AskItem;
  k: string;
  hidden: boolean;
  now: number;
  task: { id: string; href: string; title: string } | null;
  sending: boolean;
  onOpenTask: (id: string) => void;
  onReply: (text: string) => void;
}): JSX.Element {
  const ta = useRef<HTMLTextAreaElement>(null);
  return (
    <Card k={k} hidden={hidden}>
      <div class="flex items-start gap-3">
        <Avatar name={a.from} />
        <div class="min-w-0 flex-1">
          <div class="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-[var(--ui-muted)]">
            <b class="text-sm text-[var(--ui-text)]">{a.from}</b>
            <span>
              asks you · <Time ts={a.ts} now={now} />
            </span>
            {task ? (
              <span class="text-xs">
                about <TaskLink t={task} onOpenTask={onOpenTask} />
              </span>
            ) : null}
          </div>
          <div class="md mt-1">
            <Markdown text={a.msg ?? ""} />
          </div>
          {/* biome-ignore lint/a11y/useSemanticElements: the legacy quick replies' group, kept word for word */}
          <div class="mt-3 flex flex-wrap gap-1.5" role="group" aria-label="Quick replies">
            {QUICK.map((q) => (
              <Button
                variant="ghost"
                key={q}
                data-quick={String(a.seq)}
                disabled={sending}
                onClick={() => onReply(q)}
              >
                {q}
              </Button>
            ))}
          </div>
          {/* the field takes the row and the Reply button sits under its end on a phone, where
              the placeholder wraps; from sm the two share the row */}
          <div class="mt-2 flex flex-col gap-2 tablet:flex-row tablet:items-end">
            <label class="sr-only" for={`ar-${a.seq}`}>
              {`Your reply to ${a.from}`}
            </label>
            <textarea
              id={`ar-${a.seq}`}
              ref={ta}
              rows={1}
              class="min-h-[var(--ui-control-h)] max-tablet:min-h-[calc(2*var(--ui-leading)*var(--ui-text-md)+2*var(--ui-space-2))] flex-1 resize-none rounded-[var(--ui-radius)] bg-[var(--ui-surface-2)] px-3 py-2 text-[length:var(--ui-text-md)] leading-[var(--ui-leading)] text-[var(--ui-text)] placeholder:text-[var(--ui-muted)] focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--ui-accent)]"
              placeholder="Write a reply (Enter sends, Shift+Enter adds a line)"
              onKeyDown={(e) => {
                if (e.key === "Enter" && !e.shiftKey && !e.isComposing) {
                  e.preventDefault();
                  onReply(ta.current?.value ?? "");
                }
              }}
              onInput={(e) => {
                const t = e.currentTarget;
                t.style.height = "auto";
                t.style.height = `${Math.min(t.scrollHeight + 2, 200)}px`;
              }}
            />
            <Button
              data-send={String(a.seq)}
              disabled={sending}
              class="self-end"
              onClick={() => onReply(ta.current?.value ?? "")}
            >
              <Icon name="send" class="size-4" />
              Reply
            </Button>
          </div>
        </div>
      </div>
    </Card>
  );
}

/** An approval card: the gated task, Approve, and the note variant with its input (inbox.js gateItem). */
function GateCard({
  g,
  k,
  hidden,
  note,
  busy,
  task,
  onOpenTask,
  onApprove,
  onOpenNote,
  onCloseNote,
  onNoteText,
}: {
  g: GateItem;
  k: string;
  hidden: boolean;
  note: string | undefined;
  busy: boolean;
  task: { id: string; href: string; title: string };
  onOpenTask: (id: string) => void;
  onApprove: () => void;
  onOpenNote: () => void;
  onCloseNote: (refocus: boolean) => void;
  onNoteText: (text: string) => void;
}): JSX.Element {
  const owner = g.owner || "the session";
  return (
    <Card k={k} hidden={hidden}>
      <div class="flex flex-col gap-3 tablet:flex-row tablet:items-center">
        <div class="flex min-w-0 flex-1 items-start gap-3">
          <span class="tinted ink inline-flex size-7 shrink-0 items-center justify-center rounded-lg c-blue">
            <Icon name="key" />
          </span>
          <div class="min-w-0">
            <div class="text-sm font-medium">
              <TaskLink t={task} onOpenTask={onOpenTask} />
            </div>
            <div class="mt-0.5 text-xs text-[var(--ui-muted)]">
              {g.owner && g.owner !== "owner" ? `${g.owner} waits` : "It waits"}
              {" for your go-ahead before it starts"}
              {g.gate === "ask-first" ? " (ask first)" : ""}.
            </div>
          </div>
        </div>
        <div class="flex flex-wrap gap-1.5 tablet:justify-end">
          <Button data-approve={g.id} disabled={busy} onClick={() => onApprove()}>
            <Icon name="check" class="size-4" />
            Approve
          </Button>
          {note === undefined ? (
            <Button variant="secondary" data-approve-note={g.id} onClick={onOpenNote}>
              Approve with a note
            </Button>
          ) : null}
        </div>
      </div>
      {note !== undefined ? (
        // Enter approves and Escape closes from the field inside; the wrapper carries the keys
        // because the field owns none, and data-note-for is where the focus lands when it opens
        <div
          class="mt-3 flex items-end gap-2 tablet:pl-9"
          data-note-for={g.id}
          onKeyDown={(e) => {
            if (e.key === "Enter") onApprove();
            if (e.key === "Escape") {
              e.preventDefault();
              onCloseNote(false);
            }
          }}
        >
          <div class="min-w-0 flex-1">
            <Field
              label={`Note for ${owner}`}
              value={note}
              onInput={onNoteText}
              placeholder={`Note for ${owner}`}
            />
          </div>
          <Button data-approve={g.id} disabled={busy} onClick={() => onApprove()}>
            Approve
          </Button>
          <Button variant="ghost" data-close={g.id} onClick={() => onCloseNote(true)}>
            Cancel
          </Button>
        </div>
      ) : null}
    </Card>
  );
}

/** A paused-session card: who paused it and when, a message button and resume (inbox.js pausedItem). */
function PauseCard({
  p,
  k,
  hidden,
  now,
  href,
  busy,
  onOpenSession,
  onResume,
  onCompose,
}: {
  p: PausedItem;
  k: string;
  hidden: boolean;
  now: number;
  href: string;
  busy: boolean;
  onOpenSession: (name: string) => void;
  onResume: () => void;
  onCompose: (prefill: ComposePrefill) => void;
}): JSX.Element {
  return (
    <Card k={k} hidden={hidden}>
      <div class="flex flex-wrap items-center gap-3">
        <span class="tinted ink inline-flex size-7 shrink-0 items-center justify-center rounded-lg c-mauve">
          <Icon name="pause" />
        </span>
        <div class="min-w-0 flex-1 basis-56">
          <a
            class="text-sm font-medium hover:underline"
            href={href}
            onClick={(e) => {
              e.preventDefault();
              onOpenSession(p.name);
            }}
          >
            {`${p.name} is paused`}
          </a>
          <div class="text-xs text-[var(--ui-muted)]">
            {`Paused by ${whoL(p.by || "someone")} `}
            <Time ts={p.at} now={now} />
            {". Its changes are refused until you resume it."}
          </div>
        </div>
        <div class="flex gap-1.5">
          <Button
            variant="secondary"
            data-msg={p.name}
            onClick={() => onCompose({ to: p.name, mode: "msg" })}
          >
            <Icon name="msg" class="size-4" />
            Message
          </Button>
          <Button data-resume={p.name} disabled={busy} onClick={() => onResume()}>
            <Icon name="play" class="size-4" />
            {`Resume ${p.name}`}
          </Button>
        </div>
      </div>
    </Card>
  );
}

/** A blocked-task card: why, what it waits on, unblock and reassign (inbox.js blockedItem). */
function BlockedCard({
  b,
  k,
  hidden,
  task,
  waitsOn,
  owners,
  hrefOf,
  titleOf,
  onOpenTask,
  onUnblock,
  onReassign,
}: {
  b: BlockedItem;
  k: string;
  hidden: boolean;
  task: { id: string; href: string; title: string };
  waitsOn: readonly string[];
  owners: readonly string[];
  hrefOf: (id: string) => string;
  titleOf: (id: string) => string;
  onOpenTask: (id: string) => void;
  onUnblock: () => void;
  onReassign: (to: string) => void;
}): JSX.Element {
  return (
    <Card k={k} hidden={hidden}>
      <div class="flex items-start gap-3">
        <span class="tinted ink inline-flex size-7 shrink-0 items-center justify-center rounded-lg c-red">
          <Icon name="ban" />
        </span>
        <div class="min-w-0 flex-1">
          <div class="text-sm font-medium">
            <TaskLink t={task} onOpenTask={onOpenTask} />
          </div>
          <div class="mt-0.5 text-xs text-[var(--ui-muted)]">
            {b.owner ? (b.owner === "owner" ? "Yours" : `Owned by ${b.owner}`) : "Nobody owns it"}
            {b.note ? `. “${b.note}”` : ""}
          </div>
          {waitsOn.length ? (
            <div class="mt-2 flex flex-wrap items-center gap-1.5 text-xs">
              <span class="text-[var(--ui-muted)]">Waits on</span>
              {waitsOn.map((id) => (
                <TaskLink key={id} t={{ id, href: hrefOf(id), title: titleOf(id) }} onOpenTask={onOpenTask} />
              ))}
            </div>
          ) : null}
          <div class="mt-3 flex flex-wrap items-center gap-1.5">
            <Button variant="secondary" data-unblock={b.id} onClick={() => onUnblock()}>
              <Icon name="undo" class="size-4" />
              Unblock
            </Button>
            <label class="sr-only" for={`ro-${b.id}`}>
              {`Give ${b.id} to`}
            </label>
            <select
              class="min-h-[var(--ui-control-h)] w-auto rounded-[var(--ui-radius)] bg-[var(--ui-surface-2)] px-3 text-[length:var(--ui-text-md)] text-[var(--ui-text)]"
              id={`ro-${b.id}`}
              data-reassign={b.id}
              onChange={(e) => onReassign(e.currentTarget.value)}
            >
              <option value="" selected>
                Give it to…
              </option>
              <option value="owner">Me</option>
              {owners.map((o) => (
                <option key={o}>{o}</option>
              ))}
            </select>
            <a
              class="ui-btn ui-btn-ghost"
              href={task.href}
              onClick={(e) => {
                e.preventDefault();
                onOpenTask(b.id);
              }}
            >
              Open task
            </a>
          </div>
        </div>
      </div>
    </Card>
  );
}

/** An approval request card: who wants to run what, with dismiss (extras.js inboxHTML). */
function ApprovalCard({
  a,
  k,
  hidden,
  now,
  href,
  busy,
  onOpenSession,
  onDismiss,
}: {
  a: Approval;
  k: string;
  hidden: boolean;
  now: number;
  href: string;
  busy: boolean;
  onOpenSession: (name: string) => void;
  onDismiss: () => void;
}): JSX.Element {
  return (
    <Card k={k} hidden={hidden}>
      <div class="flex flex-wrap items-center gap-3">
        <span class="tinted ink inline-flex size-7 shrink-0 items-center justify-center rounded-lg c-blue">
          <Icon name="key" />
        </span>
        <div class="min-w-0 flex-1 basis-56">
          <div class="text-sm">
            <a
              class="font-medium hover:underline"
              href={href}
              onClick={(e) => {
                e.preventDefault();
                onOpenSession(a.from);
              }}
            >
              {a.from}
            </a>
            {" wants to run a command your rules name: "}
            <b class="font-medium">{(a.labels ?? []).join(", ") || "a command"}</b>
          </div>
          <div class="text-xs text-[var(--ui-muted)]">
            <Time ts={a.ts} now={now} />
            {" · Claude Code asks for the go-ahead in that session's window; answer there."}
          </div>
        </div>
        <div class="flex gap-1.5">
          <a
            class="ui-btn ui-btn-secondary"
            href={href}
            onClick={(e) => {
              e.preventDefault();
              onOpenSession(a.from);
            }}
          >
            <Icon name="users" class="size-4" />
            Open session
          </a>
          <Button variant="ghost" data-xdismiss={String(a.seq)} disabled={busy} onClick={() => onDismiss()}>
            Dismiss
          </Button>
        </div>
      </div>
    </Card>
  );
}

/** A Radar alert card: which session, why, and pause — or the pill once it is paused (extras.js inboxHTML). */
function AlertCard({
  a,
  k,
  hidden,
  now,
  href,
  busy,
  pausedHere,
  onOpenSession,
  onPause,
}: {
  a: RadarAlert;
  k: string;
  hidden: boolean;
  now: number;
  href: string;
  busy: boolean;
  pausedHere: boolean;
  onOpenSession: (name: string) => void;
  onPause: () => void;
}): JSX.Element {
  return (
    <Card k={k} hidden={hidden}>
      <div class="flex flex-wrap items-center gap-3">
        <span
          class={`tinted ink inline-flex size-7 shrink-0 items-center justify-center rounded-lg ${a.kind === "budget" ? "c-red" : "c-peach"}`}
        >
          <Icon name="alert" />
        </span>
        <div class="min-w-0 flex-1 basis-56">
          <div class="text-sm">
            <a
              class="font-medium hover:underline"
              href={href}
              onClick={(e) => {
                e.preventDefault();
                onOpenSession(a.session);
              }}
            >
              {a.session}
            </a>
            {a.agent ? <span class="text-[var(--ui-muted)]"> (a subagent)</span> : null}
            {`: ${alertKind(a.kind)}`}
          </div>
          <div class="text-xs text-[var(--ui-muted)]">
            {a.detail}
            {a.since ? (
              <>
                {" · since "}
                <Time ts={new Date(a.since).toISOString()} now={now} />
              </>
            ) : null}
            {a.cost != null ? ` · ${usd(a.cost)} so far` : ""}
          </div>
        </div>
        <div class="flex gap-1.5">
          <a
            class="ui-btn ui-btn-secondary"
            href={href}
            onClick={(e) => {
              e.preventDefault();
              onOpenSession(a.session);
            }}
          >
            Open session
          </a>
          {pausedHere ? (
            <Badge tone="warn">Paused</Badge>
          ) : (
            <Button
              data-xpause={a.session}
              data-xwhy={alertKind(a.kind)}
              disabled={busy}
              onClick={() => onPause()}
            >
              <Icon name="pause" class="size-4" />
              {`Pause ${a.session}`}
            </Button>
          )}
        </div>
      </div>
    </Card>
  );
}

/** The bold text of a status word, the way work.js's setTaskStatus announced it. */
const statusWord = (st: TaskStatus): string => STATM[st].l.toLowerCase();

/** The message of a failed call, however it failed. */
const errText = (e: unknown): string => (e instanceof Error ? e.message : String(e));

/** The toast text of a failed op: the 404 "no operation" becomes an update text-xs muted (core.js act). */
function refusal(e: unknown, name: string): string {
  const raw = errText(e);
  const status = (e as { status?: number }).status;
  return status === 404 && /no operation/.test(raw)
    ? `This Huddle server does not support “${name}” yet. Update the server.`
    : raw;
}

/** The empty Inbox: the all-clear card and its two ways out (inbox.js paint). */
function AllClear({ ch, api }: { ch: string; api: Api }): JSX.Element {
  return (
    <div class="ui-card flex flex-col items-center gap-2 px-6 py-14 text-center text-sm text-[var(--ui-muted)]">
      <span class="tinted ink c-good mb-1 inline-flex size-12 shrink-0 items-center justify-center rounded-full">
        <Icon name="check" class="size-6" />
      </span>
      <b class="text-base font-semibold text-[var(--ui-text)]" tabindex={-1} id="ib-clear">
        All clear
      </b>
      <span>No questions, approvals, paused sessions or blocked tasks.</span>
      <span class="max-w-sm text-sm text-[var(--ui-muted)]">The sessions are working on their own.</span>
      <div class="mt-2 flex flex-wrap justify-center gap-2">
        <Button variant="secondary" href={api.channelHref(ch, "/team")}>
          <Icon name="users" class="size-4" />
          See the team
        </Button>
        <Button variant="secondary" href={api.channelHref(ch, "/work")}>
          <Icon name="list" class="size-4" />
          Plan work
        </Button>
      </div>
    </div>
  );
}

/** Is this alert's session paused right now? Its pause button is a pill then. */
const isPausedHere = (state: HuddleState, name: string): boolean =>
  (state.sessions?.sessions ?? []).some((s) => s.name === name && s.control === "pause");

/** The extras' two sections: the approval requests and Radar's alerts (extras.js inboxHTML). */
function ExtrasSections({
  ext,
  state,
  now,
  hiddenKeys,
  btnBusy,
  sessH,
  onOpenSession,
  onDismiss,
  onDismissAll,
  onPause,
}: {
  ext: Extras;
  state: HuddleState;
  now: number;
  hiddenKeys: ReadonlySet<string>;
  btnBusy: string | null;
  sessH: (name: string) => string;
  onOpenSession: (name: string) => void;
  onDismiss: (seq: number) => void;
  onDismissAll: () => void;
  onPause: (name: string, why: string) => void;
}): JSX.Element {
  return (
    <>
      <Section
        id="approvals"
        title="Asked for your permission"
        n={ext.approvals.length}
        extra={
          ext.approvals.length > 1 ? (
            <Button
              variant="ghost"
              data-xdismiss-all=""
              disabled={btnBusy === "xdall"}
              onClick={onDismissAll}
            >
              Dismiss all
            </Button>
          ) : null
        }
      >
        {ext.approvals.map((ap) => (
          <ApprovalCard
            key={ap.seq}
            a={ap}
            k={`x${ap.seq}`}
            hidden={hiddenKeys.has(`x${ap.seq}`)}
            now={now}
            href={sessH(ap.from)}
            busy={btnBusy === `xd:${ap.seq}`}
            onOpenSession={onOpenSession}
            onDismiss={() => onDismiss(ap.seq)}
          />
        ))}
      </Section>
      <Section id="obs" title="Alerts from Radar" n={ext.obs?.alerts?.length ?? 0}>
        {(ext.obs?.alerts ?? []).map((al) => (
          <AlertCard
            key={al.id}
            a={al}
            k={`o${al.id}`}
            hidden={hiddenKeys.has(`o${al.id}`)}
            now={now}
            href={sessH(al.session)}
            busy={btnBusy === `xp:${al.session}`}
            pausedHere={isPausedHere(state, al.session)}
            onOpenSession={onOpenSession}
            onPause={() => onPause(al.session, alertKind(al.kind))}
          />
        ))}
      </Section>
    </>
  );
}

/** The Inbox page: every card that needs the owner and its actions. */
export function Inbox({
  state,
  api,
  ch,
  now,
  toast,
  onCompose,
  onOpenTask,
  onOpenSession,
  store,
}: InboxProps): JSX.Element {
  const root = useRef<HTMLDivElement>(null);
  const focusSel = useRef<string | null>(null);
  const pendingFocus = useRef<number | null>(null);
  // the statuses this page set itself, on top of the board's (legacy setTaskStatus mutated byId)
  const known = useRef<Map<string, TaskStatus>>(new Map<string, TaskStatus>());
  const [notes, setNotes] = useState<Map<string, string>>(new Map());
  const [hiddenKeys, setHiddenKeys] = useState<ReadonlySet<string>>(new Set<string>());
  const [cardBusy, setCardBusy] = useState<string | null>(null);
  const [btnBusy, setBtnBusy] = useState<string | null>(null);

  const a = state.attention;
  const ext = state.extras?.ch === ch ? state.extras : null;
  const asks = a?.asks ?? [];
  const gates = a?.gates ?? [];
  const paused = a?.paused ?? [];
  const blocked = a?.blocked ?? [];
  const n = inboxCountOf(state, ch);
  const taskH = (id: string): string => api.channelHref(ch, `/inbox?t=${enc(id)}`);
  const sessH = (name: string): string => api.channelHref(ch, `/inbox?s=${enc(name)}`);
  const titleOf = (id: string): string => state.byId.get(id)?.title ?? "";
  const taskLinkOf = (
    id: string | null | undefined,
    title: string | undefined,
  ): { id: string; href: string; title: string } | null =>
    id ? { id, href: taskH(id), title: title || titleOf(id) } : null;

  // view(): the page fills itself from one read of attention on the way in
  useEffect(() => {
    void store?.loadAttention();
  }, [store, ch]);

  // handled(): when the reload lands, the hidden marks go and focus moves to the card in the
  // handled one's place, or to "All clear" when the list is empty (inbox.js paint's FOCUS)
  useEffect(() => {
    setHiddenKeys((h) => (h.size ? new Set<string>() : h));
    const want = pendingFocus.current;
    if (want === null) return;
    pendingFocus.current = null;
    const items = [...(root.current?.querySelectorAll("[data-item]") ?? [])];
    const el = items[Math.min(want, items.length - 1)];
    const t = el ? el.querySelector("button, textarea, a") : root.current?.querySelector("#ib-clear");
    (t as HTMLElement | null)?.focus();
  }, [a]);

  // the note field and the button that reopens it take the focus as they appear (wire()'s focus calls)
  useEffect(() => {
    const sel = focusSel.current;
    if (sel === null) return;
    focusSel.current = null;
    (root.current?.querySelector(sel) as HTMLElement | null)?.focus();
  }, [notes]);

  // a new channel starts clean: no note fields open, nothing hidden or in flight
  useEffect(() => {
    setNotes(new Map<string, string>());
    setHiddenKeys(new Set<string>());
    setCardBusy(null);
    setBtnBusy(null);
    pendingFocus.current = null;
    focusSel.current = null;
    known.current.clear();
  }, [ch]);

  if (!a) {
    return (
      <div ref={root} class="flex min-w-0 flex-col">
        <Skeleton rows={4} class="h-28" />
      </div>
    );
  }

  /** core.js act: one owner op, its toast; the server's refusal comes back as a bad toast. */
  async function act(
    name: string,
    args: Record<string, unknown>,
    ok: string,
    o?: { undo?: () => void },
  ): Promise<boolean> {
    try {
      await api.op(ch, name, args);
      if (ok) toast(ok, o?.undo ? { undo: o.undo } : {});
      return true;
    } catch (e) {
      toast(refusal(e, name), { bad: true });
      return false;
    }
  }

  /** The card a handled key belongs to: the li whose data-item carries it. */
  function cardOf(k: string): Element | null {
    return root.current?.querySelector(`[data-item="${CSS.escape(k)}"]`) ?? null;
  }

  /** handled(): the card leaves the list and focus moves to the card now in its place. */
  async function handled(k: string): Promise<void> {
    const items = [...(root.current?.querySelectorAll("[data-item]") ?? [])];
    const li = cardOf(k);
    pendingFocus.current = li ? items.indexOf(li) : -1;
    setHiddenKeys((h) => new Set(h).add(k));
    await store?.loadAttention();
  }

  /** inbox.js reply: send a quick or typed reply; an empty one just puts the caret back. */
  async function reply(seq: number, text: string, k: string): Promise<void> {
    const msg = text.trim();
    if (!msg) {
      (root.current?.querySelector(`#ar-${seq}`) as HTMLTextAreaElement | null)?.focus();
      return;
    }
    setCardBusy(k);
    if (await act("reply", { seq, msg }, "Reply sent")) {
      await handled(k);
    } else {
      setCardBusy(null);
    }
  }

  /** Approve a gated task, with the open note when there is one (wire()'s data-approve). */
  async function approve(id: string): Promise<void> {
    setBtnBusy(`ap:${id}`);
    if (await act("approve", { id, msg: (notes.get(id) ?? "").trim() }, `Approved ${id}`)) {
      setNotes((m) => {
        const next = new Map(m);
        next.delete(id);
        return next;
      });
      store?.boardChanged();
      await handled(`g${id}`);
    } else {
      setBtnBusy(null);
    }
  }

  /** Open the note field of one gate (data-approve-note). */
  function openNote(id: string): void {
    focusSel.current = `[data-note-for="${CSS.escape(id)}"] input`;
    setNotes((m) => {
      const next = new Map(m);
      next.set(id, "");
      return next;
    });
  }

  /** Close the note field of one gate; cancel puts the caret on the button that reopens it. */
  function closeNote(id: string, refocus: boolean): void {
    if (refocus) focusSel.current = `[data-approve-note="${CSS.escape(id)}"]`;
    setNotes((m) => {
      const next = new Map(m);
      next.delete(id);
      return next;
    });
  }

  /** work.js setTaskStatus: one status change with Undo, then the board and attention refill. */
  async function setTaskStatus(id: string, st: TaskStatus, note = ""): Promise<boolean> {
    const prev = known.current.get(id) ?? state.byId.get(id)?.status;
    if (!prev || prev === st) return false;
    const ok = await act("task_status", { id, status: st, note }, `${id} is ${statusWord(st)}`, {
      undo: () => void setTaskStatus(id, prev, `undo: back to ${prev}`),
    });
    if (!ok) return false;
    known.current.set(id, st);
    if (store) await store.loadBoard().catch(() => {});
    store?.attChanged();
    return true;
  }

  /** Unblock a blocked task: back to To do, noted for the board (data-unblock). */
  async function unblock(id: string): Promise<void> {
    if (await setTaskStatus(id, "todo", "unblocked by the owner")) await handled(`b${id}`);
  }

  /** Give a blocked task to another session (data-reassign), with Undo. */
  async function reassign(id: string, to: string): Promise<void> {
    if (!to) return;
    const was = state.byId.get(id)?.owner ?? null;
    const ok = await act(
      "task_update",
      { id, owner: to },
      `${id} now belongs to ${to === "owner" ? "you" : to}`,
      {
        undo: () =>
          act("task_update", { id, owner: was }, `${id} is back with ${was || "nobody"}`).then(() => {
            store?.boardChanged();
            store?.attChanged();
          }),
      },
    );
    if (ok) {
      store?.boardChanged();
      store?.attChanged();
    }
  }

  /** Resume a paused session (data-resume), with Undo. */
  async function resume(name: string): Promise<void> {
    setBtnBusy(`rs:${name}`);
    const ok = await act("resume", { target: name }, `${name} resumed`, {
      undo: () =>
        act("pause", { target: name }, `${name} paused again`).then(() => {
          void store?.loadSessions();
          void store?.loadAttention();
        }),
    });
    if (ok) {
      void store?.loadSessions();
      await handled(`p${name}`);
    } else {
      setBtnBusy(null);
    }
  }

  /** Drop one approval request (extras.js data-xdismiss). */
  async function dismiss(seq: number): Promise<void> {
    setBtnBusy(`xd:${seq}`);
    try {
      await api.api(api.channelPath(ch, "/x/approvals/dismiss?as=owner"), { body: { seq } });
      await store?.loadExtras(); // the count and the section repaint from here
    } catch (e) {
      toast(errText(e), { bad: true });
      setBtnBusy(null);
    }
  }

  /** Drop every approval request (extras.js data-xdismiss-all). */
  async function dismissAll(): Promise<void> {
    setBtnBusy("xdall");
    try {
      await api.api(api.channelPath(ch, "/x/approvals/dismiss?as=owner"), { body: { all: true } });
      await store?.loadExtras();
      toast("Dismissed");
    } catch (e) {
      toast(errText(e), { bad: true });
      setBtnBusy(null);
    }
  }

  /** Pause the session a Radar alert is about (extras.js data-xpause), with Undo. */
  async function pauseFor(name: string, why: string): Promise<void> {
    setBtnBusy(`xp:${name}`);
    const ok = await act("pause", { target: name, why: `Radar: ${why}` }, `${name} paused`, {
      undo: () =>
        act("resume", { target: name }, `${name} resumed`).then(() => {
          void store?.loadSessions();
          void store?.loadAttention();
        }),
    });
    if (ok) {
      await store?.loadSessions();
      await store?.loadAttention();
    } else {
      setBtnBusy(null);
    }
  }

  return (
    <div ref={root} class="flex min-w-0 flex-col gap-6">
      {n === 0 ? <AllClear ch={ch} api={api} /> : null}
      {ext ? (
        <ExtrasSections
          ext={ext}
          state={state}
          now={now}
          hiddenKeys={hiddenKeys}
          btnBusy={btnBusy}
          sessH={sessH}
          onOpenSession={onOpenSession}
          onDismiss={(seq) => void dismiss(seq)}
          onDismissAll={() => void dismissAll()}
          onPause={(name, why) => void pauseFor(name, why)}
        />
      ) : null}
      <Section id="asks" title="Questions for you" n={asks.length}>
        {asks.map((q) => (
          <AskCard
            key={q.seq}
            a={q}
            k={`a${q.seq}`}
            hidden={hiddenKeys.has(`a${q.seq}`)}
            now={now}
            task={taskLinkOf(q.task, "")}
            sending={cardBusy === `a${q.seq}`}
            onOpenTask={onOpenTask}
            onReply={(text) => void reply(q.seq, text, `a${q.seq}`)}
          />
        ))}
      </Section>
      <Section id="gates" title="Waiting for your approval" n={gates.length}>
        {gates.map((g) => (
          <GateCard
            key={g.id}
            g={g}
            k={`g${g.id}`}
            hidden={hiddenKeys.has(`g${g.id}`)}
            note={notes.get(g.id)}
            busy={btnBusy === `ap:${g.id}`}
            task={{ id: g.id, href: taskH(g.id), title: g.title || titleOf(g.id) }}
            onOpenTask={onOpenTask}
            onApprove={() => void approve(g.id)}
            onOpenNote={() => openNote(g.id)}
            onCloseNote={(refocus) => closeNote(g.id, refocus)}
            onNoteText={(text) =>
              setNotes((m) => {
                const next = new Map(m);
                next.set(g.id, text);
                return next;
              })
            }
          />
        ))}
      </Section>
      <Section id="paused" title="Paused" n={paused.length}>
        {paused.map((p) => (
          <PauseCard
            key={p.name}
            p={p}
            k={`p${p.name}`}
            hidden={hiddenKeys.has(`p${p.name}`)}
            now={now}
            href={sessH(p.name)}
            busy={btnBusy === `rs:${p.name}`}
            onOpenSession={onOpenSession}
            onResume={() => void resume(p.name)}
            onCompose={onCompose}
          />
        ))}
      </Section>
      <Section id="blocked" title="Blocked" n={blocked.length}>
        {blocked.map((b) => (
          <BlockedCard
            key={b.id}
            b={b}
            k={`b${b.id}`}
            hidden={hiddenKeys.has(`b${b.id}`)}
            task={{ id: b.id, href: taskH(b.id), title: b.title || titleOf(b.id) }}
            waitsOn={b.waits_on ?? []}
            owners={reassignOwners(state.sessions?.sessions, b.owner)}
            hrefOf={taskH}
            titleOf={titleOf}
            onOpenTask={onOpenTask}
            onUnblock={() => void unblock(b.id)}
            onReassign={(to) => void reassign(b.id, to)}
          />
        ))}
      </Section>
    </div>
  );
}
