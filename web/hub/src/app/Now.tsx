// Now.tsx — the registry's screens: the Now list (what needs the owner first, then the sessions
// at work and the idle ones, grouped by machine or project; picking several to send new work to,
// and the needs-you card's deny and snooze) and the Session screen (the header with its chips and
// context bar, the stream of the session's events, and the action bar its open ask carries). One
// tree serves the three layouts: the phone paints the list or the session, the tablet and the
// desktop paint the list beside the session, and the desktop adds the context pane (the linked
// sessions and the session's facts). Session text is rendered as text, never HTML (SPEC 9).
import type { Tone } from "@huddle/ui/badge.tsx";
import { Dialog } from "@huddle/ui/dialog.tsx";
import { modelColor } from "@huddle/ui/model-color.ts";
import { stampMs } from "@huddle/ui/time.ts";
import { type ComponentChildren, Fragment, type JSX } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type { Api, ApiError } from "../api.ts";
import { Icon } from "../icons.tsx";
import type { ChannelLinks } from "../links.ts";
import {
  ageText,
  type GroupMode,
  groupSessions,
  machinesLine,
  machinesOf,
  openItemOf,
  permissionOf,
  projectName,
  type RegistrySession,
  type RegistryTeammate,
  type StreamItem,
  sessionName,
  shortModel,
  stateWord,
  streamItems,
} from "../registry.ts";
import { readPref, writePref } from "../storage.ts";
import { ConnectCard } from "./ConnectCard.tsx";
import { errText, useHuddle } from "./context.tsx";
import { LinkSheet } from "./LinkSheet.tsx";
import type { Layout } from "./layout.ts";
import {
  ActComposer,
  confirmWhat,
  type NeedsAct,
  NeedsBar,
  SnoozeMenu,
  useNeedsAct,
  useStepUp,
} from "./Needs.tsx";
import { RadarDayCard } from "./RadarDay.tsx";
import { MachinesLine } from "./TopBar.tsx";

/** One state's mark: its shape and its colour class around the svg it draws. */
type Mark = { shape: string; cls: string; draw: JSX.Element };

/** The marks, keyed by the registry's state name. The word the aria-label carries is part of
 *  the signal, so colour is never the only difference. */
const MARKS: Record<string, Mark> = {
  "needs-you": {
    shape: "ring",
    cls: "text-warning",
    draw: <circle cx="12" cy="12" r="8" fill="none" stroke="currentColor" stroke-width="5" />,
  },
  working: {
    shape: "dot",
    cls: "text-success",
    draw: <circle cx="12" cy="12" r="9" fill="currentColor" />,
  },
  idle: {
    shape: "hollow",
    cls: "text-base-content/50",
    draw: <circle cx="12" cy="12" r="8" fill="none" stroke="currentColor" stroke-width="2.5" />,
  },
  stuck: {
    shape: "dash",
    cls: "text-base-content/50",
    draw: <path d="M6 12h12" fill="none" stroke="currentColor" stroke-width="3.5" stroke-linecap="round" />,
  },
  ended: {
    shape: "cross",
    cls: "text-base-content/40",
    draw: (
      <path
        d="M7 7l10 10M17 7L7 17"
        fill="none"
        stroke="currentColor"
        stroke-width="3.5"
        stroke-linecap="round"
      />
    ),
  },
};

/** The mark an unknown state falls back to: the idle hollow circle. */
const HOLLOW: Mark = {
  shape: "hollow",
  cls: "text-base-content/50",
  draw: <circle cx="12" cy="12" r="8" fill="none" stroke="currentColor" stroke-width="2.5" />,
};

/** A session's state as one 10 px mark: shape plus word, so the state never rides on colour
 *  alone. An unknown state draws the hollow mark and says its own name. */
export function StateMark({
  state,
  class: c = "",
}: {
  state: string;
  class?: string | undefined;
}): JSX.Element {
  const m = MARKS[state] ?? HOLLOW;
  return (
    <svg
      data-shape={m.shape}
      class={`size-2.5 shrink-0 ${m.cls} ${c}`.trim()}
      viewBox="0 0 24 24"
      role="img"
      aria-label={stateWord(state)}
    >
      {m.draw}
    </svg>
  );
}

/** The status tone each state's badge takes: needs-you asks for a look, working is healthy,
 *  stuck is broken; idle, ended and an unknown state stay neutral. */
const STATE_TONES: Record<string, Tone> = { "needs-you": "warn", working: "ok", stuck: "fail" };

/** A session's state as one word in a status badge: the text the mark's shape stands for, so
 *  the state never rides on colour or shape alone. */
export function StateChip({ state }: { state: string }): JSX.Element {
  return (
    <span data-state={state} class={`ui-badge ui-badge-${STATE_TONES[state] ?? "neutral"} shrink-0`}>
      {stateWord(state)}
    </span>
  );
}

/** A model as its chip: the short name in the page's own text colour, the family's colour as
 *  the chip's leading edge (a fill behind the text fails contrast on the lighter families),
 *  the raw id on hover. */
export function ModelChip({
  model,
  pill = false,
}: {
  model: string;
  /** The session header's round pill; the list's rows carry the small square chip. */
  pill?: boolean | undefined;
}): JSX.Element {
  return (
    <span
      class={`inline-flex items-center whitespace-nowrap border hairline bg-base-200 font-mono text-[11px] leading-4 text-base-content ${
        pill ? "rounded-full px-2.5 py-[3px] text-xs" : "rounded-md px-1.5 py-px"
      }`}
      style={`border-left:4px solid ${modelColor(model)}`}
      title={model}
    >
      {shortModel(model)}
    </span>
  );
}

/** The context bar: how full the session's window is, one primary fill over a quiet track.
 *  Nothing at zero — a session that never said reads as unknown, not empty. */
