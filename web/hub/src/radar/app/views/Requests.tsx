/**
 * The requests view: a model filter, the newest 300 requests as a sortable table, the empty state that
 * shows before traffic streams in, and the "N new requests" pill while the reader is away from the live
 * head.
 */

import { modelColor } from "@huddle/ui/model-color.ts";
import { useEffect, useRef, useState } from "preact/hooks";
import { windowLabels } from "../../chart.ts";
import { bucketWord, fmtClock, fmtCount, fmtDuration, fmtNum, fmtTokens, fmtUsd } from "../../fmt.ts";
import { isInFlight, type RequestRecord } from "../../shared/model.ts";
import { rangeLabel, resolveRange } from "../../shared/time-range.ts";
import {
  agentDisplayName,
  modelChoices,
  oldestRequest,
  requestChartsData,
  type RequestSortKey,
  requestCancelled,
  requestCost,
  tableRequests,
} from "../../state.ts";
import { StackedColumnChart } from "../Chart.tsx";
import { ChartTipBox, useChartTip } from "../ChartTip.tsx";
import { useApp } from "../context.ts";
import { httpStatusOf } from "../inspector/util.ts";
import { EmptyState, ModelChip, Panel, Segmented, shortModelName } from "../kit.tsx";
import { stackedPeak } from "./Usage.tsx";
import { STATUS_COLOR, TOKEN_KINDS } from "../../palette.ts";
import { nowAction } from "../SessionCard.tsx";
import {
  type Column,
  ExportButton,
  Table,
  TableHead,
  TableHeadStatic,
  TableRow,
  timeText,
} from "../table.tsx";

