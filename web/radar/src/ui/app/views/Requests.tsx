/**
 * The requests view: a model filter, the newest 300 requests as a sortable table, the empty state that
 * shows before traffic streams in, and the "N new requests" pill while the reader is away from the live
 * head.
 */

import { modelColor } from "@huddle/ui/model-color.ts";
import { useEffect, useRef, useState } from "preact/hooks";
import { nearestIndex, windowLabels } from "../../chart.ts";
import { bucketWord, fmtClock, fmtCount, fmtDuration, fmtNum, fmtTokens, fmtUsd } from "../../fmt.ts";
import { STATUS_COLOR, TOKEN_KINDS } from "../../palette.ts";
import { isInFlight, type RequestRecord } from "../../../shared/model.ts";
import { rangeLabel, resolveRange } from "../../../shared/time-range.ts";
import {
  agentDisplayName,
  modelChoices,
  oldestRequest,
  requestChartsData,
  type RequestSortKey,
  requestCost,
  tableRequests,
} from "../../state.ts";
import { StackedColumnChart } from "../Chart.tsx";
import { useApp } from "../context.ts";
import { httpStatusOf } from "../inspector/util.ts";
import { EmptyState, ModelChip, Panel, shortModelName } from "../kit.tsx";
import { stackedPeak } from "./Usage.tsx";
import { nowAction } from "../SessionCard.tsx";
import { type Column, ExportButton, HEAD_CELL, Table, TableHead, TableRow, timeText } from "../table.tsx";

const REQUEST_COLUMNS: Column<RequestSortKey>[] = [
  { key: "time", label: "Time", width: "w-24" },
  { key: "agent", label: "Agent", width: "w-44" },
  { key: "what", label: "What" },
  { key: "model", label: "Model", width: "w-48" },
  { key: "status", label: "Status", numeric: true, width: "w-24" },
  { key: "latency", label: "Latency", numeric: true, width: "w-24" },
  { key: "input", label: "Input", numeric: true, width: "w-28" },
  { key: "output", label: "Output", numeric: true, width: "w-28" },
  { key: "cacheRead", label: "Cache read", numeric: true, width: "w-28" },
  { key: "cacheWrite", label: "Cache write", numeric: true, width: "w-28" },
  { key: "cost", label: "Est. cost", numeric: true, width: "w-28" },
];

/** How a request reached its upstream: "via zai router" (a router's own line), or the router's address
 *  when only the session's base URL said so; null for a direct call. The inspector's overview reuses it. */
export function viaText(via: string | undefined): string | null {
  if (via === undefined) return null;
  return via.includes(":") ? `via router at ${via}` : `via ${via} router`;
}

/** A model served by a provider plugin rather than Anthropic: its rows say whose model it is. */
export function isProviderModel(provider: string): boolean {
  return provider !== "Anthropic" && provider !== "other" && provider !== "";
}

/** Requests that arrived after the moment the reader looked away (the "N new requests" pill's count). */
export function newSince(requests: RequestRecord[], heldTs: number | null): number {
  if (heldTs === null) return 0;
  let count = 0;
  for (const request of requests) if (request.ts > heldTs) count += 1;
  return count;
}

/** The newest timestamp in the list: the moment to count arrivals from when the reader leaves the head. */
function newestTs(requests: RequestRecord[]): number {
  let newest = 0;
  for (const request of requests) if (request.ts > newest) newest = request.ts;
  return newest;
}

/** A `what` line that starts at a prompt: the cell leads with a quiet "Prompt:" before the action. */
export function isPromptWhat(what: string): boolean {
  return /^\s*↳?\s*prompt\b/.test(what);
}

/** One request as a table row: fresh rows flash, the open row sits on primary at 8% with a primary bar on
 *  its left edge, activating it opens the drawer. */
