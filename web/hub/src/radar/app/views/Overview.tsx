/**
 * The Overview: the tab's front page, composed from the parts every other view reads — five stat
 * cards (spend against its budget, requests and their failures, tokens and their cache share,
 * sessions and who needs the owner, routers and any budget stopped), the day card (peak sessions
 * at once and cards done today), the four usage graphs (requests per hour, tokens by kind, cost
 * per day and per session), the spend-by-hour chart stacked by model, the top sessions by spend
 * (a row opens the Session screen), the plan limits the statusline word reported, the newest
 * alert, the routers list and the top tools. The phone leads with three tiles (spend, requests, sessions live) under
 * one label that names the window they read, then the day card, the graphs stacked, the chart and
 * the routers; the rest waits for a wider pane.
 */

import { modelColor } from "@huddle/ui/model-color.ts";
import type { ComponentChildren, JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import { useHuddle } from "../../../app/context.tsx";
import { useLayout } from "../../../app/layout.ts";
import type { RegistrySession } from "../../../registry.ts";
import { fmtNum, fmtTokens, fmtUsd } from "../../fmt.ts";
import { SERIES, STATUS_COLOR } from "../../palette.ts";
import type { RouterFacts } from "../../shared/api.ts";
import { bucketPlan, rangeWords, stampOf } from "../../shared/time-range.ts";
import {
  type ClientState,
  flowSource,
  liveQuota,
  requestCancelled,
  requestCost,
  requestFailed,
  resetsIn,
  visibleRequests,
} from "../../state.ts";
import { ActiveBlock } from "../ActiveBlock.tsx";
import { alertWhere } from "../AlertStrip.tsx";
import { useApp } from "../context.ts";
import { DayCard } from "../DayCard.tsx";
import { Flow, requestsInRange } from "../Flow.tsx";
import { Icon } from "../Icon.tsx";
import { EmptyState, ModelChip, Status, shortModelName } from "../kit.tsx";
import { OverviewGraphs } from "../OverviewGraphs.tsx";
import { isJob } from "../sessions.ts";

/** How often the routers card asks the hub whether huddle-router serves. */
const ROUTER_POLL_MS = 10_000;

/** The most models the chart stacks; the rest of the spend joins one "other" band. */
const CHART_MODELS = 3;

/** The colour a chart band paints in: its model's, or the quiet mark for the "other" band. */
const bandColor = (model: string | null): string => (model === null ? STATUS_COLOR.idle : modelColor(model));

/** A horizontal bar on the token well: the filled share in its colour, a meter when it reads one. */
function Bar({
  pct,
  color,
  class: cls = "",
  meter,
}: {
  pct: number;
  color: string;
  class?: string;
  meter?: { value: number; label: string };
}): JSX.Element {
  return (
    <div class={`flex h-2 overflow-hidden rounded-full bg-[var(--ui-surface-2)] ${cls}`}>
      <div
        class="bar-seg h-full rounded-full"
        style={`width:${pct}%;background:${color}`}
        role={meter === undefined ? undefined : "meter"}
        aria-valuemin={meter === undefined ? undefined : 0}
        aria-valuemax={meter === undefined ? undefined : 100}
        aria-valuenow={meter?.value}
        aria-label={meter?.label}
      />
    </div>
  );
}

/** One stat card: a quiet title, the figure, and a subline that says what the figure reads. A
 *  title that carries the window keeps its plain name in `stat`, so the card stays addressable. */
function Stat({
  title,
  value,
  subline,
  class: cls = "",
  stat,
  oneLine = false,
}: {
  title: string;
  value: string;
  subline: ComponentChildren;
  class?: string;
  stat?: string;
  oneLine?: boolean;
}): JSX.Element {
  return (
    <div class={`ui-card p-4 md:p-5 ${cls}`} data-stat={(stat ?? title).toLowerCase()}>
      <div class="text-xs text-[var(--ui-muted)]">{title}</div>
      <div class={`num mt-0.5 text-2xl font-semibold md:text-[26px] ${oneLine ? "whitespace-nowrap" : ""}`}>
        {value}
      </div>
      {subline !== null && <div class="mt-1 text-xs text-[var(--ui-muted)]">{subline}</div>}
    </div>
  );
}

/** The day's spend against the first budget that names one: the figure from the ledger with the
 *  count of calls it could not price, the bar and its percent from the budget's own status; no
 *  budget set says where to make one. */
function SpendStat() {
  const { state } = useApp();
  const spend = state.budgetStatus?.spend.find((entry) => entry.pct > 0) ?? state.budgetStatus?.spend[0];
  const pct = spend === undefined ? null : Math.min(100, Math.round(spend.pct));
  return (
    <Stat
      title="Spend today"
      value={
        state.spendUnpriced > 0
          ? `${fmtUsd(state.spendToday)} · ${state.spendUnpriced} unpriced`
          : fmtUsd(state.spendToday)
      }
      subline={
        spend === undefined ? (
          <a
            class="inline-flex min-h-[var(--ui-tap)] items-center text-[var(--ui-accent)] underline"
            href="#/settings/radar"
          >
            No budget set
          </a>
        ) : (
          <>
            <Bar pct={pct ?? 0} color={SERIES[0]} class="mt-1.5" />
            <span class="num">{`${pct}% of ${fmtUsd(spend.limitUsd)}`}</span>
          </>
        )
      }
    />
  );
}

/** The range's numbers the cards read: requests, the HTTP and connection failures among them, the
 *  calls their client cancelled, tokens and the cache's share. */
function rangeNumbers(state: ClientState, now: number) {
  const { from, to, history } = flowSource(state, now);
  const inRange = (ts: number): boolean => ts >= from && ts <= to;
  const requests = history ? [] : visibleRequests(state).filter((r) => inRange(r.ts));
  const failed = requests.filter(requestFailed).length;
  const cancelled = requests.filter(requestCancelled).length;
  const tokens = requests.reduce(
    (sum, r) => sum + r.tokens.input + r.tokens.output + r.tokens.cacheRead + r.tokens.cacheWrite,
    0,
  );
  const cache = requests.reduce((sum, r) => sum + r.tokens.cacheRead, 0);
  return { from, to, history, requests, failed, cancelled, tokens, cache };
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
      <section class="ui-card min-w-0 p-4 md:p-5" aria-labelledby="ovw-flow">
        <h2 id="ovw-flow" class="flex items-center gap-2 text-[15px] font-semibold">
          <Icon name="arrows" class="size-4 text-[var(--ui-accent)]" />
          Token flow
        </h2>
        <p class="mb-3 text-sm text-[var(--ui-muted)]">
          Spend by the hour needs the range the requests in memory cover
        </p>
        <Flow />
      </section>
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
    <section class="ui-card flex min-h-0 flex-col gap-3 p-4 md:p-5" aria-labelledby="ovw-chart">
      <div class="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
        <h2 id="ovw-chart" class="text-[15px] font-semibold">
          Spend by hour, by model
        </h2>
        <div class="flex flex-wrap gap-x-4 gap-2 text-xs text-[var(--ui-muted)]">
          {bands.map((band) => (
            <span key={band.model ?? "other"} class="flex items-center gap-1.5">
              <span
                class="size-2.5 rounded-[3px]"
                style={`background:${bandColor(band.model)}`}
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
            class="stroke-[var(--ui-line)] [vector-effect:non-scaling-stroke]"
          />
        ))}
        <line
          x1="0"
          x2={String(width)}
          y1={String(height - 4)}
          y2={String(height - 4)}
          class="stroke-[var(--ui-line-strong)] [vector-effect:non-scaling-stroke]"
        />
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
                fill={bandColor(band.model)}
                opacity={index === plan.buckets - 1 ? 0.6 : 1}
              />
            );
          });
          return <g key={index}>{rects}</g>;
        })}
      </svg>
      <div class="flex justify-between text-[11px] text-[var(--ui-muted)]">
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
    return { cls: "ring-2 ring-[var(--ui-needs-you)] rounded-full", word: "needs you" };
  if (row !== undefined && row.state === "working")
    return { cls: "bg-[var(--ui-working)] rounded-full", word: "working" };
  if (live) return { cls: "bg-[var(--ui-ok)] rounded-full", word: "live" };
  return { cls: "border border-[var(--ui-idle)] rounded-full", word: "ended" };
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
    <section class="ui-card p-4 md:p-5" aria-labelledby="ovw-sessions">
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
              <th scope="col" class="w-6 py-1 text-left font-normal text-[var(--ui-muted)]" />
              <th scope="col" class="py-1 text-left font-normal text-[var(--ui-muted)]">
                Session
              </th>
              <th
                scope="col"
                class="hidden w-40 py-1 text-left font-normal text-[var(--ui-muted)] sm:table-cell"
              >
                Model
              </th>
              <th scope="col" class="w-24 py-1 text-right font-normal text-[var(--ui-muted)]">
                Requests
              </th>
              <th scope="col" class="w-24 py-1 text-right font-normal text-[var(--ui-muted)]">
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
                <tr key={item.id} class="border-t border-[var(--ui-line)]">
                  <td class="py-2 align-middle">
                    <span class={`size-2.5 ${dot.cls}`} role="img" aria-label={dot.word} title={dot.word} />
                  </td>
                  <td class="min-w-0 py-2">
                    <a
                      href={`#/now/${encodeURIComponent(item.id)}`}
                      class="block truncate py-2 hover:text-[var(--ui-accent)]"
                      title={item.id}
                    >
                      {name}
                      {row?.machine ? <span class="text-[var(--ui-muted)]"> · {row.machine}</span> : null}
                    </a>
                  </td>
                  <td class="hidden truncate py-2 sm:table-cell">
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