// the fixed columns leave the flexible "what" column about a quarter of a 1440 px desk
const REQUEST_COLUMNS: Column<RequestSortKey>[] = [
  { key: "time", label: "Time", width: "w-20" },
  { key: "agent", label: "Agent", width: "w-32" },
  { key: "what", label: "What" },
  { key: "model", label: "Model", width: "w-44" },
  { key: "status", label: "Status", numeric: true, width: "w-20" },
  { key: "latency", label: "Latency", numeric: true, width: "w-24" },
  { key: "input", label: "Input", numeric: true, width: "w-20" },
  { key: "output", label: "Output", numeric: true, width: "w-24" },
  { key: "cacheRead", label: "Cache read", numeric: true, width: "w-24" },
  { key: "cacheWrite", label: "Cache write", numeric: true, width: "w-24" },
  { key: "cost", label: "Est. cost", numeric: true, width: "w-24" },
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

/** One request as a table row (a card on the phone, led by what it did): fresh rows flash, the open row
 *  sits on the accent at 10% with an accent bar on its left edge, activating it opens the drawer. */
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
  const cancelled = requestCancelled(request);
  return (
    <TableRow
      onOpen={() => act("drawer", request.id)}
      action="drawer"
      value={request.id}
      label={`${request.model} at ${fmtClock(request.ts)}, open details`}
      fresh={fresh}
      active={open}
      dimmed={cancelled}
    >
      <td
        class={`time${open ? " shadow-[inset_3px_0_0_0_var(--ui-accent)]" : ""}`}
        data-label="Time"
        title={time.title}
      >
        {time.text}
      </td>
      <td class="min-w-0" data-label="Agent" title={request.agentId}>
        <span class="block truncate">{agent}</span>
      </td>
      <td class="min-w-0" data-label="What" data-cell="title" title={what}>
        {what === undefined ? (
          <span class="text-[var(--ui-muted)]">–</span>
        ) : (
          <span class="block truncate">
            {isPromptWhat(what) && <span class="font-medium text-[var(--ui-accent)]">Prompt: </span>}
            {action}
          </span>
        )}
      </td>
      <td class="min-w-0" data-label="Model">
        <span class="block truncate">
          <ModelChip model={request.model} />
          {isProviderModel(request.provider) && (
            <span class="model-provider ui-badge ui-badge-neutral ml-1.5 align-middle">
              {request.provider}
            </span>
          )}
          {request.fallback !== undefined && (
            <span
              class="model-fallback ui-badge ui-badge-warn ml-1.5 align-middle"
              title={`${request.fallback} failed 3 times in a row; the router sent this to ${request.model}`}
            >
              from {request.fallback}
            </span>
          )}
        </span>
      </td>
      <td class="num text-right" data-label="Status">
        {status === null ? (
          <span class="text-[var(--ui-muted)]">–</span>
        ) : cancelled ? (
          <span
            class="ui-badge ui-badge-neutral"
            title={`HTTP ${status}: ${request.error ?? "client closed"}`}
          >
            cancelled
          </span>
        ) : (
          <span
            class={`ui-badge ${status >= 400 ? "ui-badge-fail" : "ui-badge-neutral"}`}
            data-http={String(status)}
            title={request.error ?? `HTTP ${status}`}
          >
            {status}
          </span>
        )}
      </td>
      <td class="num text-right" data-label="Latency">
        {fmtDuration(request.latencyMs)}
      </td>
      <td class="num text-right" data-label="Input">
        {fmtNum(request.tokens.input)}
      </td>
      <td class="num text-right" data-label="Output">
        {fmtNum(request.tokens.output)}
      </td>
      <td class="num text-right" data-label="Cache read">
        {fmtNum(request.tokens.cacheRead)}
      </td>
      <td class="num text-right" data-label="Cache write">
        {fmtNum(request.tokens.cacheWrite)}
      </td>
      <td class="num text-right" data-label="Est. cost">
        {streaming ? (
          <span class="text-[var(--ui-muted)]">pending</span>
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
    <button
      type="button"
      class="ui-btn ui-btn-secondary"
      data-action="more-requests"
      onClick={() => act("more-requests")}
    >
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
          <TableHeadStatic columns={REQUEST_COLUMNS} />
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

/** The model filter: one segment per model in view plus "All models", each with the model's colour dot
 *  and its request count. */
function ModelFilter() {
  const { state } = useApp();
  const choices = modelChoices(state);
  if (choices.length <= 1) return null;
  return (
    <Segmented
      action="model"
      label="Filter by model"
      segments={[
        { label: "All models", value: "", on: state.model === null },
        ...choices.map((choice) => ({
          label: shortModelName(choice.model),
          value: choice.model,
          on: state.model === choice.model,
          count: fmtNum(choice.count),
          dot: modelColor(choice.model),
          title: choice.model,
        })),
      ]}
    />
  );
}

/** One band a range chart's tip draws: its values, its colour and its name. */
export type TipBand = { values: number[]; color: string; label: string };

/** One range chart's tip props: the bucket's clock and the series' running total in the head, one
 *  line per band. */
export function rangeTip(
  tip: { index: number; x: number; top: number; height: number },
  bands: TipBand[],
  from: number,
  bucketMs: number,
  head: (total: number) => string,
): { tip: typeof tip; head: string; lines: { color: string; label: string; value: string }[] } {
  const at = from + tip.index * bucketMs;
  const total = bands.reduce((acc, band) => acc + (band.values[tip.index] ?? 0), 0);
  return {
    tip,
    head: `${fmtClock(at)} · ${head(total)}`,
    lines: bands.map((band) => ({
      color: band.color,
      label: band.label,
      value: fmtNum(band.values[tip.index] ?? 0),
    })),
  };
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
  const tipper = useChartTip(data.ok);
  const total = data.ok.reduce((acc, count) => acc + count, 0);
  const failed = data.failed.reduce((acc, count) => acc + count, 0);
  const peak = stackedPeak([data.ok, data.failed]);
  return (
    <div class="min-w-0" data-chart="requests" data-total={String(total)}>
      <h3 class="mb-1 text-sm font-semibold">Requests</h3>
      <div
        class="relative"
        role="img"
        aria-label={`Requests per ${bucketWord(data.bucketMs)} over ${label}: ${fmtNum(total)} total, ${fmtNum(failed)} failed`}
        onPointerMove={tipper.move}
        onPointerLeave={tipper.leave}
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
        {tipper.tip !== null && (
          <ChartTipBox
            {...rangeTip(
              tipper.tip,
              [
                { values: data.ok, color: STATUS_COLOR.ok, label: "Ok" },
                { values: data.failed, color: STATUS_COLOR.err, label: "Failed" },
              ],
              from,
              data.bucketMs,
              (count) => fmtCount(count, "request"),
            )}
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
  const tipper = useChartTip(data.tokens[0] ?? []);
  const total = data.tokens.reduce((acc, values) => acc + values.reduce((a, v) => a + v, 0), 0);
  const peak = stackedPeak(data.tokens);
  return (
    <div class="min-w-0" data-chart="tokens" data-total={String(total)}>
      <h3 class="mb-1 text-sm font-semibold">Tokens</h3>
      <div
        class="relative"
        role="img"
        aria-label={`Tokens per ${bucketWord(data.bucketMs)} over ${label}: ${fmtTokens(total)} total`}
        onPointerMove={tipper.move}
        onPointerLeave={tipper.leave}
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
        {tipper.tip !== null && (
          <ChartTipBox
            {...rangeTip(
              tipper.tip,
              TOKEN_KINDS.map((kind, index) => ({
                values: data.tokens[index] ?? [],
                color: kind.color,
                label: kind.label,
              })),
              from,
              data.bucketMs,
              (count) => `${fmtTokens(count)} tokens`,
            )}
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
        <p class="m-0 text-sm text-[var(--ui-muted)]">No requests in this range.</p>
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
              <div class="border-b border-[var(--ui-line)] p-2">
                <button
                  type="button"
                  class="ui-btn ui-btn-secondary w-full"
                  aria-label={`${held} new ${held === 1 ? "request" : "requests"}, jump to newest`}
                  onClick={() => {
                    setHeldTs(null);
                    const reduced = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
                    wrapRef.current?.scrollTo({ top: 0, behavior: reduced ? "auto" : "smooth" });
                  }}
                >
                  {fmtNum(held)} new {held === 1 ? "request" : "requests"}
                </button>
              </div>
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
