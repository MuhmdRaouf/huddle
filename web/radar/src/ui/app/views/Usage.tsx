/**
 * The usage view: the long-range picture the Costs view's periods cannot give — every request over the
 * last 7, 30 or 90 days as five tiles, one layered line per model on a daily chart, per-model and
 * per-project tables that drill into their own series, and each project's costliest sessions with the
 * resume command a click away. The estimates carry the honesty labels: `*` at API list price, `~` once
 * a model without a price is in the window, the unpriced models named. Everything renders from the one
 * held report — drill-downs never refetch.
 */

import type { ComponentChildren } from "preact";
import { cacheHitRate, dayLabels, dayWords, layeredMax, nearestIndex } from "../../chart.ts";
import { ESTIMATE_NOTE, fmtAgo, fmtCount, fmtNum, fmtPercent, fmtTokens, fmtUsd } from "../../fmt.ts";
import { modelColor } from "../../palette.ts";
import type { UsageDay, UsageReport, UsageSession, UsageSlice, UsageTotals } from "../../../shared/usage.ts";
import { USAGE_WINDOWS } from "../../../shared/usage.ts";
import type { UsageDrill, UsageMetric } from "../../state.ts";
import { LineChart, StackedBar } from "../Chart.tsx";
import { CopyButton } from "../Content.tsx";
import { useApp } from "../context.ts";
import { Icon } from "../Icon.tsx";
import { EmptyState, ModelChip, Panel, Segmented, shortModelName } from "../kit.tsx";
import { Table, TableHeadStatic, TableRow } from "../table.tsx";

/** Every token kind the report counts, summed: what a window, a model or a day processed. */
export function processedTokens(
  totals: Pick<UsageTotals, "input" | "output" | "cacheRead" | "cacheWrite">,
): number {
  return totals.input + totals.output + totals.cacheRead + totals.cacheWrite;
}

/** The value a chart or a row plots for one totals: the token count, or the money. */
function metricValue(totals: UsageTotals, metric: UsageMetric): number {
  return metric === "cost" ? totals.costUsd : processedTokens(totals);
}

/** A chart's input: one series per visible model over the report's own zero-filled days. */
export type UsageSeries = { keys: string[]; values: number[][]; days: UsageDay[] };

/** One zero-filled series per visible model, in the report's own costliest-first order. */
export function usageSeries(
  report: UsageReport,
  metric: UsageMetric,
  hidden: readonly string[],
): UsageSeries {
  const slices = report.byModel.filter((slice) => !hidden.includes(slice.key));
  return {
    keys: slices.map((slice) => slice.key),
    values: slices.map((slice) => slice.days.map((day) => metricValue(day, metric))),
    days: report.days,
  };
}

/** One model's row of a picked day's split: its value and its share of the day's visible total. */
export type DayRow = { key: string; value: number; share: number };

const ZERO_DAY: UsageDay = {
  date: "",
  requests: 0,
  input: 0,
  output: 0,
  cacheRead: 0,
  cacheWrite: 0,
  costUsd: 0,
  savingsUsd: 0,
};

/** A picked day split over the visible models that ran, in the report's order; a model with nothing
 *  on the day sits out. Every kept row's value is over zero, so the share's total is never zero. */
export function dayRows(
  report: UsageReport,
  date: string,
  metric: UsageMetric,
  hidden: readonly string[],
): DayRow[] {
  const values = report.byModel
    .filter((slice) => !hidden.includes(slice.key))
    .map((slice) => ({
      key: slice.key,
      value: metricValue(slice.days.find((day) => day.date === date) ?? ZERO_DAY, metric),
    }))
    .filter((row) => row.value > 0);
  const total = values.reduce((acc, row) => acc + row.value, 0);
  return values.map((row) => ({ ...row, share: Math.round((row.value / total) * 100) }));
}

/** The models the window ran without a price: their requests and tokens count, their cost does not.
 *  A priced model never shows a zero sum, so a zero-cost slice with requests names itself. */
