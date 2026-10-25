/**
 * The Overview: the tab's front page, composed from the parts every other view reads — five stat
 * cards (spend against its budget, requests and their failures, tokens and their cache share,
 * sessions and who needs the owner, routers and any budget stopped), the four usage graphs
 * (requests per hour, tokens by kind, cost per day and per session), the spend-by-hour chart
 * stacked by model, the top sessions by spend (a row opens the Session screen), the plan limits
 * the statusline word reported, the newest alert, the routers list and the top tools, under the
 * self-check badge that holds today's totals against the transcripts. The phone
 * keeps the first two cards, the graphs stacked, the chart and the routers; the rest waits
 * for a wider pane.
 */

import { modelColor } from "@huddle/ui/model-color.ts";
import type { ComponentChildren, JSX } from "preact";
import { useHuddle } from "../../../app/context.tsx";
import type { RegistrySession } from "../../../registry.ts";
import { fmtNum, fmtTokens, fmtUsd } from "../../fmt.ts";
import { STATUS_COLOR } from "../../palette.ts";
import { bucketPlan, rangeWords, stampOf } from "../../shared/time-range.ts";
import {
  type ClientState,
  flowSource,
  liveQuota,
  requestCost,
  resetsIn,
  visibleRequests,
} from "../../state.ts";
import { ActiveBlock } from "../ActiveBlock.tsx";
import { alertWhere } from "../AlertStrip.tsx";
import { useApp } from "../context.ts";
import { Flow, requestsInRange } from "../Flow.tsx";
import { Icon } from "../Icon.tsx";
import { EmptyState, ModelChip, Panel, Status, shortModelName } from "../kit.tsx";
import { SelfCheckBadge } from "../SelfCheckBadge.tsx";
import { OverviewGraphs } from "../OverviewGraphs.tsx";
import { isJob } from "../sessions.ts";

/** The loopback port each provider plugin's router serves on, for the routers list. */
const ROUTER_PORTS: Record<string, number> = {
  zai: 18787,
  kimi: 18788,
  deepseek: 18789,
  minimax: 18790,
  qwen: 18791,
};

/** The most models the chart stacks; the rest of the spend joins one "other" band. */
const CHART_MODELS = 3;

/** One stat card: a quiet title, the figure, and a subline that says what the figure reads. A
 *  title that carries the window keeps its plain name in `stat`, so the card stays addressable. */
function Stat({
  title,
  value,
  subline,
  class: cls = "",
  stat,
}: {
  title: string;
  value: string;
  subline: ComponentChildren;
  class?: string;
  stat?: string;
}): JSX.Element {
  return (
    <div class={`panel p-4 md:p-5 ${cls}`} data-stat={(stat ?? title).toLowerCase()}>
      <div class="text-xs text-base-content/60">{title}</div>
      <div class="num mt-0.5 text-2xl font-semibold md:text-[26px]">{value}</div>
      {subline !== null && <div class="mt-1 text-xs text-base-content/60">{subline}</div>}
    </div>
  );
}

/** The day's spend against the first budget that names one: the figure from the ledger, the bar
 *  and its percent from the budget's own status; no budget set says where to make one. */
function SpendStat() {
  const { state } = useApp();
  const spend = state.budgetStatus?.spend.find((entry) => entry.pct > 0) ?? state.budgetStatus?.spend[0];
  const pct = spend === undefined ? null : Math.min(100, Math.round(spend.pct));
  return (
    <Stat
      title="Spend today"
      value={fmtUsd(state.spendToday)}
      subline={
        spend === undefined ? (
          <a class="link link-primary" href="#/settings/radar">
            No budget set
          </a>
        ) : (
          <>
            <div class="bar-track mt-1.5">
              <div class="bar-seg" style={`width:${pct}%;background:var(--series-1)`} />
            </div>
            <span class="num">{`${pct}% of ${fmtUsd(spend.limitUsd)}`}</span>
          </>
        )
      }
    />
  );
}

