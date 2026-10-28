// Sessions.tsx — the Sessions list: what needs the owner first, then the sessions at work and the
// idle ones; a search box and the machine chips narrow the rows; picking several to send new work
// to, and the needs-you card's deny and snooze; the set-up row opens the first-run checklist in
// its sheet while steps are left. One tree serves the three layouts: the phone paints the list,
// the tablet and the desktop paint the list beside the session. Session text is rendered as text,
// never HTML (SPEC 9).
import { Button } from "@huddle/ui/button.tsx";
import { Dialog } from "@huddle/ui/dialog.tsx";
import { Field } from "@huddle/ui/field.tsx";
import { Sheet } from "@huddle/ui/sheet.tsx";
import { type ComponentChildren, Fragment, type JSX } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type { Api } from "../api.ts";
import { Icon } from "../icons.tsx";
import {
  ageText,
  groupSessions,
  harnessOf,
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
import { sessionApi } from "../session/api.ts";
import type { SessionTasks } from "../session/types.ts";
import { readPref, writePref } from "../storage.ts";
import { ConnectCard } from "./ConnectCard.tsx";
import { useHuddle } from "./context.tsx";
import { FirstRunChecklist, type FirstRunState, useFirstRun } from "./FirstRun.tsx";
import { InsightsCard } from "./Insights.tsx";
import { LinkSheet } from "./LinkSheet.tsx";
import type { Layout } from "./layout.ts";
import { ActComposer, confirmWhat, type NeedsAct, SnoozeMenu, useNeedsAct } from "./Needs.tsx";
import {
  FeatureChips,
  featuresOf,
  HarnessBadge,
  HooksStaleChip,
  LINE,
  MUTED,
  openHref,
  RowStateChip,
  StateChip,
  whereOf,
} from "./session-parts.tsx";
import { MachinesLine } from "./TopBar.tsx";

/** The phone's needs-you card: the session that waits on the owner (its name, then its harness
 *  chip and where it works), what it asks, and the answers at the card's foot, 48 px tall for one
 *  thumb. A permission ask offers Deny and Allow once, both posted at once (an allow may open the
 *  passkey sheet first); any other ask offers Review, which opens the session to answer it. The
 *  menu beside them snoozes or dismisses the item. The desk draws the same session as one plain
 *  row and answers the ask at the session's own needs bar. An `<a>` cannot hold another, so the
 *  card is the container, its opening link stretches over the whole of it, and the feature chip
 *  line rides above that overlay; the ask and the answer bar sit above it too, so their controls
 *  stay their own. */
function NeedsCard({
  s,
  now,
  act,
  tasks,
}: {
  s: RegistrySession;
  now: number;
  act: NeedsAct;
  tasks: SessionTasks | null;
}): JSX.Element {
  const { registry } = useHuddle();
  const needs = registry.getState().needs;
  const ask = permissionOf(needs, s.session_id);
  const item = openItemOf(needs, s.session_id);
  const chips = tasks && featuresOf(tasks).length > 0 ? <FeatureChips tasks={tasks} phone /> : null;
  return (
    <article class="ui-card relative flex flex-col" data-row={s.session_id}>
      <a
        href={openHref(s)}
        class="flex min-w-0 flex-col gap-1.5 p-4 text-[var(--ui-text)] after:absolute after:inset-0 after:content-['']"
      >
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
      {chips ? <div class="px-4 pb-3">{chips}</div> : null}
      {ask ? (
        <div class="relative flex min-w-0 flex-col gap-1.5 px-4 pb-3" data-ask="">
          <span class="text-sm font-semibold">
            Allow <code class="ui-mono">{ask.tool}</code>?
          </span>
          {ask.input ? (
            <code
              class="ui-mono block overflow-x-auto whitespace-pre rounded-[var(--ui-radius)] border border-[var(--ui-line)] bg-[var(--ui-surface-2)] px-3 py-2 text-[13px]"
              tabindex={0}
            >
              {ask.input}
            </code>
          ) : null}
        </div>
      ) : null}
      <div class={`relative flex gap-2 border-t ${LINE} p-3`} data-needs-actions="">
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

/** The row's selection look: the desktop's keyboard pick and the open session both fill the row
 *  with the accent's soft tint and round it off from the card's edges. The fill carries the
 *  important mark: .ui-list-row's own transparent background is unlayered, so the bare utility
 *  would lose the cascade to it. */
const selRing = (sel: boolean): string =>
  sel ? " rounded-[var(--ui-radius)] bg-[var(--ui-accent-soft)]!" : "";

/** The list row's harness chip: the quiet chip naming the harness in words, no mark and no
 *  border. */
function RowHarness({ s }: { s: RegistrySession }): JSX.Element {
  const h = harnessOf(s);
  return (
    <span
      data-harness={h.id}
      title={`This session runs in ${h.name}`}
      class="inline-flex h-7 shrink-0 items-center rounded-[var(--ui-radius-sm)] bg-[var(--ui-surface-2)] px-2.5 text-sm font-medium text-[var(--ui-muted)]"
    >
      {h.name}
    </span>
  );
}

/** The phone row's avatar: the harness's initials in a quiet circle. */
function RowAvatar({ s }: { s: RegistrySession }): JSX.Element {
  const initials = harnessOf(s)
    .name.split(/\s+/)
    .slice(0, 2)
    .map((w) => (w[0] ?? "").toUpperCase())
    .join("");
  return (
    <span
      aria-hidden="true"
      class="flex size-9 shrink-0 items-center justify-center rounded-full bg-[var(--ui-surface-2)] text-xs font-bold text-[var(--ui-muted)]"
    >
      {initials}
    </span>
  );
}

/** The desk's one plain row for every state, needs-you included, the artboard's two lines: the
 *  title with its harness chip, then where it works with the state chip and the quiet age at the
 *  end. It opens the session; the session's own needs bar answers an ask. With a carded session's
 *  feature chips the row turns container: an `<a>` cannot hold another, so the title link
 *  stretches over the whole row and the chip line rides above that overlay as links of their
 *  own. */
function DeskRow({
  s,
  now,
  sel,
  open,
  tasks,
}: {
  s: RegistrySession;
  now: number;
  sel: boolean;
  open: boolean;
  tasks: SessionTasks | null;
}): JSX.Element {
  const meta = [projectName(s.project), s.branch, s.machine].filter(Boolean);
  if (!s.delivery_route) meta.push("watch only");
  const chips = tasks && featuresOf(tasks).length > 0 ? <FeatureChips tasks={tasks} /> : null;
  const body = (
    <>
      <span class="flex h-7 min-w-0 items-center gap-2">
        <span class="ui-list-title min-w-0 flex-1 leading-[1.2]">{sessionName(s)}</span>
        <RowHarness s={s} />
      </span>
      <span class="flex h-7 min-w-0 items-center gap-2">
        <span class="ui-list-meta min-w-0 flex-1 truncate">{meta.join(" · ")}</span>
        <HooksStaleChip s={s} />
        <span class={`shrink-0 text-xs ${MUTED} tabular-nums`}>{ageText(s.last_event_at, now)}</span>
        <RowStateChip state={s.state} />
      </span>
    </>
  );
  if (chips) {
    return (
      <div
        data-row={s.session_id}
        aria-current={open ? "page" : sel ? "true" : undefined}
        class={`relative${selRing(sel || open)}`}
      >
        <a
          href={openHref(s)}
          class="ui-list-row flex-col! items-stretch! justify-center! gap-0! min-h-14! after:absolute after:inset-0 after:content-['']"
        >
          {body}
        </a>
        <div class="px-4 pb-2.5">{chips}</div>
      </div>
    );
  }
  return (
    <a
      href={openHref(s)}
      data-row={s.session_id}
      aria-current={open ? "page" : sel ? "true" : undefined}
      class={`ui-list-row flex-col! items-stretch! justify-center! gap-0! min-h-14!${selRing(sel || open)}`}
    >
      {body}
    </a>
  );
}

/** The phone's one row for every state but needs-you, the artboard's three columns: the
 *  harness's initials, the name over where it works over the harness chip, and the state chip
 *  over how long the session has been quiet. With a carded session's feature chips the row turns
 *  container: an `<a>` cannot hold another, so the row's link stretches over the whole of it and
 *  the chip line rides above that overlay as links of their own. */
function PhoneRow({
  s,
  now,
  sel,
  tasks,
}: {
  s: RegistrySession;
  now: number;
  sel: boolean;
  tasks: SessionTasks | null;
}): JSX.Element {
  const meta = [projectName(s.project), s.branch, s.machine].filter(Boolean);
  if (!s.delivery_route) meta.push("watch only");
  const chips = tasks && featuresOf(tasks).length > 0 ? <FeatureChips tasks={tasks} phone /> : null;
  const body = (
    <>
      <RowAvatar s={s} />
      <span class="flex min-w-0 flex-1 flex-col items-start gap-1">
        <span class="ui-list-title max-w-full leading-[1.2]">{sessionName(s)}</span>
        <span class="ui-list-meta max-w-full truncate leading-[1.35]">{meta.join(" · ")}</span>
        <RowHarness s={s} />
      </span>
      <span class="flex shrink-0 flex-col items-end gap-1">
        <HooksStaleChip s={s} />
        <RowStateChip state={s.state} />
        <span class={`text-xs ${MUTED} tabular-nums`}>{ageText(s.last_event_at, now)}</span>
      </span>
    </>
  );
  if (chips) {
    return (
      <div data-row={s.session_id} aria-current={sel ? "true" : undefined} class="relative">
        <a
          href={openHref(s)}
          class="ui-list-row min-h-16! py-2.5! after:absolute after:inset-0 after:content-['']"
        >
          {body}
        </a>
        <div class="px-4 pb-2">{chips}</div>
      </div>
    );
  }
  return (
    <a
      href={openHref(s)}
      data-row={s.session_id}
      aria-current={sel ? "true" : undefined}
      class="ui-list-row min-h-16! py-2.5!"
    >
      {body}
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
])[] = [
  ["needsYou", "Needs you"],
  ["working", "Working"],
  ["idle", "Idle"],
  ["stuck", "Stuck"],
  ["ended", "Ended"],
];

/** One row's shell: the desktop carries a checkbox beside it while Select is on (the pick a send
 *  to many sends to), the phone hears a long-press for the same. A tap toggles the row only while
 *  a pick is on. */
function RowShell({
  layout,
  s,
  selecting,
  picked,
  tap,
  onPick,
  onHoldStart,
  onHoldStop,
  children,
}: {
  layout: Layout;
  s: RegistrySession;
  selecting: boolean;
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
      <div
        class={`flex items-start gap-1${selecting ? " pl-3" : ""}`}
        data-select-row={s.session_id}
        onClick={tap}
      >
        {selecting ? (
          <input
            type="checkbox"
            class="mt-5 size-4 shrink-0 accent-[var(--ui-accent)]"
            aria-label={`Select ${sessionName(s)}`}
            checked={picked}
            onClick={(e) => e.stopPropagation()}
            onChange={onPick}
          />
        ) : null}
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

/** The set-up row and its sheet: the row rides under the machine chips while the checklist has
 *  steps left and stands undismissed, and opens the checklist in the sheet; a minted passkey hands
 *  its token back up through `onPairToken` to the pairing screen. The steps finishing or the
 *  checklist hiding itself close the sheet, the row going with them. */
function SetupPane({
  setup,
  onPairToken,
  layout,
}: {
  setup: FirstRunState;
  onPairToken?: ((token: string) => void) | undefined;
  layout: Layout;
}): JSX.Element {
  const [open, setOpen] = useState(false);
  const showRow = !setup.dismissed && setup.left > 0;

  useEffect(() => {
    if (open && !showRow) setOpen(false);
  });

  return (
    <>
      {showRow ? (
        <div class="shrink-0 px-4 pt-4">
          <Button
            variant="secondary"
            class={`${layout === "phone" ? "h-13! min-h-13! px-4!" : "h-11! min-h-11! px-3!"} w-full justify-start! gap-3! bg-[var(--ui-surface)]! text-left! font-medium! shadow-[var(--ui-shadow-1)]`}
            onClick={() => setOpen(true)}
          >
            <span
              aria-hidden="true"
              class="flex size-7 shrink-0 items-center justify-center rounded-[var(--ui-radius-sm)] bg-[var(--ui-accent-soft)] text-[var(--ui-accent)]"
            >
              {/* the checklist's own mark; the icon set has none */}
              <svg
                class="size-4"
                viewBox="0 0 24 24"
                fill="none"
                stroke="currentColor"
                stroke-width="1.75"
                stroke-linecap="round"
                stroke-linejoin="round"
              >
                <path d="m3 7 2 2 4-4" />
                <path d="M13 6h8" />
                <path d="m3 17 2 2 4-4" />
                <path d="M13 12h8" />
                <path d="M13 18h8" />
              </svg>
            </span>
            <span class="min-w-0 flex-1 truncate text-base!">
              Set up · {setup.left} {setup.left === 1 ? "step" : "steps"} left
            </span>
            <Icon name="right" class="size-3.5 shrink-0 text-[var(--ui-muted)]" />
          </Button>
        </div>
      ) : null}
      {showRow || open ? (
        <Sheet open={open} onClose={() => setOpen(false)} title="Set up">
          {open ? <FirstRunChecklist onPairToken={onPairToken} setup={setup} /> : null}
        </Sheet>
      ) : null}
    </>
  );
}

/** The rows the desk draws, in the order the list shows them: needs you, then working, idle,
 *  stuck and ended. The desk opens `deskRows(shown)[0]` when no session is picked, so the row it
 *  lands on is the list's own first row. */
export function deskRows(shown: RegistrySession[]): RegistrySession[] {
  const g = groupSessions(shown);
  return [...g.needsYou, ...g.working, ...g.idle, ...g.stuck, ...g.ended];
}

/** How long one row's tasks answer stays fresh before the pane asks again. */
const TASKS_TTL_MS = 60_000;

/** One cache entry: the answer, or null when the read failed, stamped when it landed. */
type TasksEntry = { at: number; tasks: SessionTasks | null };

/** Every listed row's tasks, one cache entry per session id: the pane reads each row once when it
 *  first shows and again at most every 60 s while the list stands. An ended row is never asked —
 *  its work is done. A failed read caches null (no chip line) so one bad answer cannot turn into
 *  a re-ask loop. */
function useTasks(listed: RegistrySession[], api: Api): Map<string, TasksEntry> {
  const [cache, setCache] = useState<Map<string, TasksEntry>>(() => new Map());
  const reading = useRef(new Set<string>());
  // no dependency list: the pane repaints on every change, and the walk reads the cache fresh
  useEffect(() => {
    for (const s of listed) {
      if (s.state === "ended") continue;
      const id = s.session_id;
      const seen = cache.get(id);
      if (seen && Date.now() - seen.at < TASKS_TTL_MS) continue;
      if (reading.current.has(id)) continue;
      reading.current.add(id);
      sessionApi(api)
        .tasks(id)
        .then(
          (tasks) => setCache((m) => new Map(m).set(id, { at: Date.now(), tasks })),
          () => setCache((m) => new Map(m).set(id, { at: Date.now(), tasks: null })),
        )
        .finally(() => reading.current.delete(id));
    }
  });
  return cache;
}

/** The Sessions list: the registry's sessions as the three layouts' list pane. The tablet and the
 *  desktop carry their own header — the title with the Select toggle at its end (the phone's shell
 *  and the desktop's bar draw the rest); the desktop's j and k move a selection Enter opens and
 *  a, d and r answer, Select shows the checkboxes that pick sessions to send new work to, and the
 *  phone's long-press picks the same. The session the pane sits beside is `openId`, its row filled
 *  and marked `aria-current="page"`. While the first-run checklist has steps left and stands
 *  undismissed, the set-up row rides under the machine chips and opens the checklist in its sheet;
 *  a minted passkey hands its token back up through `onPairToken` to the pairing screen. */
export function SessionsPane({
  layout,
  openId,
  onPairToken,
}: {
  layout: Layout;
  openId?: string | undefined;
  onPairToken?: (token: string) => void;
}): JSX.Element {
  const { registry, now, api } = useHuddle();
  const act = useNeedsAct();
  const st = registry.getState();
  // the machine chip the list filters to, kept between visits; the empty string is All
  const [machine, setMachine] = useState<string>(() => readPref(localStorage, "sessionsmachine", ""));
  // the search box narrows the rows and starts empty every visit
  const [query, setQuery] = useState("");
  const [selId, setSelId] = useState<string | null>(null);
  const [picked, setPicked] = useState<readonly string[]>([]);
  // Select shows the rows' checkboxes; Done hides them and clears the picks
  const [selecting, setSelecting] = useState(false);
  const [replyTo, setReplyTo] = useState<string | null>(null);
  const [sending, setSending] = useState(false);
  const [linking, setLinking] = useState(false);
  const hold = useRef<number | null>(null);
  // the set-up row's steps: one watch, handed to the row's sheet and the checklist inside it, so
  // a dismissal there drops the row with it
  const setup = useFirstRun();

  // the search matches the title, project, branch, machine and the session's task or prompt line,
  // case-insensitively; the machine chip holds one machine, or All
  const q = query.trim().toLowerCase();
  const hit = (s: RegistrySession): boolean =>
    q === "" ||
    [sessionName(s), projectName(s.project), s.branch, s.machine, s.task, s.prompt_line].some((t) =>
      (t ?? "").toLowerCase().includes(q),
    );
  const shown = (list: RegistrySession[]): RegistrySession[] =>
    list.filter((s) => (machine === "" || s.machine === machine) && hit(s));
  // an open item holds a session in Needs you even when its last live row reads idle; the groups
  // keep the sections' buckets and `listed` the flat order the desk reads and the rows draw
  const listed = deskRows(shown(st.sessions?.map((s) => ({ ...s, state: shownState(s, st.needs) })) ?? []));
  const groups = groupSessions(listed);
  const tasks = useTasks(listed, api);
  const selIdx = listed.findIndex((r) => r.session_id === selId);
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

  // no dependency list: the pane repaints on every change, and the handler reads the rows fresh
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
      if (listed.length === 0) return;
      e.preventDefault();
      if (e.key === "Enter") {
        const s = listed[cur];
        if (s) location.hash = openHref(s);
        return;
      }
      if (e.key === "j" || e.key === "k") {
        const next = Math.min(listed.length - 1, Math.max(0, cur + (e.key === "j" ? 1 : -1)));
        setSelId(listed[next]?.session_id ?? null);
        return;
      }
      const s = listed[cur];
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

  /** One group's rows, either layout the artboard's one card: the phone's needs-you cards stack
   *  bare, every other group sits in one rounded card whose hairlines stop short of the rows'
   *  leading edge. */
  const rowsOf = (list: RegistrySession[]): JSX.Element =>
    layout === "phone" ? (
      list[0]?.state === "needs-you" ? (
        <div class="flex flex-col gap-3">{list.map((s) => rowOf(s))}</div>
      ) : (
        <div class="overflow-hidden rounded-[var(--ui-radius)] bg-[var(--ui-surface)] shadow-[var(--ui-shadow-1)]">
          {list.map((s, i) => (
            <Fragment key={s.session_id}>
              {i > 0 ? <div class="ml-16 h-px bg-[var(--ui-line)]" aria-hidden="true" /> : null}
              {rowOf(s)}
            </Fragment>
          ))}
        </div>
      )
    ) : (
      <div class="overflow-hidden rounded-[var(--ui-radius)] bg-[var(--ui-surface)] shadow-[var(--ui-shadow-1)]">
        {list.map((s, i) => (
          <Fragment key={s.session_id}>
            {i > 0 ? <div class="ml-4 h-px bg-[var(--ui-line)]" aria-hidden="true" /> : null}
            {rowOf(s)}
          </Fragment>
        ))}
      </div>
    );

  /** One row, as its group draws it, in the shell its layout gives it. */
  const rowOf = (s: RegistrySession): JSX.Element => {
    const sel = layout === "desktop" && listed[cur]?.session_id === s.session_id;
    const open = layout !== "phone" && openId === s.session_id;
    const held = tasks.get(s.session_id)?.tasks ?? null;
    const row =
      layout === "phone" ? (
        s.state === "needs-you" ? (
          <NeedsCard s={s} now={now} act={act} tasks={held} />
        ) : (
          <PhoneRow s={s} now={now} sel={sel} tasks={held} />
        )
      ) : (
        <DeskRow s={s} now={now} sel={sel} open={open} tasks={held} />
      );
    return (
      <RowShell
        key={s.session_id}
        layout={layout}
        s={s}
        selecting={selecting}
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
    // the Insights card rides after the needs-you cards, before the first other section
    const firstOther = SECTIONS.find(([key]) => key !== "needsYou" && groups[key].length > 0)?.[0] ?? null;
    const cards = (
      <div class="flex flex-col gap-3 px-3 pt-3">
        <InsightsCard />
      </div>
    );
    return (
      <>
        {SECTIONS.filter(([key]) => groups[key].length > 0).map(([key, label]) => (
          <Fragment key={key}>
            {/* the Insights card follows what needs the owner, or leads when nothing does */}
            {key === firstOther ? cards : null}
            <section aria-labelledby={`sessions-${key}`} class="flex flex-col gap-2 px-3 pt-6">
              <h2 id={`sessions-${key}`} class={`m-0 px-1 text-sm font-semibold ${MUTED}`}>
                {label} · {groups[key].length}
              </h2>
              {rowsOf(groups[key])}
            </section>
          </Fragment>
        ))}
        {firstOther === null ? cards : null}
      </>
    );
  };

  return (
    <div class="flex min-h-0 min-w-0 flex-col bg-[var(--ui-surface)]" data-sessions-pane="">
      {layout !== "phone" ? (
        <header class="flex shrink-0 items-center justify-between gap-3 px-4 pt-5 pb-3">
          <h1 class="text-[20px] font-bold leading-[1.2] tracking-tight">Sessions</h1>
          {layout === "tablet" ? (
            <MachinesLine live={st.live} text={machinesLine(layout, machinesOf(st.sessions))} />
          ) : null}
          <Button
            variant="ghost"
            data-select-toggle=""
            pressed={selecting}
            onClick={() => {
              if (selecting) setPicked([]);
              setSelecting(!selecting);
            }}
          >
            {selecting ? "Done" : "Select"}
          </Button>
        </header>
      ) : null}
      <div class={`flex shrink-0 flex-col gap-2 border-b ${LINE} px-4 py-2`}>
        {/* the field's own label reads out with the input; the placeholder carries the visible name.
         *  The mock's magnifier leads the field: the icon is painted over the input from this
         *  wrapper because the shared Field forwards no class of its own, and the input's own left
         *  padding makes the room. The phone's field stands the mock's 48 px, the desk's 40 px. */}
        <div
          data-session-search=""
          class={`relative [&_.ui-field-label]:sr-only [&_.ui-input]:pl-11! ${
            layout === "phone" ? "[&_.ui-input]:h-12!" : ""
          }`}
        >
          <Icon
            name="search"
            class="pointer-events-none absolute top-1/2 left-4 size-4 -translate-y-1/2 text-[var(--ui-muted)]"
          />
          <Field
            label="Search sessions"
            type="search"
            placeholder="Search sessions"
            value={query}
            onInput={setQuery}
          />
        </div>
        <div class="flex gap-2 overflow-x-auto" data-machine-chips="">
          {["", ...machinesOf(st.sessions)].map((m) => {
            const on = machine === m;
            // the span paints the contract's 28 px chip on every pointer; the button is the box a
            // thumb presses, so on a coarse pointer it grows to the --ui-tap target around the pill
            return (
              <Button
                key={m === "" ? "all" : m}
                variant="secondary"
                data-machine-chip={m === "" ? "all" : m}
                pressed={on}
                class={`h-7! min-h-7! rounded-[var(--ui-radius-sm)]! bg-transparent! p-0! shrink-0 pointer-coarse:h-[var(--ui-tap)]! pointer-coarse:min-h-[var(--ui-tap)]! pointer-coarse:min-w-[var(--ui-tap)]!`}
                onClick={() => {
                  setMachine(m);
                  writePref(localStorage, "sessionsmachine", m);
                }}
              >
                <span
                  class={`inline-flex h-7 items-center rounded-[var(--ui-radius-sm)] px-2.5 text-sm font-medium ${
                    on
                      ? "bg-[var(--ui-text)] text-[var(--ui-bg)]"
                      : "bg-[var(--ui-surface-2)] text-[var(--ui-muted)]"
                  }`}
                >
                  {m === "" ? "All" : m}
                </span>
              </Button>
            );
          })}
        </div>
      </div>
      <SetupPane setup={setup} onPairToken={onPairToken} layout={layout} />
      <div class="min-h-0 flex-1 overflow-y-auto pb-4" data-sessions-sections="">
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
        ) : listed.length === 0 ? (
          // the welcome is the empty registry's card; a search or a chip that narrows to nothing
          // shows no rows
          layout === "phone" && query === "" && machine === "" ? (
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