export function unpricedModels(report: UsageReport): string[] {
  return report.byModel
    .filter((slice) => slice.requests > 0 && slice.costUsd === 0 && slice.savingsUsd === 0)
    .map((slice) => slice.key);
}

/** The date one step through the window from the picked one (1 = later); none picks the nearer end,
 *  the ends stop, an unknown day or an empty window answers none. */
export function stepDay(dates: string[], current: string | null, delta: 1 | -1): string | null {
  const count = dates.length;
  if (count === 0) return null;
  if (current === null) return dates[delta === 1 ? 0 : count - 1] as string;
  const at = dates.indexOf(current);
  if (at < 0) return null;
  const next = Math.min(count - 1, Math.max(0, at + delta));
  return dates[next] as string;
}

/** A money figure with its honesty marks: `~` once the window holds unpriced models, `*` always —
 *  every dollar here is an API list price. */
function Money({ usd, approximate }: { usd: number; approximate: boolean }) {
  return (
    <span title="Estimated at API list price; models without a price are not included">
      {approximate ? "~" : ""}
      {fmtUsd(usd)}
      <span class="text-base-content/60" title="At API list price">
        *
      </span>
    </span>
  );
}

/** A cost cell for one slice: the estimate with its `*`, or "tokens only" when nothing prices it. */
function SliceCost({ slice }: { slice: UsageSlice }) {
  if (slice.requests > 0 && slice.costUsd === 0 && slice.savingsUsd === 0) {
    return (
      <span class="text-base-content/60" title="No price for this model in the table">
        tokens only
      </span>
    );
  }
  return <Money usd={slice.costUsd} approximate={false} />;
}

/** One number on the view: a daisyUI stat like the overview's, its figure big. */
function Stat({
  title,
  value,
  subline,
}: {
  title: string;
  value: ComponentChildren;
  subline: ComponentChildren;
}) {
  return (
    <div class="stats panel w-full">
      <div class="stat w-full p-5">
        <div class="stat-title text-sm font-normal text-base-content/65">{title}</div>
        <div class="stat-value num text-2xl font-semibold">{value}</div>
        <div class="stat-desc text-xs whitespace-normal">{subline}</div>
      </div>
    </div>
  );
}

/** The five tiles: processed tokens (or the period's cost), cached input with its share, uncached
 *  input, output, and what the cache reads saved. */
function Tiles({ report, metric }: { report: UsageReport; metric: UsageMetric }) {
  const { totals } = report;
  const processed = processedTokens(totals);
  const hit = cacheHitRate(totals);
  const money = <Money usd={totals.costUsd} approximate={report.approximate} />;
  return (
    <section
      class="grid grid-cols-1 gap-5 sm:grid-cols-2 xl:grid-cols-5"
      aria-label="Usage totals"
      data-usage-tiles=""
    >
      <Stat
        title={metric === "cost" ? "Est. cost" : "Processed tokens"}
        value={metric === "cost" ? money : fmtNum(processed)}
        subline={metric === "cost" ? `${fmtNum(processed)} tokens` : <>est. {money}</>}
      />
      <Stat
        title="Cached input"
        value={fmtNum(totals.cacheRead)}
        subline={`${hit === null ? "0%" : fmtPercent(hit)} of prompt tokens`}
      />
      <Stat title="Uncached input" value={fmtNum(totals.input)} subline="prompt tokens sent fresh" />
      <Stat title="Output" value={fmtNum(totals.output)} subline="tokens the models wrote" />
      <Stat
        title="Cache savings"
        value={<Money usd={totals.savingsUsd} approximate={report.approximate} />}
        subline="what cache reads saved against the input rate"
      />
    </section>
  );
}

/** The honesty line under the tiles: who ran unpriced, and what that does to the totals. */
function HonestNote({ report }: { report: UsageReport }) {
  const names = unpricedModels(report);
  if (names.length === 0 && !report.approximate) return null;
  const ran =
    report.unpriced > 0
      ? `${fmtCount(report.unpriced, "request")} ran on a model without a price`
      : names.length > 0
        ? "Some requests ran on models without a price"
        : "Some requests ran without a price";
  return (
    <p class="text-sm text-base-content/60" data-usage-note="">
      {ran}
      {names.length > 0 ? `: ${names.join(", ")}` : ""}. Estimates at API list price; the true total is higher
      until every model is priced.
    </p>
  );
}

