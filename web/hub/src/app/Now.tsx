// Now.tsx — the registry's screens: the Now list (what needs the owner first, then the sessions
// at work and the idle ones, grouped by machine or project; picking several to send new work to,
// and the needs-you card's deny and snooze) and the Session screen (the header with its chips and
// context bar, the stream of the session's events, and the action bar its open ask carries). One
// tree serves the three layouts: the phone paints the list or the session, the tablet and the
// desktop paint the list beside the session, and the desktop adds the context pane (the linked
// sessions and the session's facts). Session text is rendered as text, never HTML (SPEC 9).
import { Button } from "@huddle/ui/button.tsx";
import { Dialog } from "@huddle/ui/dialog.tsx";
import { modelColor } from "@huddle/ui/model-color.ts";
import { stampMs } from "@huddle/ui/time.ts";
import { type ComponentChildren, Fragment, type JSX } from "preact";
import { useEffect, useLayoutEffect, useRef, useState } from "preact/hooks";
import type { Api, ApiError } from "../api.ts";
import { Icon } from "../icons.tsx";
import type { ChannelLinks } from "../links.ts";
import {
  ageText,
  type GroupMode,
  groupSessions,
  harnessOf,
  machinesLine,
  machinesOf,
  type NeedItem,
  openItemOf,
  permissionOf,
  projectName,
  type RegistrySession,
  type RegistryTeammate,
  type StreamItem,
  type ToolCall,
  sessionName,
  shortModel,
  type StateRole,
  shownState,
  stateRole,
  stateWord,
  streamItems,
} from "../registry.ts";
import { readPref, writePref } from "../storage.ts";
import { ConnectCard } from "./ConnectCard.tsx";
import { errText, useHuddle } from "./context.tsx";
import { InsightsCard } from "./Insights.tsx";
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
import { MachinesLine } from "./TopBar.tsx";

/** One state's mark: its shape and the svg it draws; its colour comes from the state's role. */
type Mark = { shape: string; draw: JSX.Element };

/** The marks, keyed by the registry's state name. The word the aria-label carries is part of
 *  the signal, so colour is never the only difference. */
