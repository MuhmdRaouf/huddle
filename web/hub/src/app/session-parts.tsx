// session-parts.tsx — the pieces the Sessions list and the Session screen share: the state mark
// and chip (shape plus word, so the state never rides on colour alone), the model chip, the
// context bar, the harness badge, the hooks-stale chip, the link that opens a session, and the
// muted ink and hairline both draw with.
import { modelColor } from "@huddle/ui/model-color.ts";
import { type JSX } from "preact";
import {
  harnessOf,
  type NeedItem,
  projectName,
  type RegistrySession,
  shortModel,
  type StateRole,
  stateRole,
  stateWord,
} from "../registry.ts";
import type { SessionTasks } from "../session/types.ts";

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

/** The list row's state chip: the state's word on its role's tint, with the working dot the
 *  artboard gives the live rows. It carries the same data-state and data-role the badge does, so
 *  tests and styles read the two alike. The word reads as the mock writes it — "Working",
 *  "Needs you" — while data-state keeps the raw lowercase state. */
export function RowStateChip({ state }: { state: string }): JSX.Element {
  const role = stateRole(state);
  const word = stateWord(state);
  return (
    <span
      data-state={state}
      data-role={role}
      class={`inline-flex h-7 shrink-0 items-center gap-1.5 rounded-[var(--ui-radius-sm)] px-2.5 text-sm font-medium ${ROLE_CHIP[role]}`}
    >
      {state === "working" ? <span class="size-1.5 rounded-full bg-current" aria-hidden="true" /> : null}
      {word.charAt(0).toUpperCase() + word.slice(1)}
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
export const whereOf = (s: RegistrySession): string =>
  [s.machine, projectName(s.project), s.branch].filter(Boolean).join(" · ");

/** The quiet ink of a secondary line, and the hairline a divider draws. */
export const MUTED = "text-[var(--ui-muted)]";
export const LINE = "border-[var(--ui-line)]";

/** A session's harness as its chip: the mark and the name of the coding agent it runs in, from
 *  the one HARNESSES table. The list's rows and cards draw it small on the line under the name,
 *  so the name keeps the row's width; the header draws it as a pill beside the other chips. */
export function HarnessBadge({ s, pill = false }: { s: RegistrySession; pill?: boolean }): JSX.Element {
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
export function HooksStaleChip({ s }: { s: RegistrySession }): JSX.Element | null {
  if (!s.hooks_stale) return null;
  return (
    <span data-hooks-stale="" class="ui-badge shrink-0 bg-[var(--ui-warn-soft)] text-[var(--ui-warn-ink)]">
      hooks stale
    </span>
  );
}

/** The link that opens one session from the list. */
export const openHref = (s: RegistrySession): string => `#/sessions/${s.session_id}`;

/** The distinct clusters of a tasks answer's cards, in name order; empty when the answer names
 *  no board, so a session whose cards sit nowhere links nowhere. */
export function featuresOf(tasks: SessionTasks): string[] {
  if (!tasks.board) return [];
  return [
    ...new Set(tasks.cards.map((c) => c.cluster).filter((name): name is string => Boolean(name))),
  ].sort();
}

/** One cluster chip: a real link to `#/kanban/<board>/<feature>`, leading with the board's mark
 *  in the muted ink. The list's rows and needs-you cards draw it 28 px tall at a desk and 44 px
 *  on a phone, the control height the phone's rows must meet. */
export function FeatureChips({ tasks, phone }: { tasks: SessionTasks; phone?: boolean }): JSX.Element | null {
  const board = tasks.board;
  if (!board) return null;
  const names = featuresOf(tasks);
  if (names.length === 0) return null;
  return (
    <div
      data-feature-chips=""
      class={`relative z-10 flex flex-wrap items-center gap-1.5${phone ? " pt-0.5" : ""}`}
    >
      {names.map((name) => (
        <a
          key={name}
          data-feature-chip={name}
          href={`#/kanban/${board}/${encodeURIComponent(name)}`}
          title={`Open the ${name} cluster on the board`}
          class={`inline-flex shrink-0 items-center gap-1.5 rounded-lg bg-[var(--ui-surface-2)] px-2.5 text-[13px] font-medium text-[var(--ui-text)] ${
            phone ? "h-11" : "h-7"
          }`}
        >
          {/* the icon set has none: the board's three columns, drawn once per chip */}
          <svg
            viewBox="0 0 24 24"
            aria-hidden="true"
            class={`size-3.5 shrink-0 ${MUTED}`}
            fill="none"
            stroke="currentColor"
            stroke-width="1.75"
            stroke-linecap="round"
            stroke-linejoin="round"
          >
            <path d="M4 4h5v16H4zM10 4h5v10h-5zM16 4h4v7h-4z" />
          </svg>
          <span class="overflow-hidden text-ellipsis whitespace-nowrap">{name}</span>
        </a>
      ))}
    </div>
  );
}