export function ContextBar({
  pct,
  class: c = "",
}: {
  pct: number;
  class?: string | undefined;
}): JSX.Element | null {
  if (!pct) return null;
  const n = Math.round(pct);
  return (
    <div
      class={`h-1.5 w-full overflow-hidden rounded-full bg-base-content/15 ${c}`.trim()}
      role="img"
      aria-label={`Context ${n} percent used`}
    >
      <div class="h-full rounded-full bg-primary" style={`width:${n}%`} />
    </div>
  );
}

/** A row's where: machine · project · branch, the parts that exist joined by middots. */
const whereOf = (s: RegistrySession): string =>
  [s.machine, projectName(s.project), s.branch].filter(Boolean).join(" · ");

/** A session's harness as its small badge: the coding agent a non-Claude session runs in. */
function HarnessBadge({ harness }: { harness: string }): JSX.Element {
  return (
    <span
      data-harness={harness}
      title={`This session runs in ${harness}`}
      class="shrink-0 rounded-md border hairline px-1 py-px font-mono text-[11px] leading-4 text-base-content/70"
    >
      {harness}
    </span>
  );
}

/** The link that opens one session from the list. */
const openHref = (s: RegistrySession): string => `#/now/${s.session_id}`;

/** The needs-you card: the session that waits on the owner, what it asks, and the answers at
 *  the card's foot, 48 px tall for one thumb. A permission ask offers Deny and Allow once, both
 *  posted at once (an allow may open the passkey sheet first); any other ask offers Review,
 *  which opens the session to answer it. The menu beside them snoozes or dismisses the item. */
function NeedsCard({
  s,
  now,
  sel,
  act,
}: {
  s: RegistrySession;
  now: number;
  sel: boolean;
  act: NeedsAct;
}): JSX.Element {
  const { registry } = useHuddle();
  const needs = registry.getState().needs;
  const ask = permissionOf(needs, s.session_id);
  const item = openItemOf(needs, s.session_id);
  return (
    <article
      class={`ui-card flex flex-col ${sel ? "ring-2 ring-primary" : ""}`}
      data-row={s.session_id}
      aria-current={sel ? "true" : undefined}
    >
      <a href={openHref(s)} class="flex min-w-0 flex-col gap-1.5 p-4 text-base-content">
        <div class="flex items-baseline justify-between gap-2">
          <span class="flex min-w-0 items-center gap-1.5">
            <span class="truncate text-base font-semibold">{sessionName(s)}</span>
            {s.harness ? <HarnessBadge harness={s.harness} /> : null}
          </span>
          <span class="flex shrink-0 items-center gap-1.5">
            <StateChip state={s.state} />
            <span class="text-xs muted">{ageText(s.last_event_at, now)}</span>
          </span>
        </div>
        <div class="truncate text-xs muted">{whereOf(s)}</div>
        <div class="line-clamp-2 text-sm">{s.task || s.title || stateWord(s.state)}</div>
      </a>
      {ask ? (
        <div class="flex min-w-0 flex-col gap-1.5 px-4 pb-3" data-ask="">
          <span class="text-sm font-semibold">
            Allow <code class="font-mono">{ask.tool}</code>?
          </span>
          {ask.input ? (
            <code class="block overflow-x-auto whitespace-pre rounded-[10px] border hairline bg-base-300 px-3 py-2 font-mono text-[13px]">
              {ask.input}
            </code>
          ) : null}
        </div>
      ) : null}
      <div class="flex gap-2 border-t hairline p-3" data-needs-actions="">
        {item ? <SnoozeMenu item={item} /> : null}
        {ask ? (
          <>
            <button
              type="button"
              data-act="deny"
              onClick={() => act.deny(ask)}
              class="ui-btn ui-btn-danger ui-btn-lg flex-1"
            >
              Deny
            </button>
            <button
              type="button"
              data-act="allow"
              onClick={() => act.allow(ask, confirmWhat(ask, s, true))}
              class="ui-btn ui-btn-primary ui-btn-lg flex-1"
            >
              <Icon name="key" class="size-4" />
              Allow once
            </button>
          </>
        ) : (
          <a href={openHref(s)} data-act="review" class="ui-btn ui-btn-primary ui-btn-lg flex-1">
            Review
          </a>
        )}
      </div>
    </article>
  );
}

/** The selected row's ring: the desktop's keyboard selection. */
const selRing = (sel: boolean): string => (sel ? " ring-2 ring-inset ring-primary" : "");

/** A working or stuck row: the mark, the name taking the row's free width, where it works and
 *  its task under it, and the state badge, the model chip and how full its window is after. */
function WorkRow({ s, sel }: { s: RegistrySession; sel: boolean }): JSX.Element {
  return (
    <a
      href={openHref(s)}
      data-row={s.session_id}
      aria-current={sel ? "true" : undefined}
      class={`ui-list-row${selRing(sel)}`}
    >
      <span class="ui-list-leading">
        <StateMark state={s.state} />
      </span>
      <span class="ui-list-text">
        <span class="flex min-w-0 items-center gap-1.5">
          <span class="ui-list-title min-w-0">{sessionName(s)}</span>
          {s.harness ? <HarnessBadge harness={s.harness} /> : null}
        </span>
        <span class="ui-list-meta truncate">{[s.machine, s.task].filter(Boolean).join(" · ")}</span>
      </span>
      <span class="ui-list-trailing flex-col items-end gap-1">
        <span class="flex items-center gap-1.5">
          <StateChip state={s.state} />
          {s.model ? <ModelChip model={s.model} /> : null}
        </span>
        <span class="text-xs muted tnum">{s.context_percent ? `${Math.round(s.context_percent)}%` : ""}</span>
      </span>
    </a>
  );
}

/** An idle or ended row: the mark, the name taking the row's free width with its machine, the
 *  state badge after it, and how long it has been quiet. */
