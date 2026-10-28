/**
 * The shared vocabulary every Radar panel is built from, on the web/ui tokens: the card surface with its
 * titled head, badges, status dots, model chips, segmented controls, notices and empty states. Colour
 * arrives only as token roles.
 */

import type { Tone } from "@huddle/ui/badge.tsx";
import { useUiId } from "@huddle/ui/id.ts";
import { modelColor } from "@huddle/ui/model-color.ts";
import type { ComponentChildren, HTMLAttributes } from "preact";
import type { IconName } from "../icons.ts";
import { STATUS_COLOR, type StatusTone } from "../palette.ts";
import { useApp } from "./context.ts";
import { Icon } from "./Icon.tsx";

/** A status dot in a colour; `pulse` marks something live. Always sits next to a word, never alone. */
function Dot({ color, pulse = false }: { color: string; pulse?: boolean }) {
  return (
    <span
      class={`inline-block size-2 shrink-0 rounded-full${pulse ? " motion-safe:animate-pulse" : ""}`}
      style={`background:${color}`}
      data-dot=""
      aria-hidden="true"
    />
  );
}

/** Dot + word, coloured by tone (the word carries the meaning for anyone who cannot see the colour). */
export function Status({ tone, word, pulse = false }: { tone: StatusTone; word: string; pulse?: boolean }) {
  return (
    <span class="inline-flex items-center gap-[7px] whitespace-nowrap" data-status={tone}>
      <Dot color={STATUS_COLOR[tone]} pulse={pulse} />
      <span class="text-[13px]" data-word="">
        {word}
      </span>
    </span>
  );
}

/** The design system's status colour each radar tone reads as. */
export const BADGE_TONE: Record<StatusTone, Tone> = {
  ok: "ok",
  warn: "warn",
  err: "fail",
  idle: "neutral",
  info: "info",
};

/** The design system's badge in the tone's status colour, with an optional icon before the word. */
export function Badge({ text, tone, icon }: { text: string; tone: StatusTone; icon?: IconName }) {
  return (
    <span class={`ui-badge ui-badge-${BADGE_TONE[tone]} gap-1`}>
      {icon !== undefined && <Icon name={icon} class="size-3 shrink-0" />}
      <span>{text}</span>
    </span>
  );
}

/** The short name a model is read by: never a raw id like claude-opus-5-5 when "Opus 5.5" says it. */
export function shortModelName(model: string): string {
  const s = model.toLowerCase().replace(/^[a-z]+:/, "");
  const claude = /(?:^|\b)claude[- ](opus|sonnet|haiku|fable)[- ](\d+)(?:[-.](\d{1,2}))?(?:\b|$)/.exec(s);
  if (claude !== null) {
    const family = `${claude[1]?.charAt(0).toUpperCase()}${claude[1]?.slice(1)}`;
    return claude[3] === undefined ? `${family} ${claude[2]}` : `${family} ${claude[2]}.${claude[3]}`;
  }
  const glm = /^glm-(\d+(?:\.\d+)?)(?:-(\w+))?/.exec(s);
  if (glm !== null) {
    const tier = glm[2] === undefined ? "" : ` ${glm[2].charAt(0).toUpperCase()}${glm[2].slice(1)}`;
    return `GLM ${glm[1]}${tier}`;
  }
  return model;
}

/** A model as its colour dot (modelColor, one palette everywhere) plus the short name; the raw id hovers. */
export function ModelChip({ model }: { model: string }) {
  return (
    <span class="model-chip inline-flex items-center gap-1.5 whitespace-nowrap" title={model}>
      <Dot color={modelColor(model)} />
      <span>{shortModelName(model)}</span>
    </span>
  );
}

/** An icon in a softly tinted rounded square; `color` is a palette token, `sm` the compact row size. */
export function IconTile({ name, color, sm = false }: { name: IconName; color: string; sm?: boolean }) {
  return (
    <span
      class={
        sm
          ? "inline-grid size-[26px] shrink-0 place-items-center rounded-[var(--ui-radius-sm)]"
          : "inline-grid size-8 shrink-0 place-items-center rounded-[var(--ui-radius-sm)]"
      }
      style={`--tone:${color};color:var(--tone);background:color-mix(in srgb, var(--tone) 16%, transparent)`}
    >
      <Icon name={name} />
    </span>
  );
}

/** A panel: the token card with its titled head (icon, title, a quiet subtitle, the controls on the
 *  right) over a padded body, or a flush one for tables and lists; pass no title for a headless panel. */
