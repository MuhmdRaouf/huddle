/**
 * The inspector's Overview tab, read top-down: the four numbers that size the call up (latency against the
 * view's p95, total tokens, estimated cost, retries — a stat card each), the token mix as
 * one stacked bar with its legend and its kind-by-kind table, where the call ran (session, agent, project,
 * router, upstream, provider), and the identifiers with their copy buttons. A burst of retries wears its
 * own warning alert, because a number alone never says what to do about it.
 */

import { Button } from "@huddle/ui/button.tsx";
import type { ComponentChildren } from "preact";
import { Fragment } from "preact";
import { costDetailText, fmtDuration, fmtNum, fmtPercent, hostOf, sessionName } from "../../fmt.ts";
import { SERIES } from "../../palette.ts";
import { isInFlight, type RequestRecord, totalTokens } from "../../shared/model.ts";
import { agentDisplayName, requestCancelled, requestCost, requestFailed, stepScope } from "../../state.ts";
import { StackedBar } from "../Chart.tsx";
import { CopyButton } from "../Content.tsx";
import { useApp } from "../context.ts";
import { Icon } from "../Icon.tsx";
import type { IconName } from "../../icons.ts";
import { cardName } from "../SessionCard.tsx";
import { viaText } from "../views/Requests.tsx";
import { nearbyRetries, RETRY_LOOKBACK_MS, retriesText, httpStatusOf } from "./util.ts";

/** The tones a latency can earn: the three a comparison produces (never idle or info). */
type LatencyTone = "ok" | "warn" | "err";

/** Latency against the view's p95 as a tone: under 60% is fine, under 80% is a warning, the rest is slow. */
export function latencyTone(ratio: number): LatencyTone {
  return ratio < 0.6 ? "ok" : ratio < 0.8 ? "warn" : "err";
}

/** The verdict a latency earns, in words and in a semantic colour a colour-blind reader still gets. */
const LATENCY_WORDS: Record<LatencyTone, string> = {
  ok: "Fast for this view",
  warn: "Slower than most",
  err: "Among the slowest",
};
const LATENCY_TEXT: Record<LatencyTone, string> = {
  ok: "text-[var(--ui-ok-ink)]",
  warn: "text-[var(--ui-warn-ink)]",
  err: "text-[var(--ui-fail-ink)]",
};

/** One token kind's fill: the chart series the token-flow chart paints the same kinds in. */
const TOKEN_FILL = {
  input: SERIES[0],
  output: SERIES[1],
  cacheRead: SERIES[4],
  cacheWrite5m: SERIES[3],
  cacheWrite1h: SERIES[5],
} as const;

/** The quiet ink of a label, a hint or a share. */
const MUTED = "text-[var(--ui-muted)]";

/** One panel on the card surface: its icon and title in the head, the content in the body. */
function Card({
  title,
  meta,
  icon,
  children,
}: {
  title: string;
  meta?: string;
  icon: IconName;
  children: ComponentChildren;
}) {
  return (
    <section class="ui-card">
      <div class="ui-card-head">
        <Icon name={icon} class="size-4.5 text-[var(--ui-accent)]" />
        <h2 class="ui-card-title">{title}</h2>
        {meta !== undefined && <span class="ui-card-meta">{meta}</span>}
      </div>
      <div class="ui-card-body">{children}</div>
    </section>
  );
}

/** One stat card: its title, its figure and the line under it that says what the figure reads. */
function Stat({
  title,
  value,
  desc,
  valueClass = "",
}: {
  title: string;
  value: ComponentChildren;
  desc: ComponentChildren;
  valueClass?: string;
}) {
  return (
    <div class="ui-card flex min-w-0 flex-col gap-1 p-4" data-stat={title}>
      <span class={`text-xs ${MUTED}`}>{title}</span>
      <span class={`ui-mono text-xl font-semibold ${valueClass}`} data-stat-value>
        {value}
      </span>
      <span class={`text-xs ${MUTED}`} data-stat-desc>
        {desc}
      </span>
    </div>
  );
}