function IdleRow({ s, now, sel }: { s: RegistrySession; now: number; sel: boolean }): JSX.Element {
  return (
    <a
      href={openHref(s)}
      data-row={s.session_id}
      aria-current={sel ? "true" : undefined}
      class={`ui-list-row${selRing(sel)}`}
    >
      <span class="ui-list-leading">
        <StateMark state={s.state} />
      </span>
      <span class="ui-list-text">
        <span class="flex min-w-0 items-center gap-1.5">
          <span class="ui-list-title min-w-0">
            {sessionName(s)}{" "}
            {s.machine ? <span class="text-[13px] font-normal muted">· {s.machine}</span> : null}
          </span>
          {s.harness ? <HarnessBadge harness={s.harness} /> : null}
        </span>
      </span>
      <span class="ui-list-trailing gap-1.5">
        <StateChip state={s.state} />
        <span class="text-xs muted">{ageText(s.last_event_at, now)}</span>
      </span>
    </a>
  );
}

/** A lead's agent-team teammates under its row: each name with its state word. */
function TeammateList({ teammates }: { teammates: RegistryTeammate[] }): JSX.Element {
  return (
    <ul class="m-0 flex list-none flex-col gap-0.5 px-4 pb-2 pl-10" aria-label="Teammates">
      {teammates.map((m) => (
        <li key={m.name} data-teammate={m.name} class="flex min-w-0 items-center gap-1.5 text-[13px] muted">
          <span class="min-w-0 flex-1 truncate">{m.name}</span>
          <StateChip state={m.state} />
        </li>
      ))}
    </ul>
  );
}

/** The list's five sections, in the order the Now tab shows them; empty ones draw nothing. */
const SECTIONS: readonly (readonly [
  key: "needsYou" | "working" | "idle" | "stuck" | "ended",
  label: string,
  warn: boolean,
])[] = [
  ["needsYou", "Needs you", true],
  ["working", "Working", false],
  ["idle", "Idle", false],
  ["stuck", "Stuck", false],
  ["ended", "Ended", false],
];

/** One row's shell: the desktop carries a checkbox beside it (the pick a send to many sends to),
 *  the phone hears a long-press for the same. A tap toggles the row only while a pick is on. */
function RowShell({
  layout,
  s,
  picked,
  tap,
  onPick,
  onHoldStart,
  onHoldStop,
  children,
}: {
  layout: Layout;
  s: RegistrySession;
  picked: boolean;
  tap: (e: MouseEvent) => void;
  onPick: () => void;
  onHoldStart: () => void;
  onHoldStop: () => void;
  children: ComponentChildren;
}): JSX.Element {
  if (layout === "desktop") {
    return (
      // biome-ignore lint/a11y/noStaticElementInteractions: the row's link keeps the keyboard; the click only toggles a pick
      // biome-ignore lint/a11y/useKeyWithClickEvents: the row's link keeps the keyboard; the click only toggles a pick
      <div class="flex items-start gap-1 pl-3" data-select-row={s.session_id} onClick={tap}>
        <input
          type="checkbox"
          class="checkbox checkbox-xs mt-5 shrink-0"
          aria-label={`Select ${sessionName(s)}`}
          checked={picked}
          onClick={(e) => e.stopPropagation()}
          onChange={onPick}
        />
        <div class="min-w-0 flex-1">{children}</div>
      </div>
    );
  }
  return (
    // biome-ignore lint/a11y/noStaticElementInteractions: the phone's long-press picks a row for a send
    // biome-ignore lint/a11y/useKeyWithClickEvents: the row's link keeps the keyboard; the click only toggles a pick
    <div
      data-hold-row={s.session_id}
      onClick={tap}
      onPointerDown={onHoldStart}
      onPointerUp={onHoldStop}
      onPointerLeave={onHoldStop}
      onPointerMove={onHoldStop}
    >
      {children}
    </div>
  );
}

/** The Now list: the registry's sessions as the three layouts' list pane. The tablet carries its
 *  own header (the phone's shell and the desktop's bar draw theirs); the group-by toggle rides
 *  under it; the desktop's j and k move a selection Enter opens and a, d and r answer, the
 *  checkboxes pick sessions to send new work to, and the phone's long-press picks the same.
 *  `above` rides at the top of the list (the first-run checklist the shell hands down). */
