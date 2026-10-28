// Now.tsx — the Session screen: the header (the phone's two rows with their chips and context
// bar, the desktop's title, where line and status line), the stream of the session's events, and
// the action bar its open ask carries; and the desktop's right rail (the session's Now, the
// teammates it runs, the linked sessions). One tree serves the three layouts: the phone paints
// the session alone, the tablet and the desktop paint it beside the list, and the desktop adds
// the rail. Session text is rendered as text, never HTML (SPEC 9).
import { Button } from "@huddle/ui/button.tsx";
import { Dialog } from "@huddle/ui/dialog.tsx";
import { stampMs } from "@huddle/ui/time.ts";
import { type JSX } from "preact";
import { useEffect, useLayoutEffect, useRef, useState } from "preact/hooks";
import type { Api, ApiError } from "../api.ts";
import { Icon } from "../icons.tsx";
import type { ChannelLinks } from "../links.ts";
import {
  ageText,
  type NeedItem,
  openItemOf,
  type RegistrySession,
  sessionName,
  shortModel,
  shownState,
  type StateRole,
  stateRole,
  stateWord,
  streamItems,
  type StreamItem,
  type ToolCall,
} from "../registry.ts";
import { errText, useHuddle } from "./context.tsx";
import type { Layout } from "./layout.ts";
import { NeedsBar, useStepUp } from "./Needs.tsx";
import {
  ContextBar,
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
      tabIndex={0}
      aria-label="Transcript"
      data-stream=""
    >
      <div class="mx-auto flex max-w-[51.25rem] flex-col gap-3 text-sm leading-relaxed sm:text-[15px]">
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

/** The session line's text size, the one the desk header's and the rail's facts share. */
const T13 = "text-[13px]";

/** The role's ink token for each state's word: the read guaranteed 4.5:1 on every surface, so
 *  the word holds its colour on the desk header's page background as well as on the rail's
 *  surface card. The mark keeps its own fill. */
const STATE_INK: Record<StateRole, string> = {
  "needs-you": "text-[var(--ui-needs-you-ink)]",
  "turn-done": "text-[var(--ui-turn-done-ink)]",
  working: "text-[var(--ui-working-ink)]",
  idle: "text-[var(--ui-idle-ink)]",
  failed: "text-[var(--ui-failed-ink)]",
  cancelled: "text-[var(--ui-cancelled-ink)]",
};

/** A session's state as its mark and word in the role's ink, the facts that ride behind it
 *  muted: the desk header's status line and the rail's Now row draw the same lead. */
function StateLead({ state, role, tails }: { state: string; role: StateRole; tails: string[] }): JSX.Element {
  const tail = tails.filter(Boolean).join(" · ");
  // the word reads as the mock writes it — "Working", "Needs you" — while data-state keeps the
  // raw lowercase state
  const word = stateWord(state);
  return (
    <span class="flex min-w-0 items-center gap-1.5">
      <StateMark state={state} role={role} />
      <span data-state={state} data-role={role} class={`shrink-0 font-semibold ${STATE_INK[role]}`}>
        {word.charAt(0).toUpperCase() + word.slice(1)}
      </span>
      {tail ? <span class={`min-w-0 truncate ${T13} ${MUTED}`}>· {tail}</span> : null}
    </span>
  );
}

/** The Session screen: the header, the line that says how to restart a session whose hooks
 *  stopped reporting, the stream of the session's events, and the action bar its open needs-you
 *  ask carries (the phone's sheet, the wide layouts' inline box). The phone and the tablet paint
 *  the two-row header, so the name keeps its own row (the phone's carries the back link; the
 *  tablet's list stands beside it); the desktop leads with the title alone, the where line and
 *  one status line under it, and the actions at its right. */
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
        <header class="shrink-0 px-6 pt-4">
          <div class="flex min-w-0 items-start gap-4">
            <div class="flex min-w-0 flex-1 flex-col">
              <div data-session-title="" class="truncate text-lg font-semibold">
                {sessionName(d)}
              </div>
              <div class={`mt-1 truncate ${T13} ${MUTED}`}>{whereOf(d)}</div>
              <div data-status-line="" class="mt-2 flex min-w-0 items-center gap-2">
                <StateLead
                  state={shownState(d, st.needs)}
                  role={stateRole(shownState(d, st.needs), ask)}
                  tails={[d.model ? shortModel(d.model) : "", pct ? `${pct}% context` : ""]}
                />
                <HooksStaleChip s={d} />
                <HarnessBadge s={d} pill />
              </div>
            </div>
            <SessionActions session={d} />
          </div>
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

/** The desktop's right rail: the session's Now (its state, how long ago it last reported, and
 *  the facts it carries), the teammates it runs when it has any, and the sessions linked with its
 *  channel. A fact with no value is left out; nothing is drawn as a dash. */
export function NowContext(): JSX.Element | null {
  const { registry, now } = useHuddle();
  const st = registry.getState();
  const d = st.open;
  if (!d) return null;
  const linked = (st.sessions ?? []).filter(
    (s) => s.channel && s.channel === d.channel && s.session_id !== d.session_id,
  );
  const state = shownState(d, st.needs);
  const role = stateRole(state, openItemOf(st.needs, d.session_id));
  const age = ageText(d.last_event_at, now);
  const mates = d.teammates ?? [];
  const facts: { text: string; warn?: boolean }[] = [];
  if (d.model) facts.push({ text: shortModel(d.model) });
  if (d.context_percent) facts.push({ text: `${Math.round(d.context_percent)}% context` });
  if (d.hooks_stale) facts.push({ text: "hooks stale", warn: true });
  if (mates.length) facts.push({ text: `${mates.length} agent${mates.length === 1 ? "" : "s"}` });
  return (
    <aside
      class="flex min-h-0 min-w-0 flex-col gap-6 overflow-y-auto bg-[var(--ui-surface)] px-4 pt-4 pb-6"
      data-now-context=""
    >
      <section>
        <h2 class={`mb-2 ml-1 ${T13} font-semibold ${MUTED}`}>Now</h2>
        <div class="flex flex-col gap-2">
          <StateLead state={state} role={role} tails={age ? [age] : []} />
          {facts.length ? (
            <div data-now-facts="" class={`flex flex-wrap items-center gap-x-1.5 gap-y-1 text-xs ${MUTED}`}>
              {facts.map((f, i) => (
                <span key={f.text} class="flex items-center gap-1.5">
                  {i > 0 ? <span aria-hidden="true">·</span> : null}
                  <span class={f.warn ? "text-[var(--ui-warn)]" : undefined}>{f.text}</span>
                </span>
              ))}
            </div>
          ) : null}
        </div>
      </section>
      {mates.length ? (
        <section>
          <h2 class={`mb-2 ml-1 ${T13} font-semibold ${MUTED}`}>Running</h2>
          <div class="overflow-hidden rounded-[var(--ui-radius)] bg-[var(--ui-surface)] shadow-[var(--ui-shadow-1)]">
            {mates.map((m, i) => (
              <div key={m.name} class={`relative flex min-h-10 items-center gap-2 px-3 ${T13}`}>
                {i > 0 ? (
                  <span aria-hidden="true" class="absolute top-0 right-0 left-4 h-px bg-[var(--ui-line)]" />
                ) : null}
                <span
                  aria-hidden="true"
                  class={`size-1.5 shrink-0 rounded-full bg-current ${STATE_INK[stateRole(m.state)]}`}
                />
                <span class="min-w-0 truncate font-semibold">{m.name}</span>
                <span class={`ml-auto shrink-0 ${MUTED}`}>{stateWord(m.state)}</span>
              </div>
            ))}
          </div>
        </section>
      ) : null}
      {d.channel ? (
        <section class="flex flex-col gap-2.5">
          <h2 class={`mb-1 ml-1 ${T13} font-semibold ${MUTED}`}>Linked in #{d.channel}</h2>
          {linked.length === 0 ? (
            <p class={`${T13} ${MUTED}`}>No other session is linked here yet.</p>
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
    </aside>
  );
}