/** The chart area: click a day to pick it, arrow keys move the pick, Escape clears — and a visually
 *  hidden table carries the same numbers for screen readers. */
function UsagePlot({ data, metric, mark }: { data: UsageSeries; metric: UsageMetric; mark: number | null }) {
  const { state, act } = useApp();
  const dates = data.days.map((day) => day.date);
  const peak = layeredMax(data.values);
  const shown = data.keys.length;
  const label = `Layered daily usage chart, one line per model, ${shown} shown; arrow keys move the picked day, and the table under the chart carries the same numbers`;
  const pick = (event: MouseEvent): void => {
    const box = (event.currentTarget as HTMLElement).querySelector("svg")?.getBoundingClientRect();
    if (box === undefined || box.width <= 0) return;
    const index = nearestIndex(data.values[0] ?? [], event.clientX - box.left, box.width);
    const date = index === null ? null : (data.days[index]?.date ?? null);
    if (date === null) return;
    act("usage-day", state.usageDay === date ? "" : date);
  };
  const onKey = (event: KeyboardEvent): void => {
    if (event.key === "Escape") {
      if (state.usageDay !== null) act("usage-day", "");
      return;
    }
    if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
    const next = stepDay(dates, state.usageDay, event.key === "ArrowRight" ? 1 : -1);
    if (next !== null) act("usage-day", next);
  };
  return (
    <div class="min-w-0">
      <div
        class="relative cursor-pointer"
        role="img"
        aria-label={label}
        tabIndex={0}
        data-usage-plot=""
        onClick={pick}
        onKeyDown={onKey}
      >
        <LineChart
          series={data.keys.map((key, index) => ({
            values: data.values[index] ?? [],
            color: modelColor(key),
          }))}
          width={720}
          height={240}
          mark={mark}
          xLabels={dayLabels(dates)}
          yLabel={peak > 1 ? `${metric === "cost" ? fmtUsd(peak) : fmtTokens(peak)} per day` : null}
        />
      </div>
      <UsageTable data={data} metric={metric} />
    </div>
  );
}

/** The chart's text alternative: one row per day, one column per visible model, for screen readers. */
function UsageTable({ data, metric }: { data: UsageSeries; metric: UsageMetric }) {
  const cell = (value: number): string => (metric === "cost" ? fmtUsd(value) : fmtNum(value));
  return (
    <table class="sr-only" data-usage-table="">
      <caption>{`Daily ${metric} by model`}</caption>
      <thead>
        <tr>
          <th scope="col">Day</th>
          {data.keys.map((key) => (
            <th key={key} scope="col">
              {shortModelName(key)}
            </th>
          ))}
        </tr>
      </thead>
      <tbody>
        {data.days.map((day, index) => (
          <tr key={day.date}>
            <th scope="row">{day.date}</th>
            {data.keys.map((key, series) => (
              <td key={key}>{cell(data.values[series]?.[index] ?? 0)}</td>
            ))}
          </tr>
        ))}
      </tbody>
    </table>
  );
}

/** The series toggles, doubling as share rows: swatch, name, window total, percent — one click hides
 *  a line, and the last visible series refuses. */