export function NowPane({
  layout,
  above = null,
}: {
  layout: Layout;
  above?: ComponentChildren;
}): JSX.Element {
  const { registry, now } = useHuddle();
  const act = useNeedsAct();
  const st = registry.getState();
  const groups = groupSessions(st.sessions);
  const [group, setGroup] = useState<GroupMode>(() =>
    readPref(localStorage, "nowgroup", "machine" as GroupMode),
  );
  // the needs-you filter: on, the list holds the sessions that need the owner and nothing else
  const [onlyNeeds, setOnlyNeeds] = useState<boolean>(() => readPref(localStorage, "nowneeds", false));
  const [selId, setSelId] = useState<string | null>(null);
  const [picked, setPicked] = useState<readonly string[]>([]);
  const [replyTo, setReplyTo] = useState<string | null>(null);
  const [sending, setSending] = useState(false);
  const [linking, setLinking] = useState(false);
  const hold = useRef<number | null>(null);

  const rows = onlyNeeds
    ? groups.needsYou
    : [...groups.needsYou, ...groups.working, ...groups.idle, ...groups.stuck, ...groups.ended];
  const selIdx = rows.findIndex((r) => r.session_id === selId);
  const cur = selIdx >= 0 ? selIdx : 0;

  const stopHold = (): void => {
    if (hold.current !== null) window.clearTimeout(hold.current);
    hold.current = null;
  };
  const startHold = (id: string): void => {
    stopHold();
    hold.current = window.setTimeout(() => {
      hold.current = null;
      setPicked((picks) => (picks.includes(id) ? picks : [...picks, id]));
    }, 500);
  };
  // no dependency list: the pane repaints on every change, and the cleanup rides along
  useEffect(() => () => stopHold());

  const togglePick = (id: string): void =>
    setPicked((picks) => (picks.includes(id) ? picks.filter((x) => x !== id) : [...picks, id]));
  /** A row's tap does nothing while nobody is picked; with a pick on, it toggles the row. */
  const onRowTap =
    (id: string) =>
    (e: MouseEvent): void => {
      if (picked.length === 0) return;
      e.preventDefault();
      togglePick(id);
    };
  const routeOf = (id: string): string | undefined =>
    st.sessions?.find((s) => s.session_id === id)?.delivery_route;
  const replyRow = replyTo ? st.sessions?.find((s) => s.session_id === replyTo) : undefined;

  // no dependency list: the pane repaints on every change, and the handler reads rows fresh
  useEffect(() => {
    if (layout !== "desktop") return;
    // oxlint-disable-next-line complexity -- one guard per key the list owns
    const on = (e: KeyboardEvent): void => {
      if (e.metaKey || e.ctrlKey || e.altKey) return;
      if (document.querySelector("dialog[open]")) return;
      const t = e.target instanceof HTMLElement ? e.target : null;
      if (t?.closest("input, textarea, select, [contenteditable]")) return;
      if (e.key === "L") {
        // linking needs the pick of two or more; anything less leaves the bar to do the talking
        if (picked.length < 2) return;
        e.preventDefault();
        setLinking(true);
        return;
      }
      if (
        e.key !== "j" &&
        e.key !== "k" &&
        e.key !== "Enter" &&
        e.key !== "a" &&
        e.key !== "d" &&
        e.key !== "r"
      )
        return;
      if (rows.length === 0) return;
      e.preventDefault();
      if (e.key === "Enter") {
        const s = rows[cur];
        if (s) location.hash = openHref(s);
        return;
      }
      if (e.key === "j" || e.key === "k") {
        const next = Math.min(rows.length - 1, Math.max(0, cur + (e.key === "j" ? 1 : -1)));
        setSelId(rows[next]?.session_id ?? null);
        return;
      }
      const s = rows[cur];
      if (!s) return;
      if (e.key === "r") {
        setReplyTo(s.session_id);
        return;
      }
      const ask = permissionOf(st.needs, s.session_id);
      if (!ask) return;
      if (e.key === "a") act.allow(ask);
      else act.deny(ask);
    };
    window.addEventListener("keydown", on);
    return () => window.removeEventListener("keydown", on);
  });

  /** One group's rows: the needs-you cards stacked, every other state's rows in one list. */
  const rowsOf = (list: RegistrySession[], label: string): JSX.Element =>
    list[0]?.state === "needs-you" ? (
      <div class="flex flex-col gap-3">{list.map((s) => rowOf(s))}</div>
    ) : (
      <ul class="ui-list" aria-label={label}>
        {list.map((s) => (
          <li key={s.session_id} class="ui-list-item">
            {rowOf(s)}
          </li>
        ))}
      </ul>
    );

  /** One section's rows, each group under a sub-heading when the toggle groups them. */
  const sectionRows = (list: RegistrySession[], label: string): JSX.Element => {
    if (group === "machine" && new Set(list.map((s) => s.machine ?? "")).size <= 1) {
      // one machine needs no sub-heading: the rows already carry it
      return rowsOf(list, label);
    }
    const byKey = new Map<string, RegistrySession[]>();
    for (const s of list) {
      const key = group === "machine" ? s.machine || "No machine" : projectName(s.project) || "No project";
      byKey.set(key, [...(byKey.get(key) ?? []), s]);
    }
    return (
      <>
        {[...byKey.entries()].map(([key, keyed]) => (
          <div key={key} class="flex flex-col gap-1.5">
            <h3 class="m-0 px-1 pt-1 text-xs font-medium muted">
              {key} · {keyed.length}
            </h3>
            {rowsOf(keyed, `${label}, ${key}`)}
          </div>
        ))}
      </>
    );
  };

  /** One row, as its group draws it, in the shell its layout gives it. */
  const rowOf = (s: RegistrySession): JSX.Element => {
    const sel = layout === "desktop" && rows[cur]?.session_id === s.session_id;
    const row =
      s.state === "needs-you" ? (
        <NeedsCard s={s} now={now} sel={sel} act={act} />
      ) : s.state === "idle" || s.state === "ended" ? (
        <IdleRow s={s} now={now} sel={sel} />
      ) : (
        <WorkRow s={s} sel={sel} />
      );
    return (
      <RowShell
        key={s.session_id}
        layout={layout}
        s={s}
        picked={picked.includes(s.session_id)}
        tap={onRowTap(s.session_id)}
        onPick={() => togglePick(s.session_id)}
        onHoldStart={() => startHold(s.session_id)}
        onHoldStop={stopHold}
      >
        {row}
        {s.teammates?.length ? <TeammateList teammates={s.teammates} /> : null}
      </RowShell>
    );
  };

  /** The sections in order, each heading over its rows, with the Radar card after the asks. */
  const sections = (): JSX.Element => {
    // the Radar card rides after the needs-you cards, before the first other section; the
    // needs-you filter holds the asks alone
    const firstOther = onlyNeeds
      ? null
      : (SECTIONS.find(([key]) => key !== "needsYou" && groups[key].length > 0)?.[0] ?? null);
    const radar = (
      <div class="px-3 pt-3">
        <RadarDayCard />
      </div>
    );
    return (
      <>
        {SECTIONS.filter(([key]) => (!onlyNeeds || key === "needsYou") && groups[key].length > 0).map(
          ([key, label, warn]) => (
            <Fragment key={key}>
              {/* the Radar card follows what needs the owner, or leads when nothing does */}
              {key === firstOther ? radar : null}
              <section aria-labelledby={`now-${key}`} class="flex flex-col gap-2 px-3 pt-3">
                <h2
                  id={`now-${key}`}
                  class={`m-0 flex items-center gap-2 px-1 text-[13px] font-semibold tracking-[0.06em] uppercase ${
                    warn ? "text-[var(--ui-warn-ink)]" : "muted"
                  }`}
                >
                  {warn ? <StateMark state="needs-you" /> : null}
                  {label} · {groups[key].length}
                </h2>
                {sectionRows(groups[key], label)}
              </section>
            </Fragment>
          ),
        )}
        {!onlyNeeds && firstOther === null ? radar : null}
      </>
    );
  };

  return (
    <div class="flex min-h-0 min-w-0 flex-col" data-now-pane="">
      {layout === "tablet" ? (
        <header class="flex shrink-0 items-center justify-between px-4 pt-5 pb-3">
          <h1 class="text-2xl font-semibold tracking-tight">Now</h1>
          <MachinesLine live={st.live} text={machinesLine(layout, machinesOf(st.sessions))} />
        </header>
      ) : null}
      <div class="flex shrink-0 items-center justify-between gap-2 border-b hairline px-4 py-2">
        <div class="flex items-center gap-2">
          <span class="text-xs muted">Group by</span>
          <div class="flex overflow-hidden rounded-lg border hairline">
            {(["machine", "project"] as const).map((k, i) => (
              <button
                key={k}
                type="button"
                aria-pressed={group === k ? "true" : "false"}
                class={`min-h-[44px] px-3 text-xs outline-none focus-visible:ring-2 focus-visible:ring-primary ${
                  i > 0 ? "border-l hairline" : ""
                } ${group === k ? "bg-base-100 font-semibold text-base-content" : "muted"}`}
                onClick={() => {
                  setGroup(k);
                  writePref(localStorage, "nowgroup", k);
                }}
              >
                {k === "machine" ? "Machine" : "Project"}
              </button>
            ))}
          </div>
        </div>
        <button
          type="button"
          data-needs-filter=""
          aria-pressed={onlyNeeds ? "true" : "false"}
          class={`min-h-[44px] rounded-lg border px-3 text-xs outline-none focus-visible:ring-2 focus-visible:ring-primary ${
            onlyNeeds
              ? "border-warning bg-base-100 font-semibold text-[var(--ui-warn-ink)]"
              : "hairline muted"
          }`}
          onClick={() => {
            const next = !onlyNeeds;
            setOnlyNeeds(next);
            writePref(localStorage, "nowneeds", next);
          }}
        >
          Needs you
        </button>
      </div>
      <div class="min-h-0 flex-1 overflow-y-auto pb-4" data-now-sections="">
        {above}
        {st.sessions === null ? (
          <div class="grid gap-2 px-3 pt-3" role="status" aria-label="Reading the registry">
            {[0, 1, 2, 3].map((row) => (
              <div key={row} class="skeleton h-12" aria-hidden="true" />
            ))}
          </div>
        ) : rows.length === 0 ? (
          onlyNeeds ? (
            <div class="mx-auto m-4 flex max-w-lg flex-col items-center gap-2 rounded-box border border-dashed border-base-content/20 px-6 py-16 text-center text-sm muted">
              <b class="text-base font-semibold text-base-content">Nothing needs you</b>
              <p>When a session needs you, it shows up here first.</p>
            </div>
          ) : layout === "phone" ? (
            <div class="m-4">
              <NoSessionsWelcome />
            </div>
          ) : null
        ) : (
          sections()
        )}
      </div>
      {picked.length > 0 ? (
        <PickBar
          n={picked.length}
          onClear={() => setPicked([])}
          onSend={() => setSending(true)}
          onLink={() => setLinking(true)}
        />
      ) : null}
      {layout === "desktop" ? (
        <div class="flex shrink-0 gap-3.5 border-t hairline p-2.5 text-xs text-base-content/50">
          <span>
            <kbd class="kbd kbd-xs">j</kbd> <kbd class="kbd kbd-xs">k</kbd> move
          </span>
          <span>
            <kbd class="kbd kbd-xs">a</kbd> allow <kbd class="kbd kbd-xs">d</kbd> deny{" "}
            <kbd class="kbd kbd-xs">r</kbd> reply
          </span>
        </div>
      ) : null}
      {act.sheet}
      <NowDialogs
        replyTo={replyTo}
        replyRow={replyRow}
        sending={sending}
        picked={picked}
        routeOf={routeOf}
        onReplyClose={() => setReplyTo(null)}
        onSendClose={() => {
          setSending(false);
          setPicked([]);
        }}
      />
      {linking ? (
        <LinkSheet sessions={picked} onClose={() => setLinking(false)} onLinked={() => setPicked([])} />
      ) : null}
    </div>
  );
}