const MARKS: Record<string, Mark> = {
  "needs-you": {
    shape: "ring",
    draw: <circle cx="12" cy="12" r="8" fill="none" stroke="currentColor" stroke-width="5" />,
  },
  working: {
    shape: "dot",
    draw: <circle cx="12" cy="12" r="9" fill="currentColor" />,
  },
  idle: {
    shape: "hollow",
    draw: <circle cx="12" cy="12" r="8" fill="none" stroke="currentColor" stroke-width="2.5" />,
  },
  stuck: {
    shape: "dash",
    draw: <path d="M6 12h12" fill="none" stroke="currentColor" stroke-width="3.5" stroke-linecap="round" />,
  },
  ended: {
    shape: "cross",
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
  draw: <circle cx="12" cy="12" r="8" fill="none" stroke="currentColor" stroke-width="2.5" />,
};

/** Each role's mark colour, a literal per role so the styles scan finds it. */
const ROLE_MARK: Record<StateRole, string> = {
  "needs-you": "text-[var(--ui-needs-you)]",
  "turn-done": "text-[var(--ui-turn-done)]",
  working: "text-[var(--ui-working)]",
  idle: "text-[var(--ui-idle)]",
  failed: "text-[var(--ui-failed)]",
  cancelled: "text-[var(--ui-cancelled)]",
};

/** Each role's chip: its soft fill behind its ink. */
const ROLE_CHIP: Record<StateRole, string> = {
  "needs-you": "bg-[var(--ui-needs-you-soft)] text-[var(--ui-needs-you-ink)]",
  "turn-done": "bg-[var(--ui-turn-done-soft)] text-[var(--ui-turn-done-ink)]",
  working: "bg-[var(--ui-working-soft)] text-[var(--ui-working-ink)]",
  idle: "bg-[var(--ui-idle-soft)] text-[var(--ui-idle-ink)]",
  failed: "bg-[var(--ui-failed-soft)] text-[var(--ui-failed-ink)]",
  cancelled: "bg-[var(--ui-cancelled-soft)] text-[var(--ui-cancelled-ink)]",
};

/** A session's state as one 10 px mark: shape plus word, so the state never rides on colour
 *  alone, in its role's colour (the state's own unless a finished turn makes it turn-done). An
 *  unknown state draws the hollow mark and says its own name. */
export function StateMark({
  state,
  role = stateRole(state),
  class: c = "",
}: {
  state: string;
  role?: StateRole | undefined;
  class?: string | undefined;
}): JSX.Element {
  const m = MARKS[state] ?? HOLLOW;
  return (
    <svg
      data-shape={m.shape}
      class={`size-2.5 shrink-0 ${ROLE_MARK[role]} ${c}`.trim()}
      viewBox="0 0 24 24"
      role="img"
      aria-label={stateWord(state)}
    >
      {m.draw}
    </svg>
  );
}

/** A session's state as one word on its role's tint: the text the mark's shape stands for, so
 *  the state never rides on colour or shape alone. The item is the session's open ask, which
 *  tells a finished turn from a blocking one; `mark` leads the word with the state's mark. */
export function StateChip({
  state,
  item,
  mark = false,
}: {
  state: string;
  item?: NeedItem | undefined;
  mark?: boolean | undefined;
}): JSX.Element {
  const role = stateRole(state, item);
  return (
    <span data-state={state} data-role={role} class={`ui-badge ${ROLE_CHIP[role]} shrink-0 gap-1.5`}>
      {mark ? <StateMark state={state} role={role} /> : null}
      {stateWord(state)}
    </span>
  );
}

/** A model as its chip: the short name in the page's own text colour, the family's colour as
 *  the chip's leading edge (a fill behind the text fails contrast on the lighter families), a
 *  leading dot on the header's round pill, the raw id on hover. */
export function ModelChip({
  model,
  pill = false,
}: {
  model: string;
  /** The session header's round pill; the list's rows carry the small square chip. */
  pill?: boolean | undefined;
}): JSX.Element {
  const look =
    "inline-flex items-center whitespace-nowrap border border-[var(--ui-line)] bg-[var(--ui-surface-2)] ui-mono text-[var(--ui-text)]";
  if (pill) {
    return (
      <span class={`${look} gap-1.5 rounded-full px-2.5 py-[3px] text-xs`} title={model}>
        <span
          data-model-dot=""
          class="size-2 shrink-0 rounded-full"
          style={`background:${modelColor(model)}`}
          aria-hidden="true"
        />
        {shortModel(model)}
      </span>
    );
  }
  return (
    <span
      class={`${look} rounded-md px-1.5 py-px text-[11px] leading-4`}
      style={`border-left:4px solid ${modelColor(model)}`}
      title={model}
    >
      {shortModel(model)}
    </span>
  );
}

/** The context bar: how full the session's window is, one accent fill over a quiet track.
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
      class={`h-1.5 w-full overflow-hidden rounded-full bg-[var(--ui-surface-2)] ${c}`.trim()}
      role="img"
      aria-label={`Context ${n} percent used`}
    >
      <div class="h-full rounded-full bg-[var(--ui-accent)]" style={`width:${n}%`} />
    </div>
  );
}

/** A row's where: machine · project · branch, the parts that exist joined by middots. */
const whereOf = (s: RegistrySession): string =>
  [s.machine, projectName(s.project), s.branch].filter(Boolean).join(" · ");

/** The quiet ink of a secondary line, and the hairline a divider draws. */
const MUTED = "text-[var(--ui-muted)]";
const LINE = "border-[var(--ui-line)]";

/** A session's harness as its chip: the mark and the name of the coding agent it runs in, from
 *  the one HARNESSES table. The list's rows and cards draw it small on the line under the name,
 *  so the name keeps the row's width; the header draws it as a pill beside the other chips. */
function HarnessBadge({ s, pill = false }: { s: RegistrySession; pill?: boolean }): JSX.Element {
  const h = harnessOf(s);
  const shape = pill ? "rounded-full px-2.5 py-0.5 text-xs" : "rounded-md px-1 py-px text-[11px] leading-4";
  return (
    <span
      data-harness={h.id}
      title={`This session runs in ${h.name}`}
      class={`inline-flex shrink-0 items-center gap-1 border ${LINE} ${shape} ${MUTED}`}
    >
      <span
        data-harness-mark=""
        aria-hidden="true"
        class="ui-mono rounded-sm bg-[var(--ui-surface-2)] px-0.5 text-[9px] font-semibold leading-3 text-[var(--ui-text)]"
      >
        {h.mark}
      </span>
      {h.name}
    </span>
  );
}

/** The chip a session whose hooks stopped reporting carries beside its state: a warning to
 *  look, report only, with nothing to press. */
function HooksStaleChip({ s }: { s: RegistrySession }): JSX.Element | null {
  if (!s.hooks_stale) return null;
  return (
    <span data-hooks-stale="" class="ui-badge shrink-0 bg-[var(--ui-warn-soft)] text-[var(--ui-warn-ink)]">
      hooks stale
    </span>
  );
}

/** The link that opens one session from the list. */
const openHref = (s: RegistrySession): string => `#/sessions/${s.session_id}`;

/** The needs-you card: the session that waits on the owner (its name, then its harness chip and
 *  where it works), what it asks, and the answers at the card's foot, 48 px tall for one thumb. A
 *  permission ask offers Deny and Allow once, both posted at once (an allow may open the passkey
 *  sheet first); any other ask offers Review, which opens the session to answer it. The menu
 *  beside them snoozes or dismisses the item. */
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
      class={`ui-card flex flex-col ${sel ? "ring-2 ring-[var(--ui-accent)]" : ""}`}
      data-row={s.session_id}
      aria-current={sel ? "true" : undefined}
    >
      <a href={openHref(s)} class="flex min-w-0 flex-col gap-1.5 p-4 text-[var(--ui-text)]">
        <div class="flex items-baseline justify-between gap-2">
          <span class="truncate text-base font-semibold">{sessionName(s)}</span>
          <span class="flex shrink-0 items-center gap-1.5">
            <HooksStaleChip s={s} />
            <StateChip state={s.state} item={item} />
            <span class={`text-xs ${MUTED}`}>{ageText(s.last_event_at, now)}</span>
          </span>
        </div>
        <div class="flex min-w-0 items-center gap-1.5">
          <HarnessBadge s={s} />
          <span class={`truncate text-xs ${MUTED}`}>{whereOf(s)}</span>
        </div>
        <div class="line-clamp-2 text-sm">{s.task || s.title || stateWord(s.state)}</div>
      </a>
      {ask ? (
        <div class="flex min-w-0 flex-col gap-1.5 px-4 pb-3" data-ask="">
          <span class="text-sm font-semibold">
            Allow <code class="ui-mono">{ask.tool}</code>?
          </span>
          {ask.input ? (
            <code class="ui-mono block overflow-x-auto whitespace-pre rounded-[var(--ui-radius)] border border-[var(--ui-line)] bg-[var(--ui-surface-2)] px-3 py-2 text-[13px]">
              {ask.input}
            </code>
          ) : null}
        </div>
      ) : null}
      <div class={`flex gap-2 border-t ${LINE} p-3`} data-needs-actions="">
        {item ? <SnoozeMenu item={item} /> : null}
        {ask ? (
          <>
            <Button variant="danger" size="lg" data-act="deny" class="flex-1" onClick={() => act.deny(ask)}>
              Deny
            </Button>
            <Button
              size="lg"
              data-act="allow"
              class="flex-1"
              onClick={() => act.allow(ask, confirmWhat(ask, s, true))}
            >
              <Icon name="key" class="size-4" />
              Allow once
            </Button>
          </>
        ) : (
          <Button href={openHref(s)} size="lg" data-act="review" class="flex-1">
            Review
          </Button>
        )}
      </div>
    </article>
  );
}