export function Panel({
  icon,
  title,
  subtitle = null,
  actions = null,
  flush = false,
  class: cls = "",
  children,
}: {
  icon?: IconName | undefined;
  title?: string;
  subtitle?: string | null;
  /** the controls on the right of the head */
  actions?: ComponentChildren;
  /** flush bodies (tables, lists) run edge to edge; the default pads the body */
  flush?: boolean;
  class?: string;
  children?: ComponentChildren;
}) {
  const id = useUiId("radar-panel");
  const titled = title !== undefined;
  const classes = ["ui-card min-w-0", flush ? "overflow-hidden" : "", cls].filter(Boolean).join(" ");
  return (
    <section class={classes} aria-labelledby={titled ? id : undefined}>
      {titled && (
        <div class="ui-card-head">
          {icon !== undefined && <Icon name={icon} class="size-[18px] shrink-0 text-[var(--ui-accent)]" />}
          <h2 id={id} class="ui-card-title min-w-0 truncate">
            {title}
          </h2>
          {subtitle !== null && <span class="ui-card-meta">{subtitle}</span>}
          {actions !== null && actions !== undefined && actions !== false && (
            <div class="ui-card-actions flex-wrap items-center">{actions}</div>
          )}
        </div>
      )}
      {flush ? children : <div class="ui-card-body">{children}</div>}
    </section>
  );
}

export type Segment = {
  label: string;
  value: string;
  on: boolean;
  count?: string;
  /** a colour dot before the label, such as a model's */
  dot?: string;
  /** the hover text, such as a model's raw id */
  title?: string;
};

/** The well a segmented control's buttons sit in. */
export const SEGMENT_GROUP =
  "inline-flex max-w-full flex-wrap gap-1 rounded-[var(--ui-radius)] bg-[var(--ui-surface-2)] p-1";

/** One segment: a ghost token button, a full tap target, that paints the accent while it is pressed. */
export const SEGMENT = "segment ui-btn ui-btn-ghost";

/** A segment's count beside its label. */
const SEGMENT_COUNT = "segment-count ui-badge ui-badge-neutral num";

/** A segmented control: toggle buttons in one well that all run the same action with their own value. */
export function Segmented({
  action,
  label,
  segments,
}: {
  action: string;
  label: string;
  segments: Segment[];
}) {
  const { act } = useApp();
  return (
    // biome-ignore lint/a11y/useSemanticElements: a toolbar of toggles, not a form; a fieldset would bring its own border and legend
    <div class={SEGMENT_GROUP} role="group" aria-label={label}>
      {segments.map((segment) => (
        <button
          key={segment.value}
          type="button"
          class={SEGMENT}
          data-action={action}
          data-value={segment.value}
          aria-pressed={segment.on ? "true" : "false"}
          title={segment.title}
          onClick={() => act(action, segment.value)}
        >
          {segment.dot !== undefined && <Dot color={segment.dot} />}
          <span>{segment.label}</span>
          {segment.count !== undefined && <span class={SEGMENT_COUNT}>{segment.count}</span>}
        </button>
      ))}
    </div>
  );
}

/** The tones a notice reads in: a failure, a caution, or news that went well. */
type NoticeTone = "err" | "warn" | "ok";

const NOTICE_CLASS: Record<NoticeTone, string> = {
  err: "border-[var(--ui-fail)] bg-[var(--ui-fail-soft)] text-[var(--ui-fail-ink)]",
  warn: "border-[var(--ui-warn)] bg-[var(--ui-warn-soft)] text-[var(--ui-warn-ink)]",
  ok: "border-[var(--ui-ok)] bg-[var(--ui-ok-soft)] text-[var(--ui-ok-ink)]",
};

/** A line that says something went wrong, needs a look, or went well, on its tone's soft fill. */
export function Notice({
  tone,
  role = "alert",
  class: cls = "",
  children,
}: {
  tone: NoticeTone;
  role?: "alert" | "status";
  class?: string;
  children?: ComponentChildren;
}) {
  return (
    <div
      role={role}
      data-notice={tone}
      class={`flex flex-wrap items-center gap-2 rounded-[var(--ui-radius)] border px-3 py-2.5 text-sm ${NOTICE_CLASS[tone]} ${cls}`.trim()}
    >
      {children}
    </div>
  );
}

/** An empty state that says what is missing, what makes it appear, and the one verb that gets there. */
export function EmptyState({
  title,
  hint,
  icon = "inbox",
  action,
}: {
  title: string;
  hint: string;
  icon?: IconName;
  /** the one verb button, when the view has somewhere to go */
  action?: { label: string; run: () => void };
}) {
  return (
    <div class="flex flex-col items-center gap-1 px-5 pt-10 pb-11 text-center" data-empty="">
      <span class="mb-2 inline-grid size-11 place-items-center rounded-full border border-[var(--ui-line)] bg-[var(--ui-surface-2)] text-[var(--ui-muted)]">
        <Icon name={icon} class="size-5" />
      </span>
      <p class="m-0 font-semibold" data-empty-title="">
        {title}
      </p>
      <p class="m-0 max-w-[46ch] text-[13px] text-[var(--ui-muted)]" data-empty-hint="">
        {hint}
      </p>
      {action !== undefined && (
        <button type="button" class="ui-btn ui-btn-secondary mt-2" onClick={action.run}>
          {action.label}
        </button>
      )}
    </div>
  );
}

/** Inline code-ish text (model ids, hosts, session ids): the only place the monospace face appears. */
export function Code({
  text,
  class: cls = "",
  ...rest
}: { text: string; class?: string } & HTMLAttributes<HTMLSpanElement>) {
  return (
    <span class={cls === "" ? "ui-mono text-[0.86em]" : `ui-mono text-[0.86em] ${cls}`} {...rest}>
      {text}
    </span>
  );
}