/** The welcome an empty registry shows: one heading, one sentence, and the start-a-session
 *  card. The phone paints it in the list (the list is the whole screen); the tablet and the
 *  desktop paint it in the main pane, where a picked session's screen stands. */
export function NoSessionsWelcome(): JSX.Element {
  return (
    <div class="flex flex-col gap-4" data-no-sessions="">
      <div>
        <h2 class="text-lg font-semibold tracking-tight">No sessions yet</h2>
        <p class="m-0 text-sm muted">
          Bring a coding agent in: install, join, and it reports here with everything it does.
        </p>
      </div>
      <ConnectCard ch="" invite={false} />
    </div>
  );
}

/** The pick bar: how many rows are picked, the clear, the link of two or more into a channel,
 *  and the send the composer opens. */
function PickBar({
  n,
  onClear,
  onSend,
  onLink,
}: {
  n: number;
  onClear: () => void;
  onSend: () => void;
  onLink: () => void;
}): JSX.Element {
  return (
    <div class="flex shrink-0 items-center gap-2 border-t hairline px-3 py-2" data-select-bar="">
      <span class="text-sm muted">{n} selected</span>
      <span class="flex-1" />
      <button type="button" class="btn btn-ghost btn-sm" onClick={onClear}>
        Clear
      </button>
      {n >= 2 ? (
        <button type="button" class="btn btn-ghost btn-sm" onClick={onLink}>
          Link {n} sessions
        </button>
      ) : null}
      <button type="button" class="btn btn-primary btn-sm" onClick={onSend}>
        Send to {n} {n === 1 ? "session" : "sessions"}
      </button>
    </div>
  );
}