/** The range's numbers the cards read: requests, failed ones, tokens and the cache's share. */
function rangeNumbers(state: ClientState, now: number) {
  const { from, to, history } = flowSource(state, now);
  const inRange = (ts: number): boolean => ts >= from && ts <= to;
  const requests = history ? [] : visibleRequests(state).filter((r) => inRange(r.ts));
  const failed = requests.filter(
    (r) =>
      r.stopReason !== null &&
      ["error", "overloaded_error", "api_error", "request_error"].includes(r.stopReason),
  ).length;
  const tokens = requests.reduce(
    (sum, r) => sum + r.tokens.input + r.tokens.output + r.tokens.cacheRead + r.tokens.cacheWrite,
    0,
  );
  const cache = requests.reduce((sum, r) => sum + r.tokens.cacheRead, 0);
  return { from, to, history, requests, failed, tokens, cache };
}

/** The chart's bands: the range's spend bucketed by hour, one band per top model plus "other". */
function spendBands(state: ClientState, now: number) {
  const numbers = rangeNumbers(state, now);
  if (numbers.history) return null;
  const plan = bucketPlan(numbers.from, numbers.to, 24);
  const perModel = new Map<string, number[]>();
  const ensure = (model: string): number[] => {
    const held = perModel.get(model);
    if (held !== undefined) return held;
    const fresh = Array.from({ length: plan.buckets }, () => 0);
    perModel.set(model, fresh);
    return fresh;
  };
  for (const request of numbers.requests) {
    const cost = requestCost(request)?.usd;
    if (cost === undefined) continue;
    const at = Math.min(plan.buckets - 1, Math.floor((request.ts - numbers.from) / plan.bucketMs));
    const band = at < 0 ? undefined : ensure(request.model)[at];
    if (band === undefined) continue;
    ensure(request.model)[at] = band + cost;
  }
  const totals = [...perModel.entries()]
    .map(([model, values]) => ({ model, values, total: values.reduce((a, b) => a + b, 0) }))
    .sort((a, b) => b.total - a.total);
  if (totals.every((band) => band.total <= 0)) return null;
  const top = totals.slice(0, CHART_MODELS);
  const rest = totals.slice(CHART_MODELS);
  const other = rest.reduce<number[]>(
    (sum, band) => sum.map((value, index) => value + (band.values[index] ?? 0)),
    Array.from({ length: plan.buckets }, () => 0),
  );
  const bands = [
    ...top.map((band) => ({ model: band.model, values: band.values, total: band.total })),
    ...(rest.some((band) => band.total > 0)
      ? [{ model: null, values: other, total: other.reduce((a, b) => a + b, 0) }]
      : []),
  ];
  return { plan, bands, from: numbers.from, to: numbers.to };
}

/** The spend-by-hour chart: stacked columns, one band per top model, hour marks underneath. */
function SpendChart() {
  const { state, now } = useApp();
  const spent = spendBands(state, now);
  if (spent === null) {
    // the range runs on the history route, whose flow counts tokens: the token flow chart stands in
    return (
      <Panel
        icon="arrows"
        title="Token flow"
        subtitle="Spend by the hour needs the range the requests in memory cover"
      >
        <Flow />
      </Panel>
    );
  }
  const width = 700;
  const height = 230;
  const { plan, bands } = spent;
  const step = width / Math.max(1, plan.buckets);
  const bar = step * 0.66;
  const totals = Array.from({ length: plan.buckets }, (_, index) =>
    bands.reduce((sum, band) => sum + (band.values[index] ?? 0), 0),
  );
  const max = Math.max(...totals, 0.000001);
  const yOf = (value: number): number => height - 4 - (value / max) * (height - 10);
  const year = new Date(now).getFullYear();
  const marks = [0, 0.5, 1].map((at) => {
    const ts = spent.from + (spent.to - spent.from) * at;
    return { at, text: stampOf(ts, year).split(" ").slice(1).join(" ") };
  });
  return (
    <section class="panel flex min-h-0 flex-col gap-3 p-4 md:p-5" aria-labelledby="ovw-chart">
      <div class="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
        <h2 id="ovw-chart" class="text-[15px] font-semibold">
          Spend by hour, by model
        </h2>
        <div class="flex flex-wrap gap-x-4 gap-2 text-xs text-base-content/60">
          {bands.map((band) => (
            <span key={band.model ?? "other"} class="flex items-center gap-1.5">
              <span
                class="size-2.5 rounded-[3px]"
                style={`background:${band.model === null ? "var(--muted)" : modelColor(band.model)}`}
                aria-hidden="true"
              />
              {band.model === null ? "other" : shortModelName(band.model)} {fmtUsd(band.total)}
            </span>
          ))}
        </div>
      </div>
      <svg
        viewBox={`0 0 ${width} ${height}`}
        width="100%"
        height={String(height)}
        preserveAspectRatio="none"
        role="img"
        aria-label="Spend by hour, stacked by model"
        class="w-full"
      >
        {[0.25, 0.5, 0.75].map((fraction) => (
          <line
            key={fraction}
            x1="0"
            x2={String(width)}
            y1={String(height * fraction)}
            y2={String(height * fraction)}
            class="chart-grid"
          />
        ))}
        <line x1="0" x2={String(width)} y1={String(height - 4)} y2={String(height - 4)} class="chart-axis" />
        {totals.map((total, index) => {
          if (total <= 0) return null;
          let running = 0;
          const rects = bands.map((band) => {
            const value = band.values[index] ?? 0;
            if (value <= 0) return null;
            const yTop = yOf(running + value);
            const size = yOf(running) - yTop;
            running += value;
            return (
              <rect
                key={band.model ?? "other"}
                x={Number((index * step + (step - bar) / 2).toFixed(1))}
                y={Number(yTop.toFixed(1))}
                width={Number(bar.toFixed(1))}
                height={Number(Math.max(1, size).toFixed(1))}
                rx="3"
                fill={band.model === null ? "var(--muted)" : modelColor(band.model)}
                opacity={index === plan.buckets - 1 ? 0.6 : 1}
              />
            );
          });
          return <g key={index}>{rects}</g>;
        })}
      </svg>
      <div class="flex justify-between text-[11px] text-base-content/50">
        {marks.map((mark) => (
          <span key={mark.at}>{mark.text}</span>
        ))}
      </div>
    </section>
  );
}

