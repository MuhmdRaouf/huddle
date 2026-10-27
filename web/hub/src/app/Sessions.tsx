// Sessions.tsx — the Sessions list: what needs the owner first, then the sessions at work and the
// idle ones, grouped by machine or project; picking several to send new work to, and the
// needs-you card's deny and snooze. One tree serves the three layouts: the phone paints the list,
// the tablet and the desktop paint the list beside the session. Session text is rendered as
// text, never HTML (SPEC 9).
import { Button } from "@huddle/ui/button.tsx";
import { Dialog } from "@huddle/ui/dialog.tsx";
import { type ComponentChildren, Fragment, type JSX } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import { Icon } from "../icons.tsx";
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
  sessionName,
  shownState,
  stateWord,
} from "../registry.ts";
import { readPref, writePref } from "../storage.ts";
import { ConnectCard } from "./ConnectCard.tsx";
import { useHuddle } from "./context.tsx";
import { InsightsCard } from "./Insights.tsx";
import { LinkSheet } from "./LinkSheet.tsx";
import type { Layout } from "./layout.ts";
import { ActComposer, confirmWhat, type NeedsAct, SnoozeMenu, useNeedsAct } from "./Needs.tsx";
import {
  HarnessBadge,
  HooksStaleChip,
  LINE,
  ModelChip,
  MUTED,
  openHref,
  StateChip,
  StateMark,
  whereOf,
} from "./session-parts.tsx";
import { MachinesLine } from "./TopBar.tsx";

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

/** The list's five sections, in the order the Sessions list shows them; empty ones draw nothing. */
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

/** The Sessions list: the registry's sessions as the three layouts' list pane. The tablet carries its
 *  own header (the phone's shell and the desktop's bar draw theirs); the group-by toggle rides
 *  under it; the desktop's j and k move a selection Enter opens and a, d and r answer, the
 *  checkboxes pick sessions to send new work to, and the phone's long-press picks the same.
 *  `above` (the first-run checklist the shell hands down) rides at the top of the list, or right
 *  after the Needs you section when asks wait, so the first ask sits above a phone's fold. */
export function SessionsPane({
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
              <section aria-labelledby={`sessions-${key}`} class="flex flex-col gap-2 px-3 pt-3">
                <h2
                  id={`sessions-${key}`}
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
    <div class="flex min-h-0 min-w-0 flex-col" data-sessions-pane="">
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
      <div class="min-h-0 flex-1 overflow-y-auto pb-4" data-sessions-sections="">
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
      <SessionsDialogs
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
function SessionsDialogs({
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