/** The list's two dialogs: the reply one row's composer answers, and the send to every picked
 *  session. Both close on their own Cancel; a send closes the dialog and drops the picks. */
function NowDialogs({
  replyTo,
  replyRow,
  sending,
  picked,
  routeOf,
  onReplyClose,
  onSendClose,
}: {
  replyTo: string | null;
  replyRow: RegistrySession | undefined;
  sending: boolean;
  picked: readonly string[];
  routeOf: (id: string) => string | undefined;
  onReplyClose: () => void;
  onSendClose: () => void;
}): JSX.Element {
  return (
    <>
      <Dialog
        open={replyTo !== null}
        onClose={onReplyClose}
        title={replyRow ? `Reply to ${sessionName(replyRow)}` : "Reply"}
      >
        {replyTo && replyRow ? (
          <ActComposer
            kind="answer"
            targets={[replyRow.session_id]}
            routes={[replyRow.delivery_route]}
            placeholder={`Reply to ${sessionName(replyRow)}…`}
            onClose={onReplyClose}
          />
        ) : null}
      </Dialog>
      <Dialog open={sending} onClose={onSendClose} title={`Send to ${picked.length} sessions`}>
        {sending ? (
          <ActComposer
            kind="send"
            targets={picked}
            routes={picked.map((id) => routeOf(id))}
            placeholder="What should they do?"
            onClose={onSendClose}
          />
        ) : null}
      </Dialog>
    </>
  );
}

/** The session's chips: its state (mark and word), its model, and its channel when it is in one. */
function Chips({ s }: { s: RegistrySession }): JSX.Element {
  return (
    <span class="flex min-w-0 flex-wrap items-center gap-1.5">
      <span class="inline-flex items-center gap-1.5 rounded-full border border-warning px-2.5 py-0.5 text-xs text-warning">
        <StateMark state={s.state} />
        {stateWord(s.state)}
      </span>
      {s.model ? <ModelChip model={s.model} pill /> : null}
      {s.channel ? (
        <span class="rounded-full border hairline px-2.5 py-0.5 text-xs muted">#{s.channel}</span>
      ) : null}
      {s.harness ? (
        <span
          data-harness={s.harness}
          class="rounded-full border hairline px-2.5 py-0.5 font-mono text-xs text-base-content/70"
        >
          {s.harness}
        </span>
      ) : null}
    </span>
  );
}

/** One timestamp's wall-clock time, always 24 h, whatever the browser's locale spells. */
function hhmm(ms: number): string {
  const d = new Date(ms);
  return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
}

/** One stream item's day, the way the design's dividers spell it: Today, Yesterday or the date,
 *  with the item's time. */
function dayLabel(at: string, now: number): string {
  const ms = stampMs(at);
  if (ms === null) return "";
  const d = new Date(ms);
  const day = new Date(now);
  const sameDay = (a: Date, b: Date): boolean =>
    a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate();
  const yesterday = new Date(day);
  yesterday.setDate(day.getDate() - 1);
  if (sameDay(d, day)) return `Today ${hhmm(ms)}`;
  if (sameDay(d, yesterday)) return `Yesterday ${hhmm(ms)}`;
  return `${d.toLocaleDateString(undefined, { month: "short", day: "numeric" })} ${hhmm(ms)}`;
}

/** One item of the session's stream: a prompt as a right bubble, a tool call as one line with
 *  its mark (a check once done, an hourglass and the word while running), its tool and its
 *  target, anything else that carries text as a plain paragraph. */
function StreamPart({ item }: { item: StreamItem }): JSX.Element {
  if (item.kind === "prompt") {
    return (
      <div class="max-w-[85%] self-end rounded-[14px_14px_4px_14px] bg-base-content/15 px-3 py-2.5 break-words">
        {item.text}
      </div>
    );
  }
  if (item.kind === "tool") {
    return (
      <div
        class="flex items-center gap-2 text-[13px] muted"
        data-tool=""
        data-running={item.running ? "" : undefined}
      >
        {item.running ? (
          <Icon name="hourglass" class="size-4 shrink-0" />
        ) : (
          <Icon name="check" class="size-4 shrink-0 text-success" />
        )}
        <span>{item.tool}</span>
        {item.target ? <code class="min-w-0 truncate font-mono text-xs">{item.target}</code> : null}
        {item.running ? <span class="shrink-0 text-xs">running</span> : null}
      </div>
    );
  }
  return <div class="break-words whitespace-pre-wrap">{item.text}</div>;
}

/** One act the session's menu can run on a linked peer: a brief carries this session's newest
 *  digest into the peer, a hand over also moves its open tasks and leaves the channel (SPEC 8). */
type LinkAct = "brief" | "handover";

/** Reads the links list once; a failed read reads as none, so a server hiccup only hides the menu. */
async function readLinks(api: Api): Promise<{ channels?: ChannelLinks[] }> {
  try {
    return (await api.api("/api/links")) as { channels?: ChannelLinks[] };
  } catch {
    return {};
  }
}

/** The sessions that share a channel with this one, deduped, each with its display name. */
function linkedPeersOf(
  session: string,
  channels: ChannelLinks[] | null,
  nameOf: (id: string) => string,
): { id: string; name: string }[] {
  const out: { id: string; name: string }[] = [];
  for (const ch of channels ?? []) {
    if (!ch.links.some((l) => l.session_id === session)) continue;
    for (const l of ch.links) {
      if (l.session_id !== session && !out.some((c) => c.id === l.session_id)) {
        out.push({ id: l.session_id, name: nameOf(l.session_id) });
      }
    }
  }
  return out;
}

