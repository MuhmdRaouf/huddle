/**
 * The shapes the dashboard's HTTP API serves and its UI keeps, declared once: the server modules that
 * produce these records re-export them from here, and the browser imports them as types only, so the
 * dashboard bundle never carries server code. Pure types — nothing here runs.
 */
import type { Activity, RouterEventKind, RouterEventRecord, Tokens } from "./model.ts";

export type Settings = { notifications: boolean; historyRetentionDays: number };

export type SessionListItem = {
  id: string;
  project: string | null;
  cwd: string | null;
  name: string | null;
  branch: string | null;
  repo: string | null;
  parentSessionId: string | null;
  startedAt: number | null;
  endedAt: number | null;
  live: boolean;
  /** Working or idle, from Claude Code's registry; null when the store cannot tell. */
  status: "working" | "idle" | null;
  /** Request activity in 48 buckets: a live session's last 15 minutes, an ended one's whole span. */
  activity: Activity;
  model: string | null;
  agentCount: number;
  /** How many of those agents are live now (the card's "N live"); the total stays its own number. */
  liveAgentCount: number;
  requestCount: number;
  tokens: number;
  lastAt: number;
  external: boolean;
  title: string | null;
  /** Estimated USD at list price; null when none of its models is priced. */
  costUsd?: number | null;
  /** The same requests' cost split by model family — the session header's "X Claude / Y GLM" reads
   *  this, so it covers the very requests the card's `costUsd` sums and adds up to it. */
  costSplitUsd?: CostSplit;
  /** What the session's main agent is doing right now — its newest call's one-line what and when that
   *  request went out — for the rail card's now line. Null once the session is no longer live. */
  now?: { what: string | null; ts: number } | null;
  /** How full that same call's context window is, and how large the window is, for the card's gauge.
   *  Null once the session is no longer live. */
  context?: { used: number; window: number } | null;
};

export type AlertKind =
  | "stuck"
  | "loop"
  | "retry_storm"
  | "budget"
  | "context"
  | "upstream"
  | "quota_nearly_used"
  | "plugin_behind";

export type Alert = {
  id: string;
  kind: AlertKind;
  sessionId: string;
  agentId: string | null;
  project: string;
  since: number;
  detail: string;
  costUsd: number | null;
  /** "warn" or "err": how loud the dashboard shows it. */
  severity: "warn" | "err";
};

export type Period = "day" | "week" | "month";
export type BudgetAction = "warn" | "stop";

export type Budget = {
  id: string;
  /** "total" or "provider:<plugin name>", e.g. "provider:zai". */
  scope: string;
  period: Period;
  limitUsd: number;
  action: BudgetAction;
};

export type BudgetSpend = {
  id: string;
  spentUsd: number;
  limitUsd: number;
  /** Spend as a percentage of the limit (100 = the limit), one decimal. */
  pct: number;
  scope: string;
  period: Period;
  action: BudgetAction;
  /** When the current period began (epoch ms). */
  periodStart: number;
};

export type BudgetStatus = {
  version: 1;
  updatedAt: number;
  stopped: string[];
  spend: BudgetSpend[];
};

/** One session's plan-limits report: the two windows' used percentages and their reset times
 *  (unix seconds), any of them null when the session did not know. Claude Code hands its
 *  statusline command this reading; the statusline word posts it here. */
export type QuotaSnapshot = {
  session_id: string;
  five_hour: number | null;
  five_hour_resets_at: number | null;
  seven_day: number | null;
  seven_day_resets_at: number | null;
  at: number;
};

export type AdvisorModelRow = {
  model: string;
  flash: string | null;
  runs: number;
  costUsd: number | null;
  flashCostUsd: number | null;
};

export type AdvisorReport = {
  /** Subagent runs on a main model that were looked at. */
  runsChecked: number;
  /** Of those, the ones that look flash-sized. */
  candidates: number;
  /** Estimated saving had the candidates run on their flash sibling; null when none of them is priced. */
  savingUsd: number | null;
  byModel: AdvisorModelRow[];
  examples: { sessionId: string; agentId: string; model: string; requests: number; outputTokens: number }[];
};

export type AttributionRange = "day" | "week" | "month";

export type AttributionRow = {
  key: string;
  label: string;
  requests: number;
  tokens: Tokens;
  /** Estimated USD at list price; null when no request in the group has a price. */
  costUsd: number | null;
  /** Requests in the group whose model the price table does not know. */
  unpriced: number;
};

/** One drill-down row of the costs tree: a repo, a session, an agent or a model, its summed usage, the model
 *  mix behind it and its children. A job session sits under the session that submitted it; its numbers stay
 *  its own, so no row is ever counted twice. */
export type AttributionNode = AttributionRow & {
  kind: "repo" | "session" | "agent" | "model";
  /** A second word when there is one: the agent type for an agent row. */
  note: string | null;
  /** True when a request behind the node priced at its family's rate: the catalog does not know its model. */
  approximate: boolean;
  /** Tokens per model behind this node, heaviest first: the stacked model-mix bar. */
  mix: { model: string; tokens: number }[];
  children: AttributionNode[];
};