export function RequestRow({ request, now }: { request: RequestRecord; now: number }) {
  const { state, act } = useApp();
  const fresh = now - request.ts <= 1500;
  const open = state.request === request.id;
  const time = timeText(state, request.ts, now);
  const streaming = isInFlight(request);
  const agent = agentDisplayName(state, request.sessionId, request.agentId);
  const what = request.what;
  const action = what === undefined ? null : nowAction(what);
  const status = httpStatusOf(request);
  return (
    <TableRow
      onOpen={() => act("drawer", request.id)}
      action="drawer"
      value={request.id}
      label={`${request.model} at ${fmtClock(request.ts)}, open details`}
      fresh={fresh}
      active={open}
    >
      <td class={`time${open ? " shadow-[inset_3px_0_0_0_var(--color-primary)]" : ""}`} title={time.title}>
        {time.text}
      </td>
      <td class="min-w-0" title={request.agentId}>
        <span class="block truncate">{agent}</span>
      </td>
      <td class="min-w-0" title={what}>
        {what === undefined ? (
          <span class="text-base-content/60">–</span>
        ) : (
          <span class="block truncate">
            {isPromptWhat(what) && <span class="font-medium text-primary">Prompt: </span>}
            {action}
          </span>
        )}
      </td>
      <td class="min-w-0">
        <span class="block truncate">
          <ModelChip model={request.model} />
          {isProviderModel(request.provider) && (
            <span class="model-provider badge badge-ghost badge-sm ml-1.5 align-middle">
              {request.provider}
            </span>
          )}
        </span>
      </td>
      <td class="num text-right">
        {status === null ? (
          <span class="text-base-content/60">–</span>
        ) : (
          <span
            class={`badge badge-soft badge-sm${status >= 400 ? " badge-error" : ""}`}
            title={request.error ?? `HTTP ${status}`}
          >
            {status}
          </span>
        )}
      </td>
      <td class="num text-right">{fmtDuration(request.latencyMs)}</td>
      <td class="num text-right">{fmtNum(request.tokens.input)}</td>
      <td class="num text-right">{fmtNum(request.tokens.output)}</td>
      <td class="num text-right">{fmtNum(request.tokens.cacheRead)}</td>
      <td class="num text-right">{fmtNum(request.tokens.cacheWrite)}</td>
      <td class="num text-right">
        {streaming ? (
          <span class="text-base-content/60">pending</span>
        ) : (
          fmtUsd(requestCost(request)?.usd ?? null)
        )}
      </td>
    </TableRow>
  );
}