/** The local midnight that starts the day `now` falls in. */
function startOfDay(now: number): number {
  const day = new Date(now);
  day.setHours(0, 0, 0, 0);
  return day.getTime();
}

/** The sessions card: the sessions live now, the needs-you word from the registry the Now list
 *  shares, and how many sessions saw a request today. */
function SessionsStat() {
  const { state, now } = useApp();
  const { registry } = useHuddle();
  const needs = registry.needs();
  const main = state.sessions.filter((item) => !isJob(item));
  const live = main.filter((item) => item.live).length;
  const today = main.filter((item) => item.lastAt >= startOfDay(now)).length;
  return (
    <Stat
      title="Sessions · live now"
      stat="Sessions"
      value={fmtNum(live)}
      subline={
        <>
          {needs > 0 ? <Status tone="warn" word={`${fmtNum(needs)} need you`} /> : <span>none need you</span>}
          <span class="num">{` · ${fmtNum(today)} today`}</span>
        </>
      }
    />
  );
}

/** The requests card: the range's count, the window named on the tile — all time, said plainly,
 *  when only the summary can answer — the failures in their tone and the cancelled calls apart. */
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
        numbers.history && numbers.failed === 0 ? null : (
          <>
            <Status tone={numbers.failed > 0 ? "err" : "ok"} word={`${fmtNum(numbers.failed)} failed`} />
            {numbers.cancelled > 0 && <span class="num">{` · ${fmtNum(numbers.cancelled)} cancelled`}</span>}
          </>
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

/** The router part of a /health answer, checked at the edge; null when it names no router. */
function routerFactsOf(answer: unknown): RouterFacts | null {
  if (typeof answer !== "object" || answer === null) return null;
  const router: unknown = (answer as { router?: unknown }).router;
  if (typeof router !== "object" || router === null) return null;
  const { running, providers } = router as { running?: unknown; providers?: unknown };
  return {
    running: running === true,
    providers: Array.isArray(providers)
      ? providers.filter((name): name is string => typeof name === "string")
      : null,
  };
}

/** What the hub says about huddle-router, asked once on mount and every ROUTER_POLL_MS after;
 *  null until the first answer names the router. */
function useRouterFacts(): RouterFacts | null {
  const { api } = useHuddle();
  const [facts, setFacts] = useState<RouterFacts | null>(null);
  useEffect(() => {
    let alive = true;
    const ask = (): void => {
      api
        .api("/health")
        .then((answer) => {
          if (alive) setFacts(routerFactsOf(answer));
        })
        .catch(() => {
          if (alive) setFacts(null);
        });
    };
    ask();
    const timer = setInterval(ask, ROUTER_POLL_MS);
    return () => {
      alive = false;
      clearInterval(timer);
    };
  }, [api]);
  return facts;
}

/** The routers card: huddle-router down, or how many of the enabled providers it serves with the
 *  ones a stopped budget names; an enabled list the hub does not send leaves the plain "up". */
function RoutersStat({ facts }: { facts: RouterFacts | null }) {
  const { state } = useApp();
  const stopped = stoppedScopes(state);
  const enabled = facts?.providers ?? null;
  const named = (enabled ?? state.providers).filter((name) => stopped.has(`provider:${name}`));
  const stoppedWord =
    named.length > 0 ? (
      <Status tone="err" word={`${named.join(", ")} budget stopped`} />
    ) : (
      <span>all up</span>
    );
  if (facts === null) return <Stat title="Routers" value="–" subline={null} />;
  if (enabled !== null && enabled.length === 0)
    return <Stat title="Routers" value="–" subline={<span>no provider set up</span>} />;
  if (!facts.running)
    return (
      <Stat title="Routers" value="down" subline={<Status tone="err" word="huddle-router not running" />} />
    );
  if (enabled === null) return <Stat title="Routers" value="up" subline={stoppedWord} />;
  return (
    <Stat
      title="Routers"
      value={`${enabled.length - named.length} of ${enabled.length}`}
      subline={stoppedWord}
    />
  );
}

/** The scopes a stop budget has reached, as the status document names them. */
function stoppedScopes(state: ClientState): Set<string> {
  return new Set((state.budgetStatus?.stopped ?? []).filter((scope) => scope !== "total"));
}

/** One router row: the stopped diamond, the down ring or the up dot, the provider's name, the word on
 *  the right; nothing on the right while the hub has not said whether huddle-router serves. */
function RouterRow({
  plugin,
  stopped,
  running,
}: {
  plugin: string;
  stopped: boolean;
  running: boolean | null;
}) {
  return (
    <div class="grid grid-cols-[14px_1fr_auto] items-center gap-x-2.5 py-1.5 text-sm" data-router={plugin}>
      {stopped ? (
        <span
          class="size-2.5 rotate-45 rounded-[2px]"
          style={`background:${STATUS_COLOR.err}`}
          role="img"
          aria-label="budget stopped"
        />
      ) : running === false ? (
        <span
          class="size-2.5 rounded-full border-2"
          style={`border-color:${STATUS_COLOR.err}`}
          role="img"
          aria-label="down"
        />
      ) : running === true ? (
        <span class="size-2.5 rounded-full bg-[var(--ui-ok)]" role="img" aria-label="up" />
      ) : (
        <span class="size-2.5 rounded-full border border-[var(--ui-idle)]" aria-hidden="true" />
      )}
      <span class="min-w-0 truncate">{plugin}</span>
      {stopped ? (
        <span class="text-xs" style={`color:${STATUS_COLOR.err}`}>
          budget stopped
        </span>
      ) : running === false ? (
        <span class="text-xs" style={`color:${STATUS_COLOR.err}`}>
          down
        </span>
      ) : running === true ? (
        <span class="text-xs text-[var(--ui-muted)]">up</span>
      ) : (
        <span />
      )}
    </div>
  );
}

/** The routers list: the providers huddle-router enables, or every provider plugin the budgets
 *  route knows while the hub does not name them, stopped ones first. */
function RoutersList({ facts }: { facts: RouterFacts | null }) {
  const { state } = useApp();
  const stopped = stoppedScopes(state);
  const providers = facts?.providers ?? state.providers;
  return (
    <section class="ui-card p-4 md:p-5" aria-labelledby="ovw-routers">
      <h2 id="ovw-routers" class="mb-1 text-[15px] font-semibold">
        Routers
      </h2>
      {providers.length === 0 ? (
        <EmptyState
          title="No provider plugins yet"
          hint="A provider shows up here once it is set up: zai, kimi, deepseek, minimax, qwen."
          icon="route"
        />
      ) : (
        <div class="flex flex-col">
          {[...providers]
            .sort(
              (a, b) =>
                Number(stopped.has(`provider:${b}`)) - Number(stopped.has(`provider:${a}`)) ||
                a.localeCompare(b),
            )
            .map((plugin) => (
              <RouterRow
                key={plugin}
                plugin={plugin}
                stopped={stopped.has(`provider:${plugin}`)}
                running={facts?.running ?? null}
              />
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
    <section class="ui-card p-4 md:p-5" aria-labelledby="ovw-tools">
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
              <Bar pct={(tool.count / max) * 100} color={SERIES[0]} />
              <span class="num text-right text-[var(--ui-muted)]">{fmtNum(tool.count)}</span>
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
      class="ui-card flex flex-col gap-2 p-4 md:p-5"
      style={`border-color:color-mix(in oklab, ${STATUS_COLOR[tone]} 40%, transparent)`}
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
        {alert.detail} <span class="text-[var(--ui-muted)]">({alertWhere(alert)})</span>
      </p>
      <div class="mt-1 flex flex-wrap gap-2">
        {alert.kind === "budget" ? (
          <a href="#/settings/radar" class="ui-btn ui-btn-secondary" data-alert-action="">
            <Icon name="dollar" class="ui-icon" />
            Raise budget
          </a>
        ) : (
          <button
            type="button"
            class="ui-btn ui-btn-secondary"
            data-action="tab"
            data-value="alerts"
            onClick={() => act("tab", "alerts")}
          >
            All alerts
          </button>
        )}
        <button
          type="button"
          class="ui-btn ui-btn-ghost"
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
        <Bar
          pct={Math.min(100, Math.round(pct))}
          color={STATUS_COLOR[quotaTone(pct)]}
          class="mt-1"
          meter={{ value: Math.round(pct), label: `${label} plan limit` }}
        />
      )}
      {countdown !== null && <div class="mt-0.5 text-xs text-[var(--ui-muted)]">{countdown}</div>}
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
    <section class="ui-card p-4 md:p-5" aria-labelledby="ovw-quota" data-quota="">
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

/** The window the phone's tiles read: the range's requests and what they cost at list price, with
 *  the count nothing priced, while the requests in memory cover the range; the all-time summary
 *  when only it answers; a history range's own request count with no spend figure otherwise. */
function phoneWindow(state: ClientState, now: number, numbers: ReturnType<typeof rangeNumbers>) {
  const count = requestsInRange(state, now);
  if (count === null)
    return {
      words: "All time",
      requests: state.summary?.requests ?? 0,
      spend: state.summary?.costUsd ?? null,
      unpriced: 0,
    };
  const costs = numbers.requests.map((request) => requestCost(request)?.usd);
  const words = rangeWords(state.range, now);
  return {
    words: words.charAt(0).toUpperCase() + words.slice(1),
    requests: count,
    spend: numbers.history ? null : costs.reduce<number>((sum, usd) => sum + (usd ?? 0), 0),
    unpriced: costs.filter((usd) => usd === undefined).length,
  };
}

/** The phone's first screen: one label naming the window, then spend across the top and requests
 *  beside the sessions live under it, each figure on one line. */
function PhoneTiles({ numbers }: { numbers: ReturnType<typeof rangeNumbers> }) {
  const { state, now } = useApp();
  const { registry } = useHuddle();
  const span = phoneWindow(state, now, numbers);
  const needs = registry.needs();
  const live = state.sessions.filter((item) => !isJob(item) && item.live).length;
  return (
    <section class="grid gap-2" aria-labelledby="ovw-window" data-phone-tiles="">
      <h2 id="ovw-window" class="text-sm font-semibold">
        {span.words}
      </h2>
      <div class="grid grid-cols-2 gap-3">
        <Stat
          title="Spend"
          value={span.spend === null ? "–" : fmtUsd(span.spend)}
          subline={span.unpriced > 0 ? <span class="num">{`${fmtNum(span.unpriced)} unpriced`}</span> : null}
          class="col-span-2"
          oneLine
        />
        <Stat
          title="Requests"
          value={fmtNum(span.requests)}
          subline={
            numbers.failed > 0 ? <Status tone="err" word={`${fmtNum(numbers.failed)} failed`} /> : null
          }
          oneLine
        />
        <Stat
          title="Sessions live"
          stat="Sessions"
          value={fmtNum(live)}
          subline={needs > 0 ? <Status tone="warn" word={`${fmtNum(needs)} need you`} /> : null}
          oneLine
        />
      </div>
    </section>
  );
}

/** The Overview: the stat row over the active-block card, the day card, the graphs, the chart
 *  and the sessions table, the plan limits, the alert, routers and tools beside them; the phone
 *  leads with its three tiles, then the day card, the graphs, the chart, the sessions table
 *  without its model column and the routers. */
export function OverviewView(): JSX.Element {
  const { state, now } = useApp();
  const numbers = rangeNumbers(state, now);
  const routers = useRouterFacts();
  const phone = useLayout() === "phone";
  return (
    <div class="grid gap-4 xl:grid-cols-[minmax(0,1fr)_360px]">
      {phone ? (
        <PhoneTiles numbers={numbers} />
      ) : (
        <div class="grid grid-cols-2 gap-3 md:grid-cols-3 xl:col-span-2 xl:grid-cols-5" data-stat-cards="">
          <SpendStat />
          <RequestsStat state={state} now={now} numbers={numbers} />
          <TokensStat state={state} now={now} numbers={numbers} />
          <SessionsStat />
          <RoutersStat facts={routers} />
        </div>
      )}
      <div class="grid min-w-0 gap-4 xl:col-span-2">
        <ActiveBlock />
      </div>
      <div class="min-w-0 xl:col-span-2">
        <DayCard />
      </div>
      <div class="min-w-0 xl:col-span-2">
        <OverviewGraphs />
      </div>
      <div class="grid min-w-0 gap-4">
        <SpendChart />
        <TopSessions />
      </div>
      <div class="grid content-start gap-4">
        <PlanLimits />
        <div class="hidden md:block">
          <AlertCard />
        </div>
        <RoutersList facts={routers} />
        <div class="hidden md:block">
          <TopTools />
        </div>
      </div>
    </div>
  );
}