/** The act's own dialog: the target picked among the linked peers, and what the act would do. */
function LinkActDialog({
  act,
  to,
  candidates,
  busy,
  onTo,
  onClose,
  onRun,
}: {
  act: LinkAct;
  to: string;
  candidates: readonly { id: string; name: string }[];
  busy: boolean;
  onTo: (id: string) => void;
  onClose: () => void;
  onRun: () => void;
}): JSX.Element {
  return (
    <Dialog
      open
      onClose={onClose}
      title={act === "handover" ? "Hand over to another session" : "Brief another session"}
    >
      <div class="flex flex-col gap-3">
        <select
          class="select"
          aria-label="Target session"
          value={to}
          onChange={(e) => onTo(e.currentTarget.value)}
        >
          {candidates.map((c) => (
            <option key={c.id} value={c.id}>
              {c.name}
            </option>
          ))}
        </select>
        <p class="m-0 text-xs muted">
          {act === "handover"
            ? "Briefs the session you pick, moves this session's open tasks to it and leaves this session out of the channel."
            : "Sends this session's newest digest to the session you pick, on its next prompt."}
        </p>
        <div class="flex justify-end gap-2">
          <button type="button" class="btn btn-ghost btn-sm" onClick={onClose}>
            Cancel
          </button>
          <button type="button" class="btn btn-primary btn-sm" disabled={busy || to === ""} onClick={onRun}>
            {act === "handover" ? "Hand over" : "Brief"}
          </button>
        </div>
      </div>
    </Dialog>
  );
}

/** The linked session's actions (SPEC 8): `Brief another session…` and `Hand over to…`, their
 *  target picked among the sessions that share a channel with this one, whatever the channel —
 *  the one read of GET /api/links answers that. A session linked nowhere draws nothing. */
function SessionActions({ session }: { session: RegistrySession }): JSX.Element | null {
  const { api, toast, registry } = useHuddle();
  const { ask, sheet } = useStepUp();
  const [channels, setChannels] = useState<ChannelLinks[] | null>(null);
  const [menuOpen, setMenuOpen] = useState(false);
  const [act, setAct] = useState<LinkAct | null>(null);
  const [to, setTo] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let alive = true;
    void readLinks(api).then((r) => {
      if (alive) setChannels(r.channels ?? []);
    });
    return () => {
      alive = false;
    };
  }, [api]);

  const nameOf = (id: string): string => {
    const row = registry.getState().sessions?.find((s) => s.session_id === id);
    return row ? sessionName(row) : id;
  };
  const candidates = linkedPeersOf(session.session_id, channels, nameOf);
  if (channels === null || candidates.length === 0) return null;

  const post = (): Promise<unknown> =>
    api.api(act === "handover" ? "/api/handover" : "/api/brief", {
      body: { from: session.session_id, to },
    });
  const done = (r: { moved?: string[] }): void => {
    toast(
      act === "handover"
        ? `Handed over to ${nameOf(to)}: ${(r.moved ?? []).length} tasks moved`
        : `Briefed ${nameOf(to)}`,
    );
    setBusy(false);
    setAct(null);
  };
  // both acts carry this session's words or work into another (SPEC 7.4): a 401 runs the
  // passkey sheet once and posts the same body again
  const run = async (): Promise<void> => {
    setBusy(true);
    try {
      done(((await post()) as { moved?: string[] }) ?? {});
    } catch (e) {
      await runRetry(e as ApiError);
    }
  };
  const runRetry = async (e: ApiError): Promise<void> => {
    if (e.status !== 401 || e.message !== "step-up required") {
      toast(errText(e), { bad: true });
      setBusy(false);
      return;
    }
    if (!(await ask({ action: act === "handover" ? "Hand over" : "Brief", session: nameOf(to) }))) {
      setBusy(false);
      return;
    }
    try {
      const r = (await post()) as { moved?: string[] };
      done(r);
    } catch (e2) {
      toast(errText(e2), { bad: true });
      setBusy(false);
    }
  };
  const open = (kind: LinkAct): void => {
    setMenuOpen(false);
    setTo(candidates[0]?.id ?? "");
    setAct(kind);
  };

  return (
    <>
      {sheet}
      <span class="relative inline-flex" data-session-actions="">
        <button
          type="button"
          aria-label="More actions"
          aria-expanded={menuOpen ? "true" : "false"}
          class="flex size-9 items-center justify-center rounded-lg text-base-content/60 outline-none focus-visible:ring-2 focus-visible:ring-primary"
          onClick={() => setMenuOpen((v) => !v)}
        >
          <Icon name="more" class="size-4" />
        </button>
        {menuOpen ? (
          <div
            role="menu"
            aria-label="Link actions"
            class="absolute top-full right-0 z-20 mt-1 flex min-w-52 flex-col rounded-[10px] border hairline bg-base-100 p-1 shadow-lg"
          >
            <button
              type="button"
              role="menuitem"
              class="flex min-h-9 items-center rounded-md px-2.5 text-left text-sm outline-none hover:bg-base-200 focus-visible:bg-base-200"
              onClick={() => open("brief")}
            >
              Brief another session…
            </button>
            <button
              type="button"
              role="menuitem"
              class="flex min-h-9 items-center rounded-md px-2.5 text-left text-sm outline-none hover:bg-base-200 focus-visible:bg-base-200"
              onClick={() => open("handover")}
            >
              Hand over to…
            </button>
          </div>
        ) : null}
      </span>
      {act !== null ? (
        <LinkActDialog
          act={act}
          to={to}
          candidates={candidates}
          busy={busy}
          onTo={setTo}
          onClose={() => setAct(null)}
          onRun={() => void run()}
        />
      ) : null}
    </>
  );
}

/** The Session screen: the header (name, where, chips, context bar), the stream of the session's
 *  events, and the action bar its open needs-you ask carries (the phone's sheet, the wide
 *  layouts' inline box). The phone paints its own header with the back link; the tablet and the
 *  desktop carry theirs in a bar. */