export type NodeKind = "main" | "subagent" | "job";

/** The columns every tree node carries, joined with the roll-ups a read adds. */
export type NodeFields = {
  id: string;
  kind: NodeKind;
  parentId: string | null;
  sessionId: string;
  agentId: string;
  label: string | null;
  agentType: string | null;
  description: string | null;
  project: string | null;
  cwd: string | null;
  model: string | null;
  provider: string | null;
  toolUseId: string | null;
  spawnDepth: number | null;
  jobState: string | null;
  repo: string | null;
  branch: string | null;
  name: string | null;
  parentSessionId: string | null;
  startedAt: number | null;
  endedAt: number | null;
  lastAt: number;
};

/** A top-level node with roll-ups over its whole tree. */
export type RootSummary = NodeFields & {
  requests: number;
  tokens: Tokens;
  nodes: number;
  liveNodes: number;
  lastAt: number;
  /** Request activity over 48 buckets, grouped in SQL over the whole tree. */
  activity: Activity;
};

/** Every node of one tree, each with its own request roll-up and live flag. */
export type TreeNodeRow = NodeFields & {
  live: boolean;
  requests: number;
  tokens: Tokens;
};

/** The flow a chart draws: one request count and one series per token kind, zero-filled, oldest bucket
 *  first. A request past the last bucket (the window's right edge, or a cap) counts in that last bucket. */
export type FlowSeries = {
  from: number;
  to: number;
  bucketMs: number;
  requests: number[];
  kinds: { input: number[]; output: number[]; cacheRead: number[]; cacheWrite: number[] };
};

/** One turn of the conversation as the model saw it: one stored side of one request. */
export type ContextMessage = {
  role: "user" | "assistant";
  /** The first block's type ("text", "tool_result", "tool_use", "thinking", …): what the turn is made of. */
  kind: string;
  requestId: string;
  ts: number;
  /** The stored side's own byte size, the way content() counts it. */
  bytes: number;
  /** The first line of the first block that carries text, for the collapsed row. */
  preview: string;
  /** The side's blocks, wire shapes as stored (their texts are capped at capture). */
  blocks: unknown[];
};

/** A rebuilt conversation: every message up to and including the request's input, newest last. */
export type ContextAnswer = {
  requestId: string;
  nodeId: string;
  model: string;
  totals: {
    messages: number;
    /** The whole conversation's bytes divided by four, the usual characters-per-token rule of thumb. */
    approxTokens: number;
    /** The request's own reported usage; null when the provider sent none. */
    usage: { input: number; output: number; cacheRead: number; cacheWrite: number } | null;
    /** How much of that usage was cache (reads plus writes); 0 without a usage report. */
    cacheTokens: number;
  };
  messages: ContextMessage[];
  /** The cursor of the page before this one ("<ts>:<requestId>:<side>"), null at the conversation's start. */
  next: string | null;
};

/** One request's captured prompt: the system prompt and tool definitions the request carried, read back
 *  decompressed, with the plain size the capture weighed and the response headers the route stored. */
export type CaptureAnswer = {
  hash: string;
  system: unknown;
  tools: unknown;
  bytes: number;
  /** The allow-listed response headers the route event carried; null when it carried none. */
  headers: Record<string, string> | null;
};

export type RouterCounts = Record<RouterEventKind, number>;

export type ProviderHealth = {
  plugin: string;
  counts: RouterCounts;
  /** Events per hour over the window, oldest first: one total per bucket. */
  series: number[];
  /** Per-kind series, same buckets, in ROUTER_EVENT_KINDS order. */
  kindSeries: number[][];
  lastAt: number | null;
  /** The most recent reason given, for the card's subtitle. */
  lastReason: string | null;
};

/** The router part of the hub's GET /health, as the Overview's routers card reads it: whether
 *  huddle-router serves, and the providers its providers.json enables; `providers` is null when
 *  the answer does not name them. */
export type RouterFacts = { running: boolean; providers: string[] | null };

export type RouterHealth = {
  windowMs: number;
  buckets: number;
  providers: ProviderHealth[];
  recent: RouterEventRecord[];
};

/** The three families a session's cost splits into: Claude's models, GLM's, everything else priced. */
export type CostFamily = "claude" | "glm" | "other";

/** Per-family estimated cost; a family nothing priced stays null. The non-null parts of a split over
 *  some requests always add up to `costOfAll` over the same requests. */
export type CostSplit = Record<CostFamily, number | null>;

/** One side of the self-check: a UTC day's model calls and their four token counters. */
export type SelfCheckCounts = {
  requests: number;
  input: number;
  output: number;
  cacheRead: number;
  cacheWrite: number;
};

/** GET /api/selfcheck: today's (UTC) totals as Radar holds them and as the transcripts re-sum them.
 *  `driftPct` is the worst counter's gap as a percent of the transcripts' figure; null when the
 *  transcripts hold no call that day. */
export type SelfCheck = {
  day: string;
  radar: SelfCheckCounts;
  transcripts: SelfCheckCounts;
  driftPct: number | null;
};