function SeriesList({
  report,
  metric,
  hidden,
}: {
  report: UsageReport;
  metric: UsageMetric;
  hidden: string[];
}) {
  const { act } = useApp();
  const visible = report.byModel.filter((slice) => !hidden.includes(slice.key));
  const total = visible.reduce((acc, slice) => acc + metricValue(slice, metric), 0);
  return (
    <ul class="grid gap-1" aria-label="Visible series" data-usage-series="">
      {report.byModel.map((slice) => {
        const isHidden = hidden.includes(slice.key);
        const value = metricValue(slice, metric);
        const name = shortModelName(slice.key);
        return (
          <li key={slice.key}>
            <button
              type="button"
              class="flex w-full items-center gap-2 rounded-field px-1.5 py-1 text-sm hover:bg-base-200/50"
              aria-pressed={isHidden ? "true" : "false"}
              aria-label={`${isHidden ? "Show" : "Hide"} ${name}`}
              title={
                isHidden ? `Show ${name}'s line` : `Hide ${name}'s line; the last visible series always stays`
              }
              disabled={!isHidden && visible.length <= 1}
              onClick={() => act("usage-hide", slice.key)}
            >
              <span
                class="inline-block size-2.5 shrink-0 rounded-full"
                style={`background:${modelColor(slice.key)}`}
                aria-hidden="true"
              />
              <span class="min-w-0 flex-1 truncate text-left">{name}</span>
              <span class="num text-base-content/80">
                {metric === "cost" ? fmtUsd(value) : fmtNum(value)}
              </span>
              <span class="num w-10 text-right text-base-content/60">
                {total > 0 ? `${Math.round((value / total) * 100)}%` : "–"}
              </span>
            </button>
          </li>
        );
      })}
    </ul>
  );
}

/** The picked day's card: the date, each model that ran with its share, and the day's total. */
function DayCard({
  report,
  date,
  metric,
  hidden,
}: {
  report: UsageReport;
  date: string;
  metric: UsageMetric;
  hidden: string[];
}) {
  const { act } = useApp();
  const rows = dayRows(report, date, metric, hidden);
  const total = rows.reduce((acc, row) => acc + row.value, 0);
  const text = (value: number): string => (metric === "cost" ? fmtUsd(value) : fmtNum(value));
  return (
    <div class="panel px-4 py-3" data-usage-day="">
      <div class="flex items-center gap-2">
        <h3 class="min-w-0 flex-1 text-sm font-semibold">{dayWords(date)}</h3>
        <button
          type="button"
          class="btn btn-ghost btn-square btn-sm"
          aria-label="Clear the selected day"
          title="Clear the selected day"
          onClick={() => act("usage-day", "")}
        >
          <Icon name="close" class="size-4" />
        </button>
      </div>
      <ul class="grid gap-1 text-sm">
        {rows.map((row) => (
          <li key={row.key} class="flex items-center gap-2">
            <span
              class="inline-block size-2.5 shrink-0 rounded-full"
              style={`background:${modelColor(row.key)}`}
              aria-hidden="true"
            />
            <span class="min-w-0 flex-1 truncate">{shortModelName(row.key)}</span>
            <span class="num">{text(row.value)}</span>
            <span class="num w-10 text-right text-base-content/60">{row.share}%</span>
          </li>
        ))}
        {rows.length === 0 && <li class="text-base-content/60">Nothing ran that day</li>}
      </ul>
      <p class="mt-1 border-t border-base-content/8 pt-1 text-sm">
        <span class="text-base-content/60">Total</span>
        <span class="num float-right">{text(total)}</span>
      </p>
    </div>
  );
}

const SLICE_COLUMNS: { key: string; label: string; numeric?: boolean }[] = [
  { key: "key", label: "Breakdown" },
  { key: "requests", label: "Requests", numeric: true },
  { key: "tokens", label: "Tokens", numeric: true },
  { key: "cost", label: "Est. cost", numeric: true },
  { key: "share", label: "Share", numeric: true },
];

/** The by-model and by-project tables: one row per slice, its share bar, and the row opens the
 *  slice's own daily series. */
