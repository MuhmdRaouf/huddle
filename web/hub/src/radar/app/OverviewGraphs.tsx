/**
 * The Overview's four graphs: requests per UTC hour today, tokens by kind per day over seven
 * days, cost per day over thirty, and today's ten costliest sessions by name and machine. Each
 * sits in the shared chart frame and carries its series as `data-series` JSON on its root, the
 * numbers the e2e suite reads back. Read on mount and once a minute from /api/usage?days=30 and
 * /api/usage/today; the days are UTC, like the providers' own usage pages. A phone stacks the
 * four in one column, a wide pane sets them two by two.
 */

import { AreaChart, type ChartSeries } from "@huddle/ui/area-chart.tsx";
import { ChartFrame } from "@huddle/ui/chart-frame.tsx";
import type { ComponentChildren, JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import { useHuddle } from "../../app/context.tsx";
import { dayWords } from "../chart.ts";
import { fmtNum, fmtTokens, fmtUsd } from "../fmt.ts";
import type { TodayReport, UsageDay, UsageReport } from "../shared/usage.ts";

const USAGE_PATH = "/api/radar/api/usage?days=30";
const TODAY_PATH = "/api/radar/api/usage/today";
const REFRESH_MS = 60_000;
const CHART_H = 180;

/** What the four graphs read: the thirty-day report and today's. */
export type GraphsData = { usage: UsageReport; today: TodayReport };

/** Reads both reports; null when either route cannot answer (no history, a network error). */
export async function fetchGraphs(outer: typeof fetch = fetch): Promise<GraphsData | null> {
  const read = async <T,>(path: string): Promise<T | null> => {
    try {
      const response = await outer(path, { headers: { Accept: "application/json" } });
      return response.ok ? ((await response.json()) as T) : null;
    } catch {
      return null;
    }
  };
  const [usage, today] = await Promise.all([read<UsageReport>(USAGE_PATH), read<TodayReport>(TODAY_PATH)]);
  return usage === null || today === null ? null : { usage, today };
}

/** The token kinds the tokens graph draws, each with its series tint and its word. */
const KINDS = [
  { key: "input", word: "Input", tint: undefined, swatch: "var(--primary)" },
  { key: "output", word: "Output", tint: "series-2", swatch: "var(--series-2)" },
  { key: "cacheRead", word: "Cache read", tint: "series-3", swatch: "var(--series-3)" },
  { key: "cacheWrite", word: "Cache write", tint: "series-4", swatch: "var(--series-4)" },
] as const;

const sum = (values: readonly number[]): number => values.reduce((total, value) => total + value, 0);

/** Axis labels for `n` bins: the first, the middle and the last get theirs, the rest none. */
function sparseAxis(n: number, label: (i: number) => string): string[] {
  const marked = new Set([0, Math.floor((n - 1) / 2), n - 1]);
  return Array.from({ length: n }, (_, i) => (marked.has(i) ? label(i) : ""));
}

/** One graph's root: the data-graph name, the series as JSON, the frame inside. */
function Graph({
  name,
  series,
  children,
}: {
  name: string;
  series: Record<string, readonly number[]>;
  children: ComponentChildren;
}): JSX.Element {
  return (
    <div class="min-w-0" data-graph={name} data-series={JSON.stringify(series)}>
      {children}
    </div>
  );
}

function RequestsGraph({ today }: { today: TodayReport }): JSX.Element {
  const hours = today.hourlyRequests;
  const hh = (i: number): string => `${String(i).padStart(2, "0")}:00`;
  const axis = Array.from({ length: hours.length }, (_, i) => (i % 6 === 0 || i === 23 ? hh(i) : ""));
  return (
    <Graph name="requests" series={{ requests: hours }}>
      <ChartFrame
        title="Requests per hour"
        value={fmtNum(sum(hours))}
        meta="today, UTC"
        label={`Requests per UTC hour today, ${fmtNum(sum(hours))} in all`}
      >
        <AreaChart
          series={[{ values: hours }]}
          h={CHART_H}
          class="block w-full"
          axis={axis}
          yfmt={(v) => fmtNum(v)}
          note="No requests today"
          tip={(i) => [`${hh(i)} UTC`, `${fmtNum(hours[i] ?? 0)} requests`]}
        />
      </ChartFrame>
    </Graph>
  );
}

function TokensGraph({ days }: { days: readonly UsageDay[] }): JSX.Element {
  const series = Object.fromEntries(KINDS.map((kind) => [kind.key, days.map((day) => day[kind.key])]));
  const total = sum(KINDS.map((kind) => sum(series[kind.key] ?? [])));
  const drawn: ChartSeries[] = KINDS.map((kind) => ({ values: series[kind.key] ?? [], tint: kind.tint }));
  return (
    <Graph name="tokens" series={series}>
      <ChartFrame
        title="Tokens by kind"
        value={fmtTokens(total)}
        meta="7 days"
        label={`Tokens per UTC day over seven days, by kind: ${KINDS.map(
          (kind) => `${kind.word.toLowerCase()} ${fmtTokens(sum(series[kind.key] ?? []))}`,
        ).join(", ")}`}
      >
        <ul class="mb-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-base-content/60" aria-hidden="true">
          {KINDS.map((kind) => (
            <li key={kind.key} class="flex items-center gap-1.5">
              <span class="size-2.5 rounded-[3px]" style={`background:${kind.swatch}`} />
              {kind.word} <span class="num">{fmtTokens(sum(series[kind.key] ?? []))}</span>
            </li>
          ))}
        </ul>
        <AreaChart
          series={drawn}
          h={CHART_H}
          class="block w-full"
          axis={sparseAxis(days.length, (i) => dayWords(days[i]?.date))}
          yfmt={fmtTokens}
          note="No tokens in seven days"
          tip={(i) => [
            dayWords(days[i]?.date),
            ...KINDS.map((kind) => `${kind.word} ${fmtTokens(days[i]?.[kind.key] ?? 0)}`),
          ]}
        />
      </ChartFrame>
    </Graph>
  );
}

function CostGraph({ days }: { days: readonly UsageDay[] }): JSX.Element {
  const cost = days.map((day) => day.costUsd);
  return (
    <Graph name="cost" series={{ cost }}>
      <ChartFrame
        title="Cost per day"
        value={fmtUsd(sum(cost))}
        meta="30 days"
        label={`Estimated cost per UTC day over thirty days, ${fmtUsd(sum(cost))} in all`}
      >
        <AreaChart
          series={[{ values: cost }]}
          h={CHART_H}
          class="block w-full"
          axis={sparseAxis(days.length, (i) => dayWords(days[i]?.date))}
          yfmt={fmtUsd}
          note="No spend in thirty days"
          tip={(i) => [dayWords(days[i]?.date), fmtUsd(days[i]?.costUsd ?? 0)]}
        />
      </ChartFrame>
    </Graph>
  );
}

/** Today's costliest sessions as ranked bars, each named by its title and machine from the
 *  registry, the usage label or the id standing in for a session the registry never saw. */
function SessionsGraph({ today }: { today: TodayReport }): JSX.Element {
  const { registry } = useHuddle();
  const rows = registry.getState().sessions ?? [];
  const sessions = today.sessions.map((session) => {
    const row = rows.find((entry) => entry.session_id === session.sessionId);
    return {
      id: session.sessionId,
      title: row?.title ?? (session.label || session.sessionId.slice(0, 8)),
      machine: row?.machine ?? null,
      cost: session.costUsd,
    };
  });
  const cost = sessions.map((session) => session.cost);
  const max = Math.max(...cost, 0);
  return (
    <Graph name="sessions" series={{ cost }}>
      <ChartFrame
        title="Cost per session"
        value={fmtUsd(sum(cost))}
        meta="today, top 10"
        label={
          sessions.length === 0
            ? "No session spent anything today"
            : `Today's costliest sessions: ${sessions
                .map((s) => `${s.title}${s.machine ? ` on ${s.machine}` : ""} ${fmtUsd(s.cost)}`)
                .join(", ")}`
        }
      >
        {sessions.length === 0 ? (
          <p class="py-6 text-center text-sm text-base-content/60">No sessions today</p>
        ) : (
          <ol class="grid gap-2">
            {sessions.map((session) => (
              <li key={session.id} class="grid grid-cols-[minmax(0,1fr)_auto] gap-x-3 gap-y-1 text-sm">
                <span class="min-w-0 truncate" title={session.id}>
                  {session.title}
                  {session.machine ? <span class="muted"> · {session.machine}</span> : null}
                </span>
                <span class="num text-right">{fmtUsd(session.cost)}</span>
                <div class="bar-track col-span-2">
                  <div
                    class="bar-seg"
                    style={`width:${max > 0 ? (session.cost / max) * 100 : 0}%;background:var(--series-1)`}
                  />
                </div>
              </li>
            ))}
          </ol>
        )}
      </ChartFrame>
    </Graph>
  );
}

export function OverviewGraphs({
  read = fetchGraphs,
}: {
  /** The reports' read, injectable so tests answer it without a server. */
  read?: () => Promise<GraphsData | null>;
}): JSX.Element | null {
  const [data, setData] = useState<GraphsData | null>(null);

  useEffect(() => {
    let live = true;
    const load = (): void => {
      void read().then((answer) => {
        if (live && answer !== null) setData(answer);
      });
    };
    load();
    const timer = setInterval(load, REFRESH_MS);
    return () => {
      live = false;
      clearInterval(timer);
    };
  }, [read]);

  if (data === null) return null;
  return (
    <section class="grid gap-4 md:grid-cols-2" aria-label="Usage graphs" data-overview-graphs="">
      <RequestsGraph today={data.today} />
      <TokensGraph days={data.usage.days.slice(-7)} />
      <CostGraph days={data.usage.days} />
      <SessionsGraph today={data.today} />
    </section>
  );
}
