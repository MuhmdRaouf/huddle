/**
 * Colours and icons by meaning. Every colour here comes from the tokens.css roles, so it follows the light
 * and dark schemes and the Catppuccin flavour: the status tones are the tokens' ok, warn, fail, info and
 * muted marks, and the eight chart series are radar.css's --radar-series-*, the accent and the tones and
 * mixes of them. Status is never colour-alone: every tone travels with an icon or a word.
 */

import type { IconName } from "./icons.ts";
import type { AlertKind } from "./shared/api.ts";

/** Chart series in order: blue, violet, green, orange, teal, rose, amber, slate. radar.css builds
 *  each one from the token roles. */
export const SERIES = [
  "var(--radar-series-1)",
  "var(--radar-series-2)",
  "var(--radar-series-3)",
  "var(--radar-series-4)",
  "var(--radar-series-5)",
  "var(--radar-series-6)",
  "var(--radar-series-7)",
  "var(--radar-series-8)",
] as const;

/** One colour per token kind, used by the token-flow chart, the drawer and every stacked bar. */
export const TOKEN_KINDS = [
  { key: "input", label: "Input", color: SERIES[0] },
  { key: "output", label: "Output", color: SERIES[1] },
  { key: "cacheRead", label: "Cache read", color: SERIES[4] },
  { key: "cacheWrite", label: "Cache write", color: SERIES[3] },
] as const;

/** Latency against the view's p95: under 60% is fine, under 80% is a warning, the rest is slow. */
export function latencyClass(ratio: number): string {
  if (ratio < 0.6) return "var(--ui-ok)";
  if (ratio < 0.8) return "var(--ui-warn)";
  return "var(--ui-fail)";
}

export type StatusTone = "ok" | "warn" | "err" | "idle" | "info";

/** Fill colour of a tone (dots, bars, tinted icon backgrounds). */
export const STATUS_COLOR: Record<StatusTone, string> = {
  ok: "var(--ui-ok)",
  warn: "var(--ui-warn)",
  err: "var(--ui-fail)",
  idle: "var(--ui-muted)",
  info: "var(--ui-info)",
};

type EventMeta = { icon: IconName; tone: StatusTone; label: string };

const EVENTS: Record<string, EventMeta> = {
  SessionStart: { icon: "play", tone: "ok", label: "Session started" },
  SessionEnd: { icon: "stop", tone: "idle", label: "Session ended" },
  UserPromptSubmit: { icon: "message", tone: "info", label: "Prompt" },
  PreToolUse: { icon: "wrench", tone: "info", label: "Tool started" },
  PostToolUse: { icon: "check", tone: "ok", label: "Tool finished" },
  PostToolUseFailure: { icon: "close", tone: "err", label: "Tool failed" },
  SubagentStart: { icon: "branch", tone: "info", label: "Subagent started" },
  SubagentStop: { icon: "merge", tone: "idle", label: "Subagent finished" },
  Stop: { icon: "pause", tone: "idle", label: "Turn ended" },
  PreCompact: { icon: "fold", tone: "warn", label: "Compacting" },
  Notification: { icon: "bell", tone: "warn", label: "Notification" },
  route: { icon: "arrows", tone: "info", label: "Routed" },
  Interrupted: { icon: "stop", tone: "idle", label: "Interrupted by you" },
  Notice: { icon: "alert", tone: "err", label: "Claude Code notice" },
};

/** Icon, tone and plain-language label per hook event kind; unknown kinds keep their raw name. */
export function eventMeta(kind: string): EventMeta {
  return EVENTS[kind] ?? { icon: "dot", tone: "idle", label: kind === "" ? "Event" : kind };
}

/** Plain-language label per alert kind, as the strip and the alerts view show it. */
const ALERT_LABEL: Record<AlertKind, string> = {
  stuck: "Stuck",
  loop: "Loop",
  retry_storm: "Retry storm",
  context: "Context nearly full",
  budget: "Budget",
  upstream: "Claude cannot reach Anthropic through huddle-router",
  quota_nearly_used: "Quota nearly used",
  plugin_behind: "Plugin cache behind the binary",
};

export function alertLabel(kind: AlertKind): string {
  return ALERT_LABEL[kind];
}

/** A budget's tone: over the limit is an error, within 20% of it a warning. */
export function budgetTone(pct: number): StatusTone {
  if (pct >= 100) return "err";
  if (pct >= 80) return "warn";
  return "ok";
}

/** Provider → series colour, used consistently in charts, badges and the agents tree. */
const PROVIDER_COLOR: Record<string, string> = {
  Anthropic: SERIES[3],
  "Z.ai": SERIES[0],
  Moonshot: SERIES[4],
  DeepSeek: SERIES[1],
  MiniMax: SERIES[5],
  Qwen: SERIES[7],
  other: STATUS_COLOR.idle,
  route: SERIES[0],
};

export function providerColor(provider: string | null | undefined): string {
  return PROVIDER_COLOR[provider ?? "other"] ?? STATUS_COLOR.idle;
}

/** One series colour per model, hashed from its id: the same model paints the same segment everywhere. */
export function modelColor(model: string): string {
  let hash = 0;
  for (let at = 0; at < model.length; at += 1) hash = (hash * 31 + model.charCodeAt(at)) >>> 0;
  return SERIES[hash % SERIES.length] ?? SERIES[0];
}