function SliceTable({
  title,
  icon,
  slices,
  metric,
  drillKind,
}: {
  title: string;
  icon: "cpu" | "folder";
  slices: UsageSlice[];
  metric: UsageMetric;
  drillKind: "model" | "project";
}) {
  const { act } = useApp();
  const total = slices.reduce((acc, slice) => acc + metricValue(slice, metric), 0);
  return (
    <Panel icon={icon} title={title} flush>
      <Table dataKey={`usage-${drillKind}`}>
        <TableHeadStatic
          columns={SLICE_COLUMNS.map((column) => ({
            ...column,
            label: column.key === "key" ? (drillKind === "model" ? "Model" : "Project") : column.label,
          }))}
        />
        <tbody>
          {slices.map((slice) => {
            const name = drillKind === "model" ? shortModelName(slice.key) : slice.key;
            const value = metricValue(slice, metric);
            return (
              <TableRow
                key={slice.key}
                onOpen={() => act("usage-drill", JSON.stringify({ kind: drillKind, key: slice.key }))}
                label={`Open ${name}'s daily series`}
              >
                <td class="min-w-0">
                  {drillKind === "model" ? (
                    <ModelChip model={slice.key} />
                  ) : (
                    <span class="block truncate font-medium" title={slice.key}>
                      {name}
                    </span>
                  )}
                </td>
                <td class="num text-right">{fmtNum(slice.requests)}</td>
                <td class="num text-right">{fmtNum(processedTokens(slice))}</td>
                <td class="num text-right">
                  <SliceCost slice={slice} />
                </td>
                <td class="w-32">
                  <span class="flex items-center gap-2">
                    <StackedBar
                      max={total > 0 ? total : 1}
                      segments={[{ value, color: modelColor(slice.key) }]}
                    />
                    <span class="num text-xs text-base-content/60">
                      {total > 0 ? `${Math.round((value / total) * 100)}%` : "–"}
                    </span>
                  </span>
                </td>
              </TableRow>
            );
          })}
        </tbody>
      </Table>
    </Panel>
  );
}

/** A project's costliest sessions, each with the command that resumes it. */
function SessionTable({ sessions, now }: { sessions: UsageSession[]; now: number }) {
  const name = (session: UsageSession): string =>
    session.label === "" ? session.sessionId.slice(0, 8) : session.label;
  return (
    <Table dataKey="usage-sessions">
      <TableHeadStatic
        columns={[
          { key: "label", label: "Session" },
          { key: "last", label: "Last activity", numeric: true },
          { key: "requests", label: "Requests", numeric: true },
          { key: "tokens", label: "Tokens", numeric: true },
          { key: "cost", label: "Est. cost", numeric: true },
          { key: "resume", label: "Resume" },
        ]}
      />
      <tbody>
        {sessions.map((session) => (
          <TableRow key={session.sessionId}>
            <td class="min-w-0">
              <span class="block truncate font-medium" title={session.sessionId}>
                {name(session)}
              </span>
            </td>
            <td class="num text-right">{fmtAgo(session.lastTs, now)}</td>
            <td class="num text-right">{fmtNum(session.requests)}</td>
            <td class="num text-right">{fmtNum(processedTokens(session))}</td>
            <td class="num text-right">
              <SliceCost slice={{ ...session, key: session.sessionId, days: [] }} />
            </td>
            <td class="text-right">
              <CopyButton
                text={`claude --resume ${session.sessionId}`}
                label={`Copy the resume command for ${name(session)}`}
                class="btn btn-ghost btn-sm"
              />
            </td>
          </TableRow>
        ))}
      </tbody>
    </Table>
  );
}

/** The open drill-down: one model's or one project's own daily series from the held report, and —
 *  for a project — its costliest sessions with their resume commands. */
function Drill({ report, drill, metric }: { report: UsageReport; drill: UsageDrill; metric: UsageMetric }) {
  const { act, now } = useApp();
  const slices = drill.kind === "model" ? report.byModel : report.byProject;
  const slice = slices.find((entry) => entry.key === drill.key);
  if (slice === undefined) return null;
  const values = slice.days.map((day) => metricValue(day, metric));
  const peak = layeredMax([values]);
  const title = drill.kind === "model" ? shortModelName(slice.key) : slice.key;
  const sessions = drill.kind === "project" ? (report.topSessions[slice.key] ?? []) : [];
  return (
    <Panel
      icon={drill.kind === "model" ? "cpu" : "folder"}
      title={`${drill.kind === "model" ? "Model" : "Project"} · ${title}`}
      subtitle={metric === "cost" ? "daily estimated cost" : "daily processed tokens"}
      actions={
        <button
          type="button"
          class="btn btn-sm btn-ghost"
          aria-label="Close the drill-down"
          title="Close the drill-down"
          onClick={() => act("usage-drill-close")}
        >
          <Icon name="close" class="size-4" />
          <span>Close</span>
        </button>
      }
      flush
    >
      <div class="grid gap-4 px-5 py-4" data-usage-drill="">
        <div
          class="relative"
          role="img"
          aria-label={`${title}'s daily series over the window`}
          data-usage-drill-plot=""
        >
          <LineChart
            series={[{ values, color: modelColor(slice.key) }]}
            width={720}
            height={160}
            xLabels={dayLabels(slice.days.map((day) => day.date))}
            yLabel={peak > 1 ? `${metric === "cost" ? fmtUsd(peak) : fmtTokens(peak)} per day` : null}
          />
        </div>
        {sessions.length > 0 && <SessionTable sessions={sessions} now={now} />}
      </div>
    </Panel>
  );
}