/** The dot a session row wears: the registry's state word, shape plus colour (SPEC 9). */
function sessionDot(row: RegistrySession | undefined, live: boolean): { cls: string; word: string } {
  if (row !== undefined && row.state === "needs-you")
    return { cls: "ring-2 ring-warning rounded-full", word: "needs you" };
  if (row !== undefined && row.state === "working")
    return { cls: "bg-success rounded-full", word: "working" };
  if (live) return { cls: "bg-success/70 rounded-full", word: "live" };
  return { cls: "border border-base-content/40 rounded-full", word: "ended" };
}

/** The sessions that spent the most, each row opening the Session screen (SPEC 9.1: radar's
 *  sessions and the registry are one list). */
function TopSessions() {
  const { state } = useApp();
  const { registry } = useHuddle();
  const rows = registry.getState().sessions ?? [];
  const top = [...state.sessions]
    .filter((item) => !isJob(item))
    .sort((a, b) => (b.costUsd ?? 0) - (a.costUsd ?? 0))
    .slice(0, 5);
  return (
    <section class="panel p-4 md:p-5" aria-labelledby="ovw-sessions">
      <h2 id="ovw-sessions" class="mb-2 text-[15px] font-semibold">
        Top sessions by spend
      </h2>
      {top.length === 0 ? (
        <EmptyState
          title="No sessions yet"
          hint="Start a Claude Code session and it shows up here within a second."
          icon="layers"
        />
      ) : (
        <table class="w-full table-fixed text-sm" aria-label="Top sessions by spend">
          <thead>
            <tr>
              <th scope="col" class="w-6 py-1 text-left font-normal text-base-content/50" />
              <th scope="col" class="py-1 text-left font-normal text-base-content/50">
                Session
              </th>
              <th scope="col" class="w-40 py-1 text-left font-normal text-base-content/50">
                Model
              </th>
              <th scope="col" class="w-24 py-1 text-right font-normal text-base-content/50">
                Requests
              </th>
              <th scope="col" class="w-24 py-1 text-right font-normal text-base-content/50">
                Cost
              </th>
            </tr>
          </thead>
          <tbody>
            {top.map((item) => {
              const row = rows.find((entry) => entry.session_id === item.id);
              const dot = sessionDot(row, item.live);
              const name = item.name ?? item.project ?? item.id.slice(0, 8);
              return (
                <tr key={item.id} class="border-t border-base-content/10">
                  <td class="py-2 align-middle">
                    <span class={`size-2.5 ${dot.cls}`} role="img" aria-label={dot.word} title={dot.word} />
                  </td>
                  <td class="min-w-0 py-2">
                    <a
                      href={`#/now/${encodeURIComponent(item.id)}`}
                      class="block truncate py-2 hover:text-primary"
                      title={item.id}
                    >
                      {name}
                      {row?.machine ? <span class="muted"> · {row.machine}</span> : null}
                    </a>
                  </td>
                  <td class="truncate py-2">
                    {item.model === null ? "–" : <ModelChip model={item.model} />}
                  </td>
                  <td class="num py-2 text-right">{fmtNum(item.requestCount)}</td>
                  <td class="num py-2 text-right">
                    {item.costUsd === null || item.costUsd === 0 ? "–" : fmtUsd(item.costUsd)}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
    </section>
  );
}

/** The sessions card's count and its needs-you word, read from the registry the Now list shares. */
function SessionsStat() {
  const { state } = useApp();
  const { registry } = useHuddle();
  const needs = registry.needs();
  const main = state.sessions.filter((item) => !isJob(item));
  return (
    <Stat
      title="Sessions"
      value={fmtNum(main.length)}
      subline={
        needs > 0 ? <Status tone="warn" word={`${fmtNum(needs)} need you`} /> : <span>none need you</span>
      }
    />
  );
}

/** The requests card: the range's count, the window named on the tile — all time, said plainly,
 *  when only the summary can answer — and the failures in their tone. */
function RequestsStat({
  state,
  now,
  numbers,
}: {
  state: ClientState;
  now: number;
  numbers: ReturnType<typeof rangeNumbers>;
}) {
  const count = requestsInRange(state, now);
  return (
    <Stat
      title={`Requests · ${count === null ? "all time" : rangeWords(state.range, now)}`}
      stat="Requests"
      value={count === null ? fmtNum(state.summary?.requests ?? 0) : fmtNum(count)}
      subline={
        numbers.failed > 0 ? (
          <Status tone="err" word={`${fmtNum(numbers.failed)} failed`} />
        ) : numbers.history ? null : (
          <Status tone="ok" word="0 failed" />
        )
      }
    />
  );
}

/** The tokens card: the range's total and the share that came from the cache, the window named on
 *  the tile — all time, said plainly, when only the summary can answer. */
function TokensStat({
  state,
  now,
  numbers,
}: {
  state: ClientState;
  now: number;
  numbers: ReturnType<typeof rangeNumbers>;
}) {
  const all = state.summary?.tokens;
  const tokens = numbers.history
    ? all === undefined
      ? null
      : all.input + all.output + all.cacheRead + all.cacheWrite
    : numbers.tokens;
  const share = tokens !== null && tokens > 0 ? Math.round((numbers.cache / tokens) * 100) : null;
  return (
    <Stat
      title={`Tokens · ${numbers.history ? "all time" : rangeWords(state.range, now)}`}
      stat="Tokens"
      value={fmtTokens(tokens ?? 0)}
      subline={
        tokens === null ? (
          "nothing yet"
        ) : !numbers.history && share !== null ? (
          <span class="num">{`${share}% from cache`}</span>
        ) : null
      }
    />
  );
}

/** The routers card: how many are up, the one a stopped budget names. */
function RoutersStat() {
  const { state } = useApp();
  const stopped = stoppedScopes(state);
  const up = Math.max(0, state.providers.length - stopped.size);
  const named = [...stopped].map((scope) => scope.replace(/^provider:/, ""));
  return (
    <Stat
      title="Routers"
      value={state.providers.length === 0 ? "–" : `${up} of ${state.providers.length}`}
      subline={
        named.length > 0 ? (
          <Status tone="err" word={`${named.join(", ")} budget stopped`} />
        ) : (
          <span>all up</span>
        )
      }
    />
  );
}

/** The scopes a stop budget has reached, as the status document names them. */
function stoppedScopes(state: ClientState): Set<string> {
  return new Set((state.budgetStatus?.stopped ?? []).filter((scope) => scope !== "total"));
}

/** One router row: the up dot or the stopped diamond, the name and its port, the word on the right. */
function RouterRow({ plugin, stopped }: { plugin: string; stopped: boolean }) {
  const port = ROUTER_PORTS[plugin];
  return (
    <div class="grid grid-cols-[14px_1fr_auto] items-center gap-x-2.5 py-1.5 text-sm" data-router={plugin}>
      {stopped ? (
        <span
          class="size-2.5 rotate-45 rounded-[2px]"
          style={`background:${STATUS_COLOR.err}`}
          role="img"
          aria-label="budget stopped"
        />
      ) : (
        <span class="size-2.5 rounded-full bg-success" role="img" aria-label="up" />
      )}
      <span class="min-w-0 truncate">
        {plugin}
        {port !== undefined && <span class="muted"> · :{port}</span>}
      </span>
      {stopped ? (
        <span class="text-xs" style={`color:${STATUS_COLOR.err}`}>
          budget stopped
        </span>
      ) : (
        <span class="muted text-xs">up</span>
      )}
    </div>
  );
}

/** The routers list: every provider plugin the budgets route knows, stopped ones named. */
function RoutersList() {
  const { state } = useApp();
  const stopped = stoppedScopes(state);
  return (
    <section class="panel p-4 md:p-5" aria-labelledby="ovw-routers">
      <h2 id="ovw-routers" class="mb-1 text-[15px] font-semibold">
        Routers
      </h2>
      {state.providers.length === 0 ? (
        <EmptyState
          title="No provider plugins yet"
          hint="A provider plugin's router shows up here once it reports: zai, kimi, deepseek, minimax, qwen."
          icon="route"
        />
      ) : (
        <div class="flex flex-col">
          {[...state.providers]
            .sort(
              (a, b) =>
                Number(stopped.has(`provider:${b}`)) - Number(stopped.has(`provider:${a}`)) ||
                a.localeCompare(b),
            )
            .map((plugin) => (
              <RouterRow key={plugin} plugin={plugin} stopped={stopped.has(`provider:${plugin}`)} />
            ))}
        </div>
      )}
    </section>
  );
}

/** The tools the agents used most, as bars. */
function TopTools() {
  const { state } = useApp();
  const tools = [...(state.models?.tools ?? [])].sort((a, b) => b.count - a.count).slice(0, 3);
  const max = Math.max(...tools.map((tool) => tool.count), 1);
  return (
    <section class="panel p-4 md:p-5" aria-labelledby="ovw-tools">
      <h2 id="ovw-tools" class="mb-2 text-[15px] font-semibold">
        Top tools
      </h2>
      {tools.length === 0 ? (
        <EmptyState title="No tool calls yet" hint="Tool calls the agents made land here." icon="wrench" />
      ) : (
        <div class="grid gap-2">
          {tools.map((tool) => (
            <div key={tool.name} class="grid grid-cols-[60px_1fr_40px] items-center gap-2 text-sm">
              <span class="min-w-0 truncate">{tool.name}</span>
              <div class="bar-track">
                <div
                  class="bar-seg"
                  style={`width:${(tool.count / max) * 100}%;background:var(--series-1)`}
                />
              </div>
              <span class="num text-right text-base-content/60">{fmtNum(tool.count)}</span>
            </div>
          ))}
        </div>
      )}
    </section>
  );
}

/** The newest alert as the design's card: what happened and the one verb that helps — the budget
 *  ones raise the limit, the rest lead to the Alerts view. */
function AlertCard() {
  const { state, act } = useApp();
  const alert = state.alerts[0];
  if (alert === undefined) return null;
  const tone = alert.severity === "err" ? "err" : "warn";
  return (
    <section
      class="panel flex flex-col gap-2 border-warning/60 p-4 md:p-5"
      style={`border-color:${STATUS_COLOR[tone]}66`}
      aria-labelledby="ovw-alert"
      role="alert"
    >
      <h2 id="ovw-alert" class="flex items-center gap-2 text-[15px] font-semibold">
        <span
          class="size-2.5 rotate-45 rounded-[2px]"
          style={`background:${STATUS_COLOR[tone]}`}
          aria-hidden="true"
        />
        Alert
      </h2>
      <p class="m-0 text-sm leading-relaxed">
        {alert.detail} <span class="muted">({alertWhere(alert)})</span>
      </p>
      <div class="mt-1 flex flex-wrap gap-2">
        {alert.kind === "budget" ? (
          <a href="#/settings/radar" class="btn btn-sm" data-alert-action="">
            <Icon name="dollar" class="size-4" />
            Raise budget
          </a>
        ) : (
          <button
            type="button"
            class="btn btn-sm"
            data-action="tab"
            data-value="alerts"
            onClick={() => act("tab", "alerts")}
          >
            All alerts
          </button>
        )}
        <button
          type="button"
          class="btn btn-ghost btn-sm"
          data-action="dismiss-alert"
          data-value={alert.id}
          onClick={() => act("dismiss-alert", alert.id)}
        >
          Dismiss
        </button>
      </div>
    </section>
  );
}

/** The window a plan limit's percentage sits in: ok under 70, warn to 90, danger past it. */
function quotaTone(pct: number): "ok" | "warn" | "err" {
  if (pct > 90) return "err";
  if (pct >= 70) return "warn";
  return "ok";
}

/** One window's row: its label, the percent against a bar in its tone, and when it resets. */
function QuotaMeter({
  label,
  pct,
  resetsAt,
  now,
}: {
  label: string;
  pct: number | null;
  resetsAt: number | null;
  now: number;
}): JSX.Element {
  const countdown = resetsIn(resetsAt, now);
  return (
    <div data-quota-window={label}>
      <div class="flex items-baseline justify-between text-sm">
        <span>{label}</span>
        <span class="num" style={pct === null ? undefined : `color:${STATUS_COLOR[quotaTone(pct)]}`}>
          {pct === null ? "–" : `${Math.round(pct)}%`}
        </span>
      </div>
      {pct !== null && (
        <div class="bar-track mt-1">
          <div
            class="bar-seg"
            style={`width:${Math.min(100, Math.round(pct))}%;background:${STATUS_COLOR[quotaTone(pct)]}`}
            role="meter"
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={Math.round(pct)}
            aria-label={`${label} plan limit`}
          />
        </div>
      )}
      {countdown !== null && <div class="mt-0.5 text-xs text-base-content/60">{countdown}</div>}
    </div>
  );
}

/** The plan limits card: the 5-hour and weekly windows Claude Code reported through the
 *  statusline word, coloured by how close each one is. A report older than six hours (or none)
 *  leaves the card out: nothing to say about now. */
function PlanLimits() {
  const { state, now } = useApp();
  const quota = liveQuota(state.quota, now);
  if (quota === null) return null;
  return (
    <section class="panel p-4 md:p-5" aria-labelledby="ovw-quota" data-quota="">
      <h2 id="ovw-quota" class="mb-2 text-[15px] font-semibold">
        Plan limits
      </h2>
      <div class="flex flex-col gap-3">
        <QuotaMeter label="5-hour" pct={quota.five_hour} resetsAt={quota.five_hour_resets_at} now={now} />
        <QuotaMeter label="weekly" pct={quota.seven_day} resetsAt={quota.seven_day_resets_at} now={now} />
      </div>
    </section>
  );
}

/** The Overview: the stat row over the active-block card, the chart and the sessions table, the
 *  plan limits, the alert, routers and tools beside them; the phone keeps the first two cards,
 *  the chart and the routers. */
export function OverviewView(): JSX.Element {
  const { state, now } = useApp();
  const numbers = rangeNumbers(state, now);
  return (
    <div class="grid gap-4 xl:grid-cols-[minmax(0,1fr)_360px]">
      <div class="flex justify-end xl:col-span-2">
        <SelfCheckBadge />
      </div>
      <div class="grid grid-cols-2 gap-3 md:grid-cols-3 xl:col-span-2 xl:grid-cols-5" data-stat-cards="">
        <SpendStat />
        <RequestsStat state={state} now={now} numbers={numbers} />
        <TokensStat state={state} now={now} numbers={numbers} />
        <SessionsStat />
        <RoutersStat />
      </div>
      <div class="grid min-w-0 gap-4 xl:col-span-2">
        <ActiveBlock />
      </div>
      <div class="min-w-0 xl:col-span-2">
        <OverviewGraphs />
      </div>
      <div class="grid min-w-0 gap-4">
        <SpendChart />
        <div class="hidden md:block">
          <TopSessions />
        </div>
      </div>
      <div class="grid content-start gap-4">
        <PlanLimits />
        <div class="hidden md:block">
          <AlertCard />
        </div>
        <RoutersList />
        <div class="hidden md:block">
          <TopTools />
        </div>
      </div>
    </div>
  );
}