/** The selected row's ring: the desktop's keyboard selection. */
const selRing = (sel: boolean): string => (sel ? " ring-2 ring-inset ring-[var(--ui-accent)]" : "");

/** A working or stuck row: the mark, the name taking the row's free width, its harness chip with
 *  where it works and its task under it, and the state badge, the model chip and how full its
 *  window is after. */
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
        <span class="ui-list-title min-w-0">{sessionName(s)}</span>
        <span class="flex min-w-0 items-center gap-1.5">
          <HarnessBadge s={s} />
          <span class="ui-list-meta truncate">{[s.machine, s.task].filter(Boolean).join(" · ")}</span>
        </span>
      </span>
      <span class="ui-list-trailing flex-col items-end gap-1">
        <span class="flex items-center gap-1.5">
          <HooksStaleChip s={s} />
          <StateChip state={s.state} />
          {s.model ? <ModelChip model={s.model} /> : null}
        </span>
        <span class={`text-xs ${MUTED} tabular-nums`}>
          {s.context_percent ? `${Math.round(s.context_percent)}%` : ""}
        </span>
      </span>
    </a>
  );
}

/** An idle or ended row: the mark, the name taking the row's free width with its machine and its
 *  harness chip under it, the state badge after it, and how long it has been quiet. */
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
        <span class="ui-list-title min-w-0">
          {sessionName(s)}{" "}
          {s.machine ? <span class={`text-[13px] font-normal ${MUTED}`}>· {s.machine}</span> : null}
        </span>
        <span class="flex min-w-0 items-center">
          <HarnessBadge s={s} />
        </span>
      </span>
      <span class="ui-list-trailing gap-1.5">
        <HooksStaleChip s={s} />
        <StateChip state={s.state} />
        <span class={`text-xs ${MUTED}`}>{ageText(s.last_event_at, now)}</span>
      </span>
    </a>
  );
}