/** The whole view: the tiles, the honesty line, the daily chart with its series and day controls,
 *  the two breakdown tables and the open drill-down — all from the one held report. */
export function UsageView() {
  const { state } = useApp();
  const held = state.usage;
  if (held === null) {
    if (state.usageError !== null) {
      return (
        <div role="alert" class="alert alert-error">
          <Icon name="alert" class="icon" />
          <span>
            The usage report did not load ({state.usageError}). The next refresh tries again; the ranges and
            tables come back with it.
          </span>
        </div>
      );
    }
    return (
      <div class="grid gap-5" aria-live="polite">
        <div class="grid grid-cols-1 gap-5 sm:grid-cols-2 xl:grid-cols-5">
          {[0, 1, 2, 3, 4].map((card) => (
            <div key={card} class="skeleton h-24" />
          ))}
        </div>
        <div class="skeleton h-64" role="status" aria-label="Loading usage" />
      </div>
    );
  }
  const report = held.report;
  if (report.totals.requests === 0 && report.byModel.length === 0) {
    return (
      <EmptyState
        title="No usage in this window"
        hint="Requests the sessions ran land here as radar ingests them. Pick a wider window, or come back after the next run."
        icon="history"
      />
    );
  }
  const metric = state.usageMetric;
  const data = usageSeries(report, metric, state.usageHidden);
  const at = data.days.findIndex((day) => day.date === state.usageDay);
  const mark = at < 0 ? null : at;
  return (
    <div class="grid gap-5" data-usage-view="">
      <Tiles report={report} metric={metric} />
      <HonestNote report={report} />
      <Panel
        icon="history"
        title="Daily usage"
        subtitle={report.from === "" ? null : `${dayWords(report.from)} – ${dayWords(report.to)}`}
        actions={
          <>
            <Segmented
              action="usage-metric"
              label="Unit"
              segments={[
                { label: "Tokens", value: "tokens", on: metric === "tokens" },
                { label: "Cost", value: "cost", on: metric === "cost" },
              ]}
            />
            <Segmented
              action="usage-days"
              label="Window"
              segments={USAGE_WINDOWS.map((days) => ({
                label: `${days} days`,
                value: String(days),
                on: state.usageDays === days,
              }))}
            />
          </>
        }
      >
        <div class="grid items-start gap-6 px-5 py-4 xl:grid-cols-[minmax(0,1fr)_20rem]">
          <UsagePlot data={data} metric={metric} mark={mark} />
          <aside class="grid content-start gap-4">
            <SeriesList report={report} metric={metric} hidden={state.usageHidden} />
            {state.usageDay !== null && (
              <DayCard report={report} date={state.usageDay} metric={metric} hidden={state.usageHidden} />
            )}
          </aside>
        </div>
      </Panel>
      <SliceTable title="By model" icon="cpu" slices={report.byModel} metric={metric} drillKind="model" />
      <SliceTable
        title="By project"
        icon="folder"
        slices={report.byProject}
        metric={metric}
        drillKind="project"
      />
      {state.usageDrill !== null && <Drill report={report} drill={state.usageDrill} metric={metric} />}
      <p class="text-meta text-base-content/60">{ESTIMATE_NOTE}</p>
    </div>
  );
}