export function SessionScreen({ id, layout }: { id: string; layout: Layout }): JSX.Element {
  const { registry, now } = useHuddle();
  const st = registry.getState();
  const d = st.open;

  if (!d) {
    const line = st.openMissing ? `There is no session ${id}.` : "Reading the session…";
    return (
      <div class="grid min-h-0 flex-1 place-items-center px-6">
        <p class="text-sm muted">{line}</p>
      </div>
    );
  }
  const ask = openItemOf(st.needs, id);

  const items = streamItems(d.events ?? []);
  let lastDay = "";
  const pct = d.context_percent ? Math.round(d.context_percent) : 0;

  return (
    <div class="flex min-h-0 min-w-0 flex-1 flex-col" data-session="">
      {layout === "phone" ? (
        <header class="flex shrink-0 flex-col gap-2.5 border-b hairline bg-base-200 px-3 pt-3.5 pb-3">
          <div class="flex items-center gap-1">
            <a
              href="#/now"
              aria-label="Back to Now"
              class="flex size-[var(--ui-tap)] shrink-0 items-center justify-center rounded-lg text-base-content outline-none focus-visible:ring-2 focus-visible:ring-primary"
            >
              <Icon name="left" class="size-[22px]" />
            </a>
            <div class="flex min-w-0 flex-1 flex-col gap-0.5">
              <div class="truncate text-lg font-semibold">{sessionName(d)}</div>
              <div class="truncate text-xs muted">{whereOf(d)}</div>
            </div>
            <SessionActions session={d} />
          </div>
          <div class="px-2">
            <Chips s={d} />
          </div>
          {pct ? (
            <div class="flex items-center gap-2.5 px-2">
              <ContextBar pct={pct} class="min-w-0 flex-1" />
              <span class="shrink-0 text-xs muted">{pct}% context</span>
            </div>
          ) : null}
        </header>
      ) : (
        <header class="flex shrink-0 flex-wrap items-center gap-3.5 border-b hairline px-6 pt-4 pb-3.5">
          <div class="flex min-w-0 flex-1 flex-col gap-0.5">
            <div class="truncate text-xl font-semibold">{sessionName(d)}</div>
            <div class="truncate text-[13px] muted">{whereOf(d)}</div>
          </div>
          <Chips s={d} />
          <SessionActions session={d} />
          {pct ? (
            <span class="flex w-[140px] shrink-0 items-center gap-2">
              <ContextBar pct={pct} />
              <span class="shrink-0 text-xs muted">{pct}%</span>
            </span>
          ) : null}
        </header>
      )}
      <div class="min-h-0 flex-1 overflow-y-auto px-4 py-3.5 sm:px-6" data-stream="">
        <div class="mx-auto flex max-w-[720px] flex-col gap-3 text-sm leading-relaxed sm:text-[15px]">
          {items.map((item, i) => {
            const parts: JSX.Element[] = [];
            const day = dayLabel(item.at, now);
            const dayKey = day.split(" ").slice(0, -1).join(" ");
            if (day && dayKey !== lastDay) {
              lastDay = dayKey;
              parts.push(
                <div key={`d${i}`} class="py-1 text-center text-xs text-base-content/50">
                  {day}
                </div>,
              );
            }
            parts.push(<StreamPart key={i} item={item} />);
            return <>{parts}</>;
          })}
          {items.length === 0 ? <p class="py-6 text-center text-sm muted">No events yet.</p> : null}
        </div>
      </div>
      {ask ? <NeedsBar item={ask} session={d} layout={layout} /> : null}
    </div>
  );
}

/** The desktop's context pane: the sessions linked with the open one, and the session's own
 *  facts. The digest arrives with the linking work; the cost shows when Radar knows the session. */
export function NowContext(): JSX.Element | null {
  const { registry, now } = useHuddle();
  const st = registry.getState();
  const d = st.open;
  if (!d) return null;
  const linked = (st.sessions ?? []).filter(
    (s) => s.channel && s.channel === d.channel && s.session_id !== d.session_id,
  );
  const startedAt = d.events?.[0]?.report.last_event_at ?? d.last_event_at;
  const startedMs = stampMs(startedAt);
  const started = startedMs === null ? "—" : `${hhmm(startedMs)} · ${ageText(startedAt, now)}`;
  return (
    <aside
      class="flex min-h-0 min-w-0 flex-col gap-4 overflow-y-auto border-l hairline bg-base-200 p-4"
      data-now-context=""
    >
      {d.channel ? (
        <section class="flex flex-col gap-2.5">
          <h2 class="text-xs font-semibold tracking-[0.06em] uppercase muted">Linked in #{d.channel}</h2>
          {linked.length === 0 ? (
            <p class="text-[13px] muted">No other session is linked here yet.</p>
          ) : (
            linked.map((s) => (
              <a
                key={s.session_id}
                href={openHref(s)}
                class="flex min-w-0 items-center gap-2.5 text-sm outline-none focus-visible:ring-2 focus-visible:ring-primary"
              >
                <StateMark state={s.state} />
                <span class="truncate">{sessionName(s)}</span>
                <span class="shrink-0 text-xs text-base-content/50">{s.machine}</span>
              </a>
            ))
          )}
        </section>
      ) : null}
      <section class="flex flex-col gap-2">
        <h2 class="text-xs font-semibold tracking-[0.06em] uppercase muted">Session</h2>
        <dl class="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1.5 text-[13px]">
          <dt class="text-base-content/50">Model</dt>
          <dd class="m-0 truncate font-mono">{d.model || "—"}</dd>
          <dt class="text-base-content/50">Context</dt>
          <dd class="m-0">{d.context_percent ? `${Math.round(d.context_percent)}% used` : "—"}</dd>
          <dt class="text-base-content/50">Started</dt>
          <dd class="m-0">{started}</dd>
        </dl>
      </section>
    </aside>
  );
}