/** One row of the tokens table: a colour dot when the kind has one, its tokens and its share. */
function TokenRow({
  label,
  value,
  total,
  color,
}: {
  label: string;
  value: number;
  total: number;
  color?: string;
}) {
  return (
    <tr>
      <td>
        <span class="flex items-center gap-2 text-sm">
          {color !== undefined && (
            <span class="size-2.5 shrink-0 rounded-full" style={`background:${color}`} aria-hidden="true" />
          )}
          {label}
        </span>
      </td>
      <td class="ui-mono ui-end">
        {value === 0 ? <span title="Not reported for this request">–</span> : fmtNum(value)}
      </td>
      <td class={`ui-mono ui-end ${MUTED}`}>
        {value === 0 || total === 0 ? "–" : fmtPercent(value / total)}
      </td>
    </tr>
  );
}

/** The Est. cost stat: the sum, the conditions that shaped it, and the honest words when nothing prices.
 *  A request still streaming shows "pending" — its opening numbers are not the bill. */
function CostStat({ request }: { request: RequestRecord }) {
  if (isInFlight(request)) {
    return (
      <Stat
        title="Est. cost"
        value={
          <span class="flex items-center gap-1.5">
            <span class="size-2 animate-pulse rounded-full bg-[var(--ui-info)]" aria-hidden="true" />
            <span>pending</span>
          </span>
        }
        desc="Still streaming; the bill lands when it ends."
      />
    );
  }
  const cost = requestCost(request);
  if (cost === null) {
    return (
      <Stat title="Est. cost" value="tokens only" valueClass="text-xl" desc="No list price for this model." />
    );
  }
  const [sum, ...detail] = costDetailText(cost).split(" · ");
  return <Stat title="Est. cost" value={sum} desc={detail.join(" · ")} />;
}

/** The retries stat: quiet when there are none, in the fail ink when there are. */
function Retries({ retries }: { retries: RequestRecord[] }) {
  return (
    <Stat
      title="Retries"
      value={fmtNum(retries.length)}
      valueClass={retries.length === 0 ? "" : "text-[var(--ui-fail-ink)]"}
      desc={retriesText(retries)}
    />
  );
}

/** The burst of retries as a warning under the stat row — a number alone never says what to do. */
function RetryBurst({ retries }: { retries: RequestRecord[] }) {
  if (retries.length === 0) return null;
  return (
    <div
      role="alert"
      class="flex items-start gap-3 rounded-[var(--ui-radius)] border border-[var(--ui-warn)] bg-[var(--ui-warn-soft)] p-3 text-[var(--ui-warn-ink)]"
      data-retry-burst
    >
      <Icon name="alert" class="mt-0.5 size-4.5 shrink-0" />
      <div class="min-w-0">
        <p class="m-0 font-medium">{retriesText(retries)}</p>
        <p class="m-0 text-xs">
          Failed attempts the view holds for this agent in the {Math.round(RETRY_LOOKBACK_MS / 60_000)}{" "}
          minutes before the request.
        </p>
      </div>
    </div>
  );
}

/** The request's failure or its cancellation as its own alert — the stat row's numbers stay neutral.
 *  A cancellation (status 499) says so in its own words; a failure carries the error the route recorded. */
