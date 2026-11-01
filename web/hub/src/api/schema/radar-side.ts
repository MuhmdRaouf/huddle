// radar-side.ts — the payloads of Radar's side panels, one valibot schema per read, mirrored from
// the Go types that write them: the Overview's day counts (internal/radar/server/day.go
// `dayAnswer`), the usage and today reports (internal/radar/usage), the alerts
// (internal/radar/shared/alert.go `Alert`), the self-check (internal/radar/server/selfcheck.go
// `selfCheckBody`), the OpenTelemetry read (internal/radar/server/otel.go `OtelAnswer`) and the
// owner's audit page (internal/huddle/hub/audit.go `AuditRow`). The type beside each schema is
// what the callers hold past decode; a field is optional only where the Go tag says omitempty.
import * as v from "valibot";

/** One group's usage: the requests, the token kinds and the money (usage.Totals). */
const UsageTotals = {
  requests: v.number(),
  input: v.number(),
  output: v.number(),
  cacheRead: v.number(),
  cacheWrite: v.number(),
  costUsd: v.number(),
  savingsUsd: v.number(),
};

/** One local day's totals; `date` spells YYYY-MM-DD in the report's own location (usage.Day). */
export const UsageDay = v.object({ ...UsageTotals, date: v.string() });
export type UsageDay = v.InferOutput<typeof UsageDay>;

/** One key's totals and its zero-filled daily series, the rows of byModel, byProject, byKind and
 *  byHarness (usage.Slice). */
export const UsageSlice = v.object({ ...UsageTotals, key: v.string(), days: v.array(UsageDay) });
export type UsageSlice = v.InferOutput<typeof UsageSlice>;

/** One project's session in the top list, its label the head of the session's first prompt
 *  (usage.Session). The harness is the agent CLI's own name, so it stays a string. */
export const UsageSession = v.object({
  ...UsageTotals,
  sessionId: v.string(),
  label: v.string(),
  project: v.string(),
  harness: v.string(),
  lastTs: v.number(),
});
export type UsageSession = v.InferOutput<typeof UsageSession>;

/** One window's usage: the daily series, the totals, the breakdowns by model, project, kind and
 *  harness, each project's top sessions, and the honest flags about prices (usage.Report). */
export const UsageReport = v.object({
  from: v.string(),
  to: v.string(),
  days: v.array(UsageDay),
  totals: v.object(UsageTotals),
  byModel: v.array(UsageSlice),
  byProject: v.array(UsageSlice),
  byKind: v.array(UsageSlice),
  byHarness: v.array(UsageSlice),
  topSessions: v.record(v.string(), v.array(UsageSession)),
  approximate: v.boolean(),
  unpriced: v.number(),
  pricesRetrieved: v.string(),
});
export type UsageReport = v.InferOutput<typeof UsageReport>;

/** The current day as /api/usage/today serves it: the requests in each of its clock hours, its
 *  costliest sessions, and the honest flags about prices (usage.TodayReport). */
export const TodayReport = v.object({
  date: v.string(),
  hourlyRequests: v.array(v.number()),
  sessions: v.array(UsageSession),
  approximate: v.boolean(),
  unpriced: v.number(),
});
export type TodayReport = v.InferOutput<typeof TodayReport>;

/** One thing worth the user's attention right now (shared.Alert); agentId and costUsd are null
 *  when the alert has no session or no known cost. */
export const Alert = v.object({
  id: v.string(),
  kind: v.picklist([
    "stuck",
    "loop",
    "retry_storm",
    "budget",
    "context",
    "upstream",
    "quota_nearly_used",
    "plugin_behind",
  ]),
  sessionId: v.string(),
  agentId: v.nullable(v.string()),
  project: v.string(),
  since: v.number(),
  detail: v.string(),
  costUsd: v.nullable(v.number()),
  severity: v.picklist(["warn", "err"]),
});
export type Alert = v.InferOutput<typeof Alert>;

/** The alerts route's answer; the list is the active alerts, empty when all is quiet. */
export const AlertsAnswer = v.object({ alerts: v.array(Alert) });
export type AlertsAnswer = v.InferOutput<typeof AlertsAnswer>;

/** The day route's answer: the most sessions alive at once today and the cards the boards moved
 *  to done (server.dayAnswer). */
export const DayAnswer = v.object({ peakSessions: v.number(), cardsDone: v.number() });
export type DayAnswer = v.InferOutput<typeof DayAnswer>;

/** One side's counters of the self-check (ingest.DaySums). */
export const SelfCheckCounts = v.object({
  requests: v.number(),
  input: v.number(),
  output: v.number(),
  cacheRead: v.number(),
  cacheWrite: v.number(),
});
export type SelfCheckCounts = v.InferOutput<typeof SelfCheckCounts>;

/** The self-check route's answer; driftPct is null when the transcripts hold no call that day
 *  (server.selfCheckBody). */
export const SelfCheck = v.object({
  day: v.string(),
  radar: SelfCheckCounts,
  transcripts: SelfCheckCounts,
  driftPct: v.nullable(v.number()),
});
export type SelfCheck = v.InferOutput<typeof SelfCheck>;

/** One model's totals as the export reported them (store.OtelModelTotals). */
export const OtelModelTotals = v.object({
  model: v.string(),
  input: v.number(),
  output: v.number(),
  cacheRead: v.number(),
  cacheCreation: v.number(),
  costUsd: v.number(),
  sessions: v.number(),
});
export type OtelModelTotals = v.InferOutput<typeof OtelModelTotals>;

/** One data point of Claude Code's OpenTelemetry export (shared.OtelRow); `type` is the token
 *  kind, absent on a cost point. */
export const OtelRow = v.object({
  source: v.string(),
  metric: v.string(),
  sessionId: v.string(),
  model: v.string(),
  type: v.optional(v.string()),
  value: v.number(),
  startTs: v.number(),
  ts: v.number(),
  cumulative: v.boolean(),
});
export type OtelRow = v.InferOutput<typeof OtelRow>;

/** The /api/otel answer; rows is null while no export has landed (the nil slice Go marshals) */
export const OtelAnswer = v.object({
  source: v.string(),
  points: v.number(),
  byModel: v.array(OtelModelTotals),
  rows: v.nullable(v.array(OtelRow)),
});
export type OtelAnswer = v.InferOutput<typeof OtelAnswer>;

/** One audit row as GET /api/audit answers it; the detail is the action's own JSON, null when it
 *  carried none (hub.AuditRow). */
export const AuditRow = v.object({
  id: v.number(),
  at: v.string(),
  actor: v.string(),
  action: v.string(),
  target: v.string(),
  detail: v.unknown(),
});
export type AuditRow = v.InferOutput<typeof AuditRow>;

/** The audit read's page envelope: the raw entries, each checked as an AuditRow by its reader. */
export const AuditEntries = v.object({ entries: v.array(v.unknown()) });
export type AuditEntries = v.InferOutput<typeof AuditEntries>;
