// Now.tsx — the Session screen: the header block (the title with its where line, the status
// line "State · model · context · bypass", and the Now card), the ask its open needs-you item
// carries, and the session's transcript — its own prompts, replies and folded tool runs — or,
// for a session whose transcript the hub cannot read and whose harness is not Claude Code, the
// stream of its hook events; and the desktop's right rail (the session's Now, the teammates it
// runs, the linked sessions). One tree serves the three layouts: the phone paints the session
// alone, the tablet and the desktop paint it beside the list, and the desktop adds the rail.
// Session text is rendered as text, never HTML (SPEC 9).
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
  harnessOf,
  openItemOf,
  type NeedItem,
  type RegistryDetail,
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
import { useConversation, useTurn } from "../session/conversation.ts";
import { TranscriptPane } from "../session/TranscriptPane.tsx";
import type { TurnFacts } from "../session/types.ts";
import { errText, useHuddle } from "./context.tsx";
import type { Layout } from "./layout.ts";
import { NeedsBar, useStepUp } from "./Needs.tsx";
import { nowLine } from "./now-line.ts";
import { HarnessBadge, HooksStaleChip, LINE, MUTED, openHref, StateMark, whereOf } from "./session-parts.tsx";

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

/** The session's stream with its day dividers. It opens at the latest event and never moves by
 *  itself afterwards: the reader keeps their place. */
function StreamPane({ items, now }: { items: StreamItem[]; now: number }): JSX.Element {
  const pane = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const el = pane.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, []);
  let lastDay = "";
  return (
    <div
      ref={pane}
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
function StateLead({
  state,
  role,
  tails,
  warnTail,
}: {
  state: string;
  role: StateRole;
  tails: string[];
  warnTail?: string;
}): JSX.Element {
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
      {tail || warnTail ? (
        <span class={`min-w-0 truncate ${T13} ${MUTED}`}>
          · {tail}
          {warnTail ? (
            <>
              {tail ? " · " : ""}
              <span class="text-[var(--ui-warn-ink)]">{warnTail}</span>
            </>
          ) : null}
        </span>
      ) : null}
    </span>
  );
}

/** The Now card's words: a line that opens "Waiting for you" (or "on you") leads with those
 *  words in the warn ink, the rest in the text ink. */
function NowWords({ line }: { line: string }): JSX.Element {
  const m = /^(Waiting (?:for|on) you)([\s\S]*)$/.exec(line);
  if (!m) return <>{line}</>;
  return (
    <>
      <span class="font-semibold text-[var(--ui-warn-ink)]">{m[1]}</span>
      {m[2]}
    </>
  );
}

/** The Now card: what the session is doing or waits on, and how long it has been so. */
function NowCard({
  d,
  ask,
  turn,
  now,
}: {
  d: RegistryDetail;
  ask: NeedItem | undefined;
  turn: TurnFacts | undefined;
  now: number;
}): JSX.Element | null {
  const line = nowLine(d, ask, turn, now);
  if (!line) return null;
  const age = ageText(ask?.created_at ?? d.last_event_at, now);
  return (
    <div
      data-now-card=""
      class="mt-2 flex min-h-[var(--ui-tap)] min-w-0 items-center gap-3 rounded-[var(--ui-radius)] bg-[var(--ui-surface)] px-4 py-2 shadow-[var(--ui-shadow-1)]"
    >
      <span class={`shrink-0 ${T13} font-semibold ${MUTED}`}>Now</span>
      <span class="min-w-0 flex-1 truncate text-sm">
        <NowWords line={line} />
      </span>
      {age ? <span class={`shrink-0 text-xs ${MUTED}`}>{age}</span> : null}
    </div>
  );
}

/** The session's status line: its state, model, how full its context window is, and "bypass"
 *  when the turn's permission mode runs without asking; the chips ride behind it. */