function FailedCard({ request }: { request: RequestRecord }) {
  const cancelled = requestCancelled(request);
  if (!cancelled && !requestFailed(request)) return null;
  const status = httpStatusOf(request);
  return (
    <div
      role="alert"
      class="flex items-start gap-3 rounded-[var(--ui-radius)] border border-[var(--ui-fail)] bg-[var(--ui-fail-soft)] p-3 text-[var(--ui-fail-ink)]"
      data-failed
    >
      <Icon name="alert" class="mt-0.5 size-4.5 shrink-0" />
      <div class="min-w-0">
        <p class="m-0 font-medium">{cancelled ? "Cancelled" : "Failed"}</p>
        <p class="m-0 text-xs break-words">
          {cancelled
            ? "The request was cancelled before the model finished."
            : (request.error ?? `The route answered HTTP ${status}.`)}
        </p>
      </div>
    </div>
  );
}
/** The request's tokens as one bar and one table: every kind's share of the whole, coloured the same. */
function TokensPanel({ request }: { request: RequestRecord }) {
  const t = request.tokens;
  const total = totalTokens(t);
  const write1h = request.cacheWrite1h ?? 0;
  const write5m = Math.max(0, t.cacheWrite - write1h);
  const kinds = [
    { label: "Input", value: t.input, fill: TOKEN_FILL.input },
    { label: "Output", value: t.output, fill: TOKEN_FILL.output },
    { label: "Cache read", value: t.cacheRead, fill: TOKEN_FILL.cacheRead },
    { label: "Cache write (5 min)", value: write5m, fill: TOKEN_FILL.cacheWrite5m },
    ...(write1h > 0 ? [{ label: "Cache write (1 h)", value: write1h, fill: TOKEN_FILL.cacheWrite1h }] : []),
  ];
  const shown = kinds.filter((kind) => kind.value > 0);
  return (
    <Card title="Tokens" meta={`${fmtNum(total)} total`} icon="coins">
      <div class="flex flex-col gap-4">
        <div class="flex flex-col gap-2">
          <StackedBar segments={shown.map((kind) => ({ value: kind.value, color: kind.fill }))} max={total} />
          <div class="flex flex-wrap gap-x-5 gap-y-1">
            {shown.map((kind) => (
              <span class="flex items-center gap-1.5 text-sm">
                <span class="size-2.5 rounded-full" style={`background:${kind.fill}`} aria-hidden="true" />
                {kind.label}
                <span class={`ui-mono ${MUTED}`}>{fmtNum(kind.value)}</span>
              </span>
            ))}
          </div>
        </div>
        <div class="overflow-x-auto">
          <table class="ui-table">
            <thead>
              <tr>
                <th scope="col">Kind</th>
                <th scope="col" class="ui-end">
                  Tokens
                </th>
                <th scope="col" class="ui-end">
                  Share
                </th>
              </tr>
            </thead>
            <tbody>
              {kinds.map((kind) => (
                <TokenRow
                  key={kind.label}
                  label={kind.label}
                  value={kind.value}
                  total={total}
                  color={kind.fill}
                />
              ))}
              {t.thinking !== undefined && t.thinking > 0 && (
                <TokenRow label="Thinking (of output)" value={t.thinking} total={total} />
              )}
              <tr class="font-semibold">
                <td>Total</td>
                <td class="ui-mono ui-end">{fmtNum(total)}</td>
                <td class="ui-mono ui-end">{total === 0 ? "–" : "100%"}</td>
              </tr>
            </tbody>
          </table>
        </div>
        {total === 0 && (
          <p class={`m-0 text-sm ${MUTED}`}>The provider reported no token usage for this request.</p>
        )}
      </div>
    </Card>
  );
}

/** One definition row of "Where it ran": its name above its value. */
function Fact({ label, children }: { label: string; children: ComponentChildren }) {
  return (
    <div class="min-w-0">
      <dt class={`text-xs ${MUTED}`}>{label}</dt>
      <dd class="m-0 mt-0.5 text-sm">{children}</dd>
    </div>
  );
}

/** A mono identifier line with its copy button, for the Identifiers panel. */
function Identifier({ label, value }: { label: string; value: string }) {
  return (
    <div class="flex items-center gap-3">
      <span class={`w-24 shrink-0 text-xs ${MUTED}`}>{label}</span>
      <code class="ui-mono min-w-0 flex-1 truncate text-sm" title={value}>
        {value}
      </code>
      <CopyButton
        text={value}
        label={`Copy ${label.toLowerCase()}`}
        class="ui-btn ui-btn-ghost ui-btn-icon"
      />
    </div>
  );
}

