/**
 * The long-range usage report /api/usage serves, mirrored from internal/radar/usage: one window's
 * zero-filled daily series of requests, tokens and estimated cost by model, project, agent kind and
 * harness, each project's costliest sessions, and the honesty flags about prices. Pure types — the
 * browser imports them as types only, so the bundle never carries server code.
 */

/** One group's usage: the requests, the token kinds and the money. */
export type UsageTotals = {
  requests: number;
  input: number;
  output: number;
  cacheRead: number;
  cacheWrite: number;
  /** Estimated USD at list price; models the price table does not know add nothing. */
  costUsd: number;
  /** Estimated USD the window's cache reads saved against the input rate. */
  savingsUsd: number;
};

/** One local day's totals; `date` spells YYYY-MM-DD in the report's own location. */
export type UsageDay = UsageTotals & { date: string };

/** One key's totals and its zero-filled daily series, the rows of byModel, byProject, byKind and
 *  byHarness. */
export type UsageSlice = UsageTotals & { key: string; days: UsageDay[] };

/** The agent CLIs a request can name; the report spells an untagged one "claude". */
export type UsageHarness = "claude" | "codex" | "opencode";

/** One project's session in its top list; `label` is the head of the session's first prompt. */
export type UsageSession = UsageTotals & {
  sessionId: string;
  label: string;
  project: string;
  /** The agent CLI the session ran on, which picks the resume command it offers. */
  harness: UsageHarness;
  lastTs: number;
};

/** One window's usage: the daily series, the totals, the breakdowns, each project's top sessions,
 *  and the honest flags about prices. */
export type UsageReport = {
  from: string;
  to: string;
  days: UsageDay[];
  totals: UsageTotals;
  byModel: UsageSlice[];
  byProject: UsageSlice[];
  byKind: UsageSlice[];
  byHarness: UsageSlice[];
  topSessions: Record<string, UsageSession[]>;
  /** True when some request's model had no price: the true cost is higher than the estimate. */
  approximate: boolean;
  /** Requests whose model the price table does not know: counted, never costed. */
  unpriced: number;
  /** When the price table was last pulled, as the report's own clock spells it. */
  pricesRetrieved: string;
};

/** The windows the view offers, as the route's days query takes them. */
export type UsageWindow = 7 | 30 | 90;

/** The window sizes the view's tabs carry, in display order. */
export const USAGE_WINDOWS: readonly UsageWindow[] = [7, 30, 90];

/** One 5-hour metering window: the requests that fell in it, the burn rate over the minutes it
 *  has been active, and the linear projection to its end. `start` is the first request's own
 *  hour, floored; `end` is five hours later; a request can sit past `end` when the gaps stayed
 *  under five hours. */
export type UsageBlock = UsageTotals & {
  start: number;
  end: number;
  firstTs: number;
  lastTs: number;
  active: boolean;
  burnTokensPerHour: number;
  burnCostPerHour: number;
  projectedTokens: number;
  projectedCostUsd: number;
};

/** The blocks answer /api/blocks serves, mirrored from internal/radar/usage: the windows
 *  newest first and the honesty flags about prices. */
export type BlocksReport = {
  blocks: UsageBlock[];
  approximate: boolean;
  unpriced: number;
};

/** Which requests the blocks count: Claude's models alone (what a subscription meters), or
 *  every model — the routed provider ones included. */
export type BlocksModels = "claude" | "all";

/** The windows the blocks view offers, as the route's days query takes them. */
export type BlocksWindow = 7 | 30;

/** The window sizes the blocks view's tabs carry, in display order. */
export const BLOCKS_WINDOWS: readonly BlocksWindow[] = [7, 30];