function StatusLine({
  d,
  state,
  role,
  turn,
  layout,
}: {
  d: RegistryDetail;
  state: string;
  role: StateRole;
  turn: TurnFacts | undefined;
  layout: Layout;
}): JSX.Element {
  const pct = d.context_percent ? Math.round(d.context_percent) : 0;
  const bypass = turn?.permission_mode === BYPASS_MODE;
  return (
    <div data-status-line="" class={`mt-2 flex min-w-0 items-center gap-2 ${T13}`}>
      <StateLead
        state={state}
        role={role}
        tails={[d.model ? shortModel(d.model) : "", pct ? `${pct}% context` : ""]}
        {...(bypass ? { warnTail: "bypass" } : {})}
      />
      {layout === "desktop" ? <HooksStaleChip s={d} /> : null}
      {layout === "desktop" || harnessOf(d).id !== "claude" ? <HarnessBadge s={d} pill /> : null}
    </div>
  );
}

/** The permission mode that runs every call without asking. */
const BYPASS_MODE = "bypassPermissions";

/** The session's header block: the title with its where line (the phone's carries the accent
 *  back link and the hooks-stale chip), the status line and the Now card. */
function SessionHeader({
  d,
  state,
  role,
  ask,
  layout,
}: {
  d: RegistryDetail;
  state: string;
  role: StateRole;
  ask: NeedItem | undefined;
  layout: Layout;
}): JSX.Element {
  const { now } = useHuddle();
  const turn = useTurn(d.session_id, state);
  const title = (
    <div class="flex min-w-0 flex-1 flex-col gap-0.5">
      <div data-session-title="" class="truncate text-lg font-semibold">
        {sessionName(d)}
      </div>
      <div class="flex min-w-0 items-center gap-2">
        <div class={`min-w-0 truncate ${layout === "desktop" ? T13 : "text-xs"} ${MUTED}`}>{whereOf(d)}</div>
        {layout === "desktop" ? null : <HooksStaleChip s={d} />}
      </div>
    </div>
  );
  return (
    <header data-block="header" class={`shrink-0 ${layout === "desktop" ? "px-6 pt-4" : "px-4 pt-3.5"} pb-1`}>
      <div class="flex min-w-0 items-center gap-1 sm:gap-4">
        {layout === "phone" ? (
          <a
            href="#/sessions"
            aria-label="Back to Sessions"
            class="-ml-2 flex size-[var(--ui-tap)] shrink-0 items-center justify-center rounded-[var(--ui-radius)] text-[var(--ui-accent)] outline-none focus-visible:ring-2 focus-visible:ring-[var(--ui-accent)]"
          >
            <Icon name="left" class="size-[22px]" />
          </a>
        ) : null}
        {title}
        <SessionActions session={d} />
      </div>
      <StatusLine d={d} state={state} role={role} turn={turn} layout={layout} />
      <NowCard d={d} ask={ask} turn={turn} now={now} />
    </header>
  );
}

/** What the page reads of the session's words: its transcript, or, when the hub reads none for a
 *  harness other than Claude Code, the stream of its hook events. A Claude Code session waits
 *  for its first read, so the page never paints one pane and then the other. */
function SessionBody({ d }: { d: RegistryDetail }): JSX.Element {
  const { now } = useHuddle();
  const convo = useConversation(d.session_id);
  const held = convo.state.items.length > 0;
  if (held || (harnessOf(d).id === "claude" && !convo.settled)) {
    return <TranscriptPane items={convo.state.items} now={now} />;
  }
  return <StreamPane items={streamItems(d.events ?? [])} now={now} />;
}

/** The Session screen: the header block, the ask its open needs-you item carries (the page's
 *  second block on every layout), the line that says how to restart a session whose hooks
 *  stopped reporting, and the session's transcript. The phone paints the accent back link in
 *  the title's row; the desktop leads with the title alone and the actions at its right. */
export function SessionScreen({ id, layout }: { id: string; layout: Layout }): JSX.Element {
  const { registry } = useHuddle();
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
  const state = shownState(d, st.needs);
  const role = stateRole(state, ask);

  return (
    <div class="flex min-h-0 min-w-0 flex-1 flex-col" data-session="">
      <SessionHeader d={d} state={state} role={role} ask={ask} layout={layout} />
      {ask ? <NeedsBar item={ask} session={d} layout={layout} /> : null}
      {d.hooks_stale ? (
        <p data-resume="" class={`shrink-0 border-b ${LINE} px-4 py-2.5 text-sm sm:px-6`}>
          Hooks stopped reporting: restart this session with{" "}
          <code class="ui-mono">claude --resume {d.session_id}</code>
        </p>
      ) : null}
      <SessionBody key={d.session_id} d={d} />
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