/** A lead's agent-team teammates under its row: each name with its state word. */
function TeammateList({ teammates }: { teammates: RegistryTeammate[] }): JSX.Element {
  return (
    <ul class="m-0 flex list-none flex-col gap-0.5 px-4 pb-2 pl-10" aria-label="Teammates">
      {teammates.map((m) => (
        <li
          key={m.name}
          data-teammate={m.name}
          class={`flex min-w-0 items-center gap-1.5 text-[13px] ${MUTED}`}
        >
          <span class="min-w-0 flex-1 truncate">{m.name}</span>
          <StateChip state={m.state} />
        </li>
      ))}
    </ul>
  );
}

/** The list's five sections, in the order the Now list shows them; empty ones draw nothing. */
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
          class="mt-5 size-4 shrink-0 accent-[var(--ui-accent)]"
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
 *  `above` (the first-run checklist the shell hands down) rides at the top of the list, or right
 *  after the Needs you section when asks wait, so the first ask sits above a phone's fold. */
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
  // an open item holds a session in Needs you even when its last live row reads idle
  const groups = groupSessions(st.sessions?.map((s) => ({ ...s, state: shownState(s, st.needs) })) ?? null);
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
            <h3 class={`m-0 px-1 pt-1 text-xs font-medium ${MUTED}`}>
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

  /** The sections in order, each heading over its rows, with the Insights card after the asks. */
  const sections = (): JSX.Element => {
    // the Insights card rides after the needs-you cards, before the first other section;
    // the needs-you filter holds the asks alone
    const firstOther = onlyNeeds
      ? null
      : (SECTIONS.find(([key]) => key !== "needsYou" && groups[key].length > 0)?.[0] ?? null);
    const cards = (
      <div class="flex flex-col gap-3 px-3 pt-3">
        <InsightsCard />
      </div>
    );
    return (
      <>
        {SECTIONS.filter(([key]) => (!onlyNeeds || key === "needsYou") && groups[key].length > 0).map(
          ([key, label, warn]) => (
            <Fragment key={key}>
              {/* the Insights card follows what needs the owner, or leads when nothing does */}
              {key === firstOther ? cards : null}
              <section aria-labelledby={`now-${key}`} class="flex flex-col gap-2 px-3 pt-3">
                <h2
                  id={`now-${key}`}
                  class={`m-0 flex items-center gap-2 px-1 text-[13px] font-semibold tracking-[0.06em] uppercase ${
                    warn ? "text-[var(--ui-needs-you-ink)]" : MUTED
                  }`}
                >
                  {warn ? <StateMark state="needs-you" /> : null}
                  {label} · {groups[key].length}
                </h2>
                {sectionRows(groups[key], label)}
              </section>
              {key === "needsYou" ? above : null}
            </Fragment>
          ),
        )}
        {!onlyNeeds && firstOther === null ? cards : null}
      </>
    );
  };

  return (
    <div class="flex min-h-0 min-w-0 flex-col" data-now-pane="">
      {layout === "tablet" ? (
        <header class="flex shrink-0 items-center justify-between px-4 pt-5 pb-3">
          <h1 class="text-xl font-semibold tracking-tight">Now</h1>
          <MachinesLine live={st.live} text={machinesLine(layout, machinesOf(st.sessions))} />
        </header>
      ) : null}
      <div class={`flex shrink-0 flex-wrap items-center justify-between gap-2 border-b ${LINE} px-4 py-2`}>
        <div class="flex items-center gap-2">
          <span class={`text-xs ${MUTED}`}>Group by</span>
          <div class="flex gap-1">
            {(["machine", "project"] as const).map((k) => (
              <Button
                key={k}
                variant="ghost"
                pressed={group === k}
                onClick={() => {
                  setGroup(k);
                  writePref(localStorage, "nowgroup", k);
                }}
              >
                {k === "machine" ? "Machine" : "Project"}
              </Button>
            ))}
          </div>
        </div>
        <Button
          variant="secondary"
          data-needs-filter=""
          pressed={onlyNeeds}
          onClick={() => {
            const next = !onlyNeeds;
            setOnlyNeeds(next);
            writePref(localStorage, "nowneeds", next);
          }}
        >
          Needs you
        </Button>
      </div>
      <div class="min-h-0 flex-1 overflow-y-auto pb-4" data-now-sections="">
        {groups.needsYou.length > 0 ? null : above}
        {st.sessions === null ? (
          <div class="grid gap-2 px-3 pt-3" role="status" aria-label="Reading the registry">
            {[0, 1, 2, 3].map((row) => (
              <div
                key={row}
                class="h-12 animate-pulse rounded-[var(--ui-radius)] bg-[var(--ui-surface-2)]"
                aria-hidden="true"
              />
            ))}
          </div>
        ) : rows.length === 0 ? (
          onlyNeeds ? (
            <div
              class={`mx-auto m-4 flex max-w-lg flex-col items-center gap-2 rounded-[var(--ui-radius-lg)] border border-dashed border-[var(--ui-line-strong)] px-6 py-16 text-center text-sm ${MUTED}`}
            >
              <b class="text-base font-semibold text-[var(--ui-text)]">Nothing needs you</b>
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
        <div class={`flex shrink-0 gap-3.5 border-t ${LINE} p-2.5 text-xs ${MUTED}`}>
          <span>
            <Key k="j" /> <Key k="k" /> move
          </span>
          <span>
            <Key k="a" /> allow <Key k="d" /> deny <Key k="r" /> reply
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

/** One key of the desktop's key hints, drawn as a small keycap. */
function Key({ k }: { k: string }): JSX.Element {
  return (
    <kbd class="ui-mono rounded-[var(--ui-radius-sm)] border border-[var(--ui-line-strong)] px-1 text-[11px] text-[var(--ui-text)]">
      {k}
    </kbd>
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
        <p class={`m-0 text-sm ${MUTED}`}>
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
    <div class={`flex shrink-0 flex-wrap items-center gap-2 border-t ${LINE} px-3 py-2`} data-select-bar="">
      <span class={`text-sm ${MUTED}`}>{n} selected</span>
      <span class="flex-1" />
      <Button variant="ghost" onClick={onClear}>
        Clear
      </Button>
      {n >= 2 ? (
        <Button variant="ghost" onClick={onLink}>
          Link {n} sessions
        </Button>
      ) : null}
      <Button onClick={onSend}>
        Send to {n} {n === 1 ? "session" : "sessions"}
      </Button>
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

/** The session's chips: its state (mark and word) in its role's tone, the hooks-stale chip when
 *  its hooks stopped reporting, its model, its channel when it is in one, and the harness it runs
 *  in. The state is the one the session shows, and its open item tells a finished turn from a
 *  blocking ask. */
function Chips({ s, needs }: { s: RegistrySession; needs: NeedItem[] | null }): JSX.Element {
  return (
    <span class="flex min-w-0 flex-wrap items-center gap-1.5">
      <StateChip state={shownState(s, needs)} item={openItemOf(needs, s.session_id)} mark />
      <HooksStaleChip s={s} />
      {s.model ? <ModelChip model={s.model} pill /> : null}
      {s.channel ? (
        <span class={`rounded-full border ${LINE} px-2.5 py-0.5 text-xs ${MUTED}`}>#{s.channel}</span>
      ) : null}
      <HarnessBadge s={s} pill />
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

/** One stream line's look: a tool row, a group's header and each call a group opens to. */
const STREAM_LINE = `flex items-center gap-2 text-[13px] ${MUTED}`;

/** A tool call's mark: a check once done, a clock while running, a circled dash once ended
 *  without its PostToolUse. */
function CallMark({ state }: { state: ToolCall["state"] }): JSX.Element {
  if (state === "running") return <Icon name="clock" class="size-4 shrink-0" />;
  if (state === "ended") return <Icon name="minusc" class="size-4 shrink-0" />;
  return <Icon name="check" class="size-4 shrink-0 text-[var(--ui-ok)]" />;
}

/** The data attributes a call's line carries for its state. */
function callData(state: ToolCall["state"]): { "data-running"?: ""; "data-ended"?: "" } {
  if (state === "running") return { "data-running": "" };
  if (state === "ended") return { "data-ended": "" };
  return {};
}

/** The word after a call that has not finished: running, or ended. */
function CallWord({ state }: { state: ToolCall["state"] }): JSX.Element | null {
  return state === "done" ? null : <span class="shrink-0 text-xs">{state}</span>;
}

/** One call's target, in the stream's mono face. */
function Target({ target }: { target: string }): JSX.Element | null {
  return target ? <code class="ui-mono min-w-0 truncate text-xs">{target}</code> : null;
}

/** A row of the stream's tool calls: one call as one line with its mark, tool, target and word;
 *  repeated calls of one tool as one button with the latest target and a count, which a tap
 *  opens to one line per call. */
function ToolRow({ item }: { item: Extract<StreamItem, { kind: "tool" }> }): JSX.Element {
  const [open, setOpen] = useState(false);
  const latest = item.calls[item.calls.length - 1] as ToolCall;
  const state = item.calls.some((c) => c.state === "running") ? "running" : latest.state;
  if (item.calls.length === 1) {
    return (
      <div class={STREAM_LINE} data-tool="" {...callData(state)}>
        <CallMark state={state} />
        <span>{item.tool}</span>
        <Target target={latest.target} />
        <CallWord state={state} />
      </div>
    );
  }
  return (
    <div class="flex flex-col" data-tool="" {...callData(state)}>
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
        class={`${STREAM_LINE} -my-2.5 min-h-[var(--ui-tap)] w-full rounded-[var(--ui-radius-sm)] text-left outline-none focus-visible:ring-2 focus-visible:ring-[var(--ui-accent)]`}
      >
        <CallMark state={state} />
        <span>{item.tool}</span>
        <Target target={latest.target} />
        <span class="ui-mono shrink-0 text-xs" aria-hidden="true">
          ×{item.calls.length}
        </span>
        <span class="sr-only">{item.calls.length} calls</span>
        <CallWord state={state} />
        <Icon name={open ? "up" : "down"} class="ml-auto size-4 shrink-0" />
      </button>
      {open ? (
        <div class="flex flex-col gap-1.5 pt-3 pl-6" data-tool-calls="">
          {item.calls.map((call, i) => (
            <div key={i} class={STREAM_LINE} {...callData(call.state)}>
              <CallMark state={call.state} />
              <Target target={call.target} />
              <CallWord state={call.state} />
            </div>
          ))}
        </div>
      ) : null}
    </div>
  );
}

/** One item of the session's stream: a prompt as a right bubble, a tool row, anything else that
 *  carries text as a plain paragraph. */
function StreamPart({ item }: { item: StreamItem }): JSX.Element {
  if (item.kind === "prompt") {
    return (
      <div class="max-w-[85%] self-end rounded-[14px_14px_4px_14px] bg-[var(--ui-surface-2)] px-3 py-2.5 break-words">
        {item.text}
      </div>
    );
  }
  if (item.kind === "tool") return <ToolRow item={item} />;
  return <div class="break-words whitespace-pre-wrap">{item.text}</div>;
}

/** How close to the end, in px, still counts as reading the latest event. */
const BOTTOM_SLACK = 8;

/** The session's stream with its day dividers. It opens at the latest event and follows new
 *  ones while the reader is at the bottom; a reader who scrolled up stays where they are. */
function StreamPane({ items, now }: { items: StreamItem[]; now: number }): JSX.Element {
  const pane = useRef<HTMLDivElement>(null);
  const atBottom = useRef(true);
  // one count per call, so a call that joins a group follows as a new item does
  const size = items.reduce((n, item) => n + (item.kind === "tool" ? item.calls.length : 1), 0);
  useLayoutEffect(() => {
    const el = pane.current;
    if (el && atBottom.current) el.scrollTop = el.scrollHeight;
  }, [size]);
  const onScroll = (): void => {
    const el = pane.current;
    if (el) atBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight <= BOTTOM_SLACK;
  };
  let lastDay = "";
  return (
    <div
      ref={pane}
      onScroll={onScroll}
      class="min-h-0 flex-1 overflow-y-auto px-4 py-3.5 sm:px-6"
      data-stream=""
    >
      <div class="mx-auto flex max-w-[720px] flex-col gap-3 text-sm leading-relaxed sm:text-[15px]">
        {items.map((item, i) => {
          const parts: JSX.Element[] = [];
          const day = dayLabel(item.at, now);
          const dayKey = day.split(" ").slice(0, -1).join(" ");
          if (day && dayKey !== lastDay) {
            lastDay = dayKey;
            parts.push(
              <div key={`d${i}`} class={`py-1 text-center text-xs ${MUTED}`}>
                {day}
              </div>,
            );
          }
          parts.push(<StreamPart key={i} item={item} />);
          return <>{parts}</>;
        })}
        {items.length === 0 ? <p class={`py-6 text-center text-sm ${MUTED}`}>No events yet.</p> : null}
      </div>
    </div>
  );
}

/** One item of the session's link menu: a full tap target tall. */
const MENU_ITEM =
  "flex min-h-[var(--ui-tap)] items-center rounded-[var(--ui-radius-sm)] px-2.5 text-left text-sm text-[var(--ui-text)] outline-none hover:bg-[var(--ui-surface-2)] focus-visible:bg-[var(--ui-surface-2)]";

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
      <div class="flex flex-col gap-3" data-link-act="">
        <select
          class="ui-input"
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
        <p class={`m-0 text-xs ${MUTED}`}>
          {act === "handover"
            ? "Briefs the session you pick, moves this session's open tasks to it and leaves this session out of the channel."
            : "Sends this session's newest digest to the session you pick, on its next prompt."}
        </p>
        <div class="flex justify-end gap-2">
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button disabled={busy || to === ""} onClick={onRun}>
            {act === "handover" ? "Hand over" : "Brief"}
          </Button>
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
          class={`flex size-[var(--ui-tap)] items-center justify-center rounded-[var(--ui-radius)] ${MUTED} outline-none hover:bg-[var(--ui-surface-2)] focus-visible:ring-2 focus-visible:ring-[var(--ui-accent)]`}
          onClick={() => setMenuOpen((v) => !v)}
        >
          <Icon name="more" class="size-4" />
        </button>
        {menuOpen ? (
          <div
            role="menu"
            aria-label="Link actions"
            class={`absolute top-full right-0 z-20 mt-1 flex min-w-52 flex-col rounded-[var(--ui-radius)] border ${LINE} bg-[var(--ui-surface)] p-1 shadow-lg`}
          >
            <button type="button" role="menuitem" class={MENU_ITEM} onClick={() => open("brief")}>
              Brief another session…
            </button>
            <button type="button" role="menuitem" class={MENU_ITEM} onClick={() => open("handover")}>
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

/** The Session screen: the header (name, where, chips, context bar), the line that says how to
 *  restart a session whose hooks stopped reporting, the stream of the session's events, and the
 *  action bar its open needs-you ask carries (the phone's sheet, the wide layouts' inline box).
 *  The phone and the tablet paint the two-row header, so the name keeps its own row (the phone's
 *  carries the back link; the tablet's list stands beside it); the desktop carries its header in
 *  one bar. */
export function SessionScreen({ id, layout }: { id: string; layout: Layout }): JSX.Element {
  const { registry, now } = useHuddle();
  const st = registry.getState();
  const d = st.open;

  if (!d) {
    const line = st.openMissing ? `There is no session ${id}.` : "Reading the session…";
    return (
      <div class="grid min-h-0 flex-1 place-items-center px-6">
        <p class={`text-sm ${MUTED}`}>{line}</p>
      </div>
    );
  }
  const ask = openItemOf(st.needs, id);

  const items = streamItems(d.events ?? []);
  const pct = d.context_percent ? Math.round(d.context_percent) : 0;

  return (
    <div class="flex min-h-0 min-w-0 flex-1 flex-col" data-session="">
      {layout !== "desktop" ? (
        <header
          class={`flex shrink-0 flex-col gap-2.5 border-b ${LINE} bg-[var(--ui-surface)] px-3 pt-3.5 pb-3`}
        >
          <div class="flex items-center gap-1">
            {layout === "phone" ? (
              <a
                href="#/sessions"
                aria-label="Back to Sessions"
                class="flex size-[var(--ui-tap)] shrink-0 items-center justify-center rounded-[var(--ui-radius)] text-[var(--ui-text)] outline-none focus-visible:ring-2 focus-visible:ring-[var(--ui-accent)]"
              >
                <Icon name="left" class="size-[22px]" />
              </a>
            ) : null}
            <div class="flex min-w-0 flex-1 flex-col gap-0.5">
              <div class="truncate text-lg font-semibold">{sessionName(d)}</div>
              <div class={`truncate text-xs ${MUTED}`}>{whereOf(d)}</div>
            </div>
            <SessionActions session={d} />
          </div>
          <div class="px-2">
            <Chips s={d} needs={st.needs} />
          </div>
          {pct ? (
            <div class="flex items-center gap-2.5 px-2">
              <ContextBar pct={pct} class="min-w-0 flex-1" />
              <span class={`shrink-0 text-xs ${MUTED}`}>{pct}% context</span>
            </div>
          ) : null}
        </header>
      ) : (
        <header class={`flex shrink-0 flex-wrap items-center gap-3.5 border-b ${LINE} px-6 pt-4 pb-3.5`}>
          <div class="flex min-w-0 flex-1 flex-col gap-0.5">
            <div class="truncate text-xl font-semibold">{sessionName(d)}</div>
            <div class={`truncate text-[13px] ${MUTED}`}>{whereOf(d)}</div>
          </div>
          <Chips s={d} needs={st.needs} />
          <SessionActions session={d} />
          {pct ? (
            <span class="flex w-[140px] shrink-0 items-center gap-2">
              <ContextBar pct={pct} />
              <span class={`shrink-0 text-xs ${MUTED}`}>{pct}%</span>
            </span>
          ) : null}
        </header>
      )}
      {d.hooks_stale ? (
        <p data-resume="" class={`shrink-0 border-b ${LINE} px-4 py-2.5 text-sm sm:px-6`}>
          Hooks stopped reporting: restart this session with{" "}
          <code class="ui-mono">claude --resume {d.session_id}</code>
        </p>
      ) : null}
      <StreamPane key={d.session_id} items={items} now={now} />
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
      class={`flex min-h-0 min-w-0 flex-col gap-4 overflow-y-auto border-l ${LINE} bg-[var(--ui-surface)] p-4`}
      data-now-context=""
    >
      {d.channel ? (
        <section class="flex flex-col gap-2.5">
          <h2 class={`text-xs font-semibold tracking-[0.06em] uppercase ${MUTED}`}>Linked in #{d.channel}</h2>
          {linked.length === 0 ? (
            <p class={`text-[13px] ${MUTED}`}>No other session is linked here yet.</p>
          ) : (
            linked.map((s) => (
              <a
                key={s.session_id}
                href={openHref(s)}
                class="flex min-h-[var(--ui-tap)] min-w-0 items-center gap-2.5 text-sm outline-none focus-visible:ring-2 focus-visible:ring-[var(--ui-accent)]"
              >
                <StateMark state={shownState(s, st.needs)} />
                <span class="truncate">{sessionName(s)}</span>
                <span class={`shrink-0 text-xs ${MUTED}`}>{s.machine}</span>
              </a>
            ))
          )}
        </section>
      ) : null}
      <section class="flex flex-col gap-2">
        <h2 class={`text-xs font-semibold tracking-[0.06em] uppercase ${MUTED}`}>Session</h2>
        <dl class="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1.5 text-[13px]">
          <dt class={MUTED}>Model</dt>
          <dd class="ui-mono m-0 truncate">{d.model || "—"}</dd>
          <dt class={MUTED}>Context</dt>
          <dd class="m-0">{d.context_percent ? `${Math.round(d.context_percent)}% used` : "—"}</dd>
          <dt class={MUTED}>Started</dt>
          <dd class="m-0">{started}</dd>
        </dl>
      </section>
    </aside>
  );
}