/** The requests tab while a history session or agent is picked: the store's paged rows for that scope. */
export function HistoryRequestsPanel() {
  const { state, now, act } = useApp();
  const requests = state.historyRequests;
  const loadMore = state.historyRequestsNext !== null && (
    <button type="button" class="btn btn-sm" data-action="more-requests" onClick={() => act("more-requests")}>
      {state.historyRequestsLoading ? "Loading…" : "Load more"}
    </button>
  );
  return (
    <Panel
      icon="arrows"
      flush
      title="Requests"
      subtitle={requests === null ? "Loading…" : `${fmtNum(requests.length)} from history`}
      actions={loadMore}
    >
      {requests === null ? (
        <EmptyState
          title="Loading this scope's requests"
          hint="The history store is being read; the rows land in a moment."
          icon="arrows"
        />
      ) : requests.length === 0 ? (
        <EmptyState
          title="No requests recorded for this scope"
          hint={
            state.historyRequestsError ??
            "Nothing the history store kept for this session or agent; pick another scope from the rail."
          }
          icon="arrows"
        />
      ) : (
        <Table dataKey="history-requests" columns={REQUEST_COLUMNS}>
          <thead>
            <tr>
              {REQUEST_COLUMNS.map((column) => (
                <th
                  key={column.key}
                  class={[HEAD_CELL, column.numeric === true ? "num text-right" : ""]
                    .filter(Boolean)
                    .join(" ")}
                  scope="col"
                >
                  {column.label}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {requests.map((request) => (
              <RequestRow key={request.id} request={request} now={now} />
            ))}
          </tbody>
        </Table>
      )}
    </Panel>
  );
}

/** The live requests tab: filter pills per model in view, the capped request table, and the pill that
 *  counts what arrived while the reader was scrolled away or reading a row. */
export function RequestsView() {
  const { state } = useApp();
  if (state.historyScope !== null) return <HistoryRequestsPanel />;
  return <LiveRequestsPanel />;
}

/** The model filter: a daisyUI join of buttons, one per model in view plus "All models", each with the
 *  model's colour dot and its request count. */
function ModelFilter() {
  const { state, act } = useApp();
  const choices = modelChoices(state);
  if (choices.length <= 1) return null;
  return (
    // biome-ignore lint/a11y/useSemanticElements: a toolbar of filter pills, not a form; a fieldset would bring its own border and legend
    <div class="join" role="group" aria-label="Filter by model">
      <button
        type="button"
        class={
          state.model === null
            ? "btn btn-sm btn-soft btn-primary join-item"
            : "btn btn-sm btn-ghost join-item"
        }
        data-action="model"
        data-value=""
        aria-pressed={state.model === null ? "true" : "false"}
        onClick={() => act("model", "")}
      >
        <span>All models</span>
      </button>
      {choices.map((choice) => (
        <button
          key={choice.model}
          type="button"
          class={
            state.model === choice.model
              ? "btn btn-sm btn-soft btn-primary join-item"
              : "btn btn-sm btn-ghost join-item"
          }
          data-action="model"
          data-value={choice.model}
          aria-pressed={state.model === choice.model ? "true" : "false"}
          title={choice.model}
          onClick={() => act("model", choice.model)}
        >
          <span
            class="inline-block size-2 shrink-0 rounded-full"
            style={`background:${modelColor(choice.model)}`}
            aria-hidden="true"
          />
          <span>{shortModelName(choice.model)}</span>
          <span class="badge badge-sm badge-ghost num">{fmtNum(choice.count)}</span>
        </button>
      ))}
    </div>
  );
}

/** Where the tooltip sits: right of the pointer, or left of it when it would run off the window. */
function tipLeft(x: number, width: number): number {
  const left = x + 14 + width > window.innerWidth ? x - 14 - width : x + 14;
  return Math.max(8, left);
}

/** One range chart's tip: the bucket's clock and the series' running total in the head, one line
 *  per series. */
function RangeTip({
  tip,
  series,
  colors,
  labels,
  from,
  bucketMs,
  head,
}: {
  tip: { index: number; x: number; top: number; height: number };
  series: number[][];
  colors: string[];
  labels: string[];
  from: number;
  bucketMs: number;
  head: (total: number) => string;
}) {
  const at = from + tip.index * bucketMs;
  const total = series.reduce((acc, values) => acc + (values[tip.index] ?? 0), 0);
  return (
    <>
      <div
        class="chart-tip"
        style={{ display: "block", left: `${tipLeft(tip.x, 190)}px`, top: `${tip.top + 8}px` }}
      >
        <div class="tip-head">{`${fmtClock(at)} \u00b7 ${head(total)}`}</div>
        {series.map((values, index) => (
          <div key={labels[index]} class="tip-line">
            <span class="swatch" style={{ background: colors[index] }} />
            <span>{labels[index]}</span>
            <span class="tip-value">{fmtNum(values[tip.index] ?? 0)}</span>
          </div>
        ))}
      </div>
      <div
        class="chart-cross"
        style={{ display: "block", left: `${tip.x}px`, top: `${tip.top}px`, height: `${tip.height}px` }}
      />
    </>
  );
}

/** The requests chart: ok and failed counts as one stacked bar per bucket of the range. */
function RequestsChart({
  data,
  from,
  to,
  now,
  label,
}: {
  data: ReturnType<typeof requestChartsData>;
  from: number;
  to: number;
  now: number;
  label: string;
}) {
  const [tip, setTip] = useState<{ index: number; x: number; top: number; height: number } | null>(null);
  const total = data.ok.reduce((acc, count) => acc + count, 0);
  const failed = data.failed.reduce((acc, count) => acc + count, 0);
  const peak = stackedPeak([data.ok, data.failed]);
  const hover = (event: PointerEvent): void => {
    const box = (event.currentTarget as HTMLElement).querySelector("svg")?.getBoundingClientRect();
    if (box === undefined || box.width <= 0) return;
    const index = nearestIndex(data.ok, event.clientX - box.left, box.width);
    setTip(index === null ? null : { index, x: event.clientX, top: box.top, height: box.height });
  };
  return (
    <div class="min-w-0" data-chart="requests" data-total={String(total)}>
      <h3 class="mb-1 text-sm font-semibold">Requests</h3>
      <div
        class="relative"
        role="img"
        aria-label={`Requests per ${bucketWord(data.bucketMs)} over ${label}: ${fmtNum(total)} total, ${fmtNum(failed)} failed`}
        onPointerMove={hover}
        onPointerLeave={() => setTip(null)}
      >
        <StackedColumnChart
          series={[
            { values: data.ok, color: STATUS_COLOR.ok },
            { values: data.failed, color: STATUS_COLOR.err },
          ]}
          width={720}
          height={180}
          xLabels={windowLabels(from, to, now)}
          yLabel={peak > 1 ? `${fmtNum(peak)} per ${bucketWord(data.bucketMs)}` : null}
        />
        {tip !== null && (
          <RangeTip
            tip={tip}
            series={[data.ok, data.failed]}
            colors={[STATUS_COLOR.ok, STATUS_COLOR.err]}
            labels={["Ok", "Failed"]}
            from={from}
            bucketMs={data.bucketMs}
            head={(count) => fmtCount(count, "request")}
          />
        )}
      </div>
    </div>
  );
}

/** The tokens chart: the four token kinds as one stacked bar per bucket of the range. */
function TokensChart({
  data,
  from,
  to,
  now,
  label,
}: {
  data: ReturnType<typeof requestChartsData>;
  from: number;
  to: number;
  now: number;
  label: string;
}) {
  const [tip, setTip] = useState<{ index: number; x: number; top: number; height: number } | null>(null);
  const total = data.tokens.reduce((acc, values) => acc + values.reduce((a, v) => a + v, 0), 0);
  const peak = stackedPeak(data.tokens);
  const hover = (event: PointerEvent): void => {
    const box = (event.currentTarget as HTMLElement).querySelector("svg")?.getBoundingClientRect();
    if (box === undefined || box.width <= 0) return;
    const index = nearestIndex(data.tokens[0] ?? [], event.clientX - box.left, box.width);
    setTip(index === null ? null : { index, x: event.clientX, top: box.top, height: box.height });
  };
  return (
    <div class="min-w-0" data-chart="tokens" data-total={String(total)}>
      <h3 class="mb-1 text-sm font-semibold">Tokens</h3>
      <div
        class="relative"
        role="img"
        aria-label={`Tokens per ${bucketWord(data.bucketMs)} over ${label}: ${fmtTokens(total)} total`}
        onPointerMove={hover}
        onPointerLeave={() => setTip(null)}
      >
        <StackedColumnChart
          series={TOKEN_KINDS.map((kind, index) => ({
            values: data.tokens[index] ?? [],
            color: kind.color,
          }))}
          width={720}
          height={180}
          xLabels={windowLabels(from, to, now)}
          yLabel={peak > 1 ? `${fmtTokens(peak)} per ${bucketWord(data.bucketMs)}` : null}
        />
        {tip !== null && (
          <RangeTip
            tip={tip}
            series={TOKEN_KINDS.map((_, index) => data.tokens[index] ?? [])}
            colors={TOKEN_KINDS.map((kind) => kind.color)}
            labels={TOKEN_KINDS.map((kind) => kind.label)}
            from={from}
            bucketMs={data.bucketMs}
            head={(count) => `${fmtTokens(count)} tokens`}
          />
        )}
      </div>
    </div>
  );
}

/** The two charts above the table: requests by outcome and tokens by kind over the selected range,
 *  both drawn from the same narrowed list the table shows. An empty range reads as one line. */
function RequestsCharts({ requests }: { requests: RequestRecord[] }) {
  const { state, now } = useApp();
  const label = rangeLabel(state.range, now);
  if (requests.length === 0) {
    return (
      <Panel icon="activity" title="Requests and tokens" subtitle={label}>
        <p class="text-sm text-base-content/60">No requests in this range.</p>
      </Panel>
    );
  }
  const oldest = oldestRequest(state.requests);
  const { from, to } = resolveRange(state.range, now, oldest ?? undefined);
  const data = requestChartsData(requests, from, to);
  return (
    <Panel
      icon="activity"
      title="Requests and tokens"
      subtitle={`${label}, ${bucketWord(data.bucketMs)} per bar`}
      flush
    >
      <div class="grid gap-6 px-5 py-4 xl:grid-cols-2" data-requests-charts="">
        <RequestsChart data={data} from={from} to={to} now={now} label={label} />
        <TokensChart data={data} from={from} to={to} now={now} label={label} />
      </div>
    </Panel>
  );
}

function LiveRequestsPanel() {
  const { state, now } = useApp();
  const requests = tableRequests(state);
  const shown = requests.slice(0, 300);
  const subtitle =
    requests.length > shown.length
      ? `Newest ${fmtNum(shown.length)} of ${fmtNum(requests.length)}`
      : `${fmtNum(requests.length)} in this view`;

  // While the reader is away from the live head (scrolled down or reading a row) arrivals pile up behind a
  // pill; jumping back — or scrolling back to the top — releases the table to follow the newest again.
  const wrapRef = useRef<HTMLDivElement>(null);
  const [heldTs, setHeldTs] = useState<number | null>(null);
  const held = newSince(requests, heldTs);

  useEffect(() => {
    if (state.request === null) return;
    setHeldTs((current) => current ?? newestTs(requests));
  }, [state.request, requests]);

  useEffect(() => {
    const wrap = wrapRef.current;
    if (wrap === null) return;
    const onScroll = () => {
      if (wrap.scrollTop > 4) setHeldTs((current) => current ?? newestTs(requests));
      else setHeldTs(null);
    };
    wrap.addEventListener("scroll", onScroll);
    return () => wrap.removeEventListener("scroll", onScroll);
  }, [requests]);

  return (
    <div class="grid gap-5">
      <RequestsCharts requests={requests} />
      <Panel
        icon="arrows"
        flush
        title="Requests"
        subtitle={subtitle}
        actions={
          <>
            <ModelFilter />
            <ExportButton kind="requests" count={requests.length} />
          </>
        }
      >
        {requests.length === 0 ? (
          <EmptyState
            title={
              state.model === null
                ? "No requests in this view yet"
                : `No ${state.model} requests in this view`
            }
            hint="Model requests stream in live as Claude Code talks to its model. Pick a row to see its tokens and latency."
            icon="arrows"
          />
        ) : (
          <div class="relative">
            {held > 0 && (
              <button
                type="button"
                class="btn btn-soft btn-primary btn-block rounded-none border-0 border-b border-base-content/8"
                aria-label={`${held} new ${held === 1 ? "request" : "requests"}, jump to newest`}
                onClick={() => {
                  setHeldTs(null);
                  const reduced = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
                  wrapRef.current?.scrollTo({ top: 0, behavior: reduced ? "auto" : "smooth" });
                }}
              >
                {fmtNum(held)} new {held === 1 ? "request" : "requests"}
              </button>
            )}
            <Table dataKey="requests-table" columns={REQUEST_COLUMNS} wrapRef={wrapRef}>
              <TableHead columns={REQUEST_COLUMNS} sort={state.requestSort} action="sort-requests" />
              <tbody>
                {shown.map((request) => (
                  <RequestRow key={request.id} request={request} now={now} />
                ))}
              </tbody>
            </Table>
          </div>
        )}
      </Panel>
    </div>
  );
}