/** Everything recorded about the request itself: its numbers, its tokens, its route, its ids. */
export function OverviewTab({ request }: { request: RequestRecord }) {
  const { state, act } = useApp();
  const session = state.sessions.find((s) => s.id === request.sessionId);
  const p95 = state.summary?.latencyP95 ?? null;
  const t = request.tokens;
  const total = totalTokens(t);
  const ratio = request.latencyMs === null || p95 === null || p95 <= 0 ? null : request.latencyMs / p95;
  const tone: LatencyTone | null = ratio === null ? null : latencyTone(ratio);
  const agentScope = JSON.stringify({ sessionId: request.sessionId, agentId: request.agentId });
  const retries = nearbyRetries(stepScope(state), request);
  return (
    <div class="flex flex-col gap-5">
      <div class="flex flex-col gap-3">
        <div class="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <Stat
            title="Latency"
            value={fmtDuration(request.latencyMs)}
            desc={
              tone === null ? (
                "No view p95 to compare with yet."
              ) : (
                <Fragment>
                  <span class={`font-medium ${LATENCY_TEXT[tone]}`}>{LATENCY_WORDS[tone]}</span>
                  <span class="block">Measured against the view's p95 of {fmtDuration(p95)}.</span>
                </Fragment>
              )
            }
          />
          <Stat
            title="Total tokens"
            value={fmtNum(total)}
            desc={`${fmtNum(t.input)} in, ${fmtNum(t.output)} out`}
          />
          <CostStat request={request} />
          <Retries retries={retries} />
        </div>
        <RetryBurst retries={retries} />
        <FailedCard request={request} />
      </div>
      <Card title="Timing and result" icon="timer">
        <dl class="grid grid-cols-1 gap-x-8 gap-y-4 sm:grid-cols-2">
          <Fact label="TTFB">
            <span class="ui-mono">{fmtDuration(request.ttfbMs)}</span>
          </Fact>
          <Fact label="TTFT">
            <span class="ui-mono">{fmtDuration(request.ttftMs)}</span>
          </Fact>
          <Fact label="Status">
            <span class="ui-mono">
              {httpStatusOf(request) === null ? "–" : String(httpStatusOf(request))}
            </span>
          </Fact>
          <Fact label="Stop">
            <span class="ui-mono">{request.stopReason ?? "–"}</span>
          </Fact>
        </dl>
      </Card>
      <TokensPanel request={request} />
      <Card title="Where it ran" icon="route">
        <dl class="grid grid-cols-1 gap-x-8 gap-y-4 sm:grid-cols-2">
          <Fact label="Session">{session === undefined ? request.sessionId : cardName(session)}</Fact>
          <Fact label="Agent">
            <span class="flex flex-col items-start gap-1.5">
              <span title={request.agentId}>
                {agentDisplayName(state, request.sessionId, request.agentId)}
              </span>
              <Button
                variant="secondary"
                data-action="scope-agent"
                data-value={agentScope}
                onClick={() => act("scope-agent", agentScope)}
              >
                Show this agent's requests
              </Button>
            </span>
          </Fact>
          <Fact label="Project">{session === undefined ? "–" : sessionName(session)}</Fact>
          <Fact label="Through">{viaText(request.via) ?? "Direct"}</Fact>
          <Fact label="Upstream">
            <span class="ui-mono break-all text-sm">{hostOf(request.upstream)}</span>
          </Fact>
          <Fact label="Provider">{request.provider}</Fact>
        </dl>
      </Card>
      <Card title="Identifiers" icon="dot">
        <div class="flex flex-col gap-3">
          <Identifier label="Session id" value={request.sessionId} />
          <Identifier label="Request id" value={request.id} />
          {request.parentAgentId !== undefined && (
            <Identifier label="Parent agent" value={request.parentAgentId} />
          )}
        </div>
      </Card>
    </div>
  );
}
