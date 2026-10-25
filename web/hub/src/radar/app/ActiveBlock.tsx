/**
 * The active block card: the 5-hour metering window a subscription is counting right now — the
 * time left, the tokens and cost so far, the burn rate over the window's active minutes, and
 * the linear projection to its end over a bar that fills as the window runs. Claude's models
 * count by default; the toggle widens the scope to the routed provider models. The overview
 * carries the card and the Blocks view leads with it. Its times read in UTC, like the Blocks view.
 */

import { Button } from "@huddle/ui/button.tsx";
import { Card } from "@huddle/ui/card.tsx";
import { fmtDuration, fmtHour, fmtNum, fmtTokens, fmtUsd } from "../fmt.ts";
import { SERIES } from "../palette.ts";
import type { BlocksReport, UsageBlock } from "../shared/usage.ts";
import { useApp } from "./context.ts";
import { Icon } from "./Icon.tsx";

/** The report's active window, when it holds one. */
export function activeBlockOf(report: BlocksReport): UsageBlock | null {
  return report.blocks.find((block) => block.active) ?? null;
}

/** Every token kind a window counts, summed. */
export function blockTokens(block: UsageBlock): number {
  return block.input + block.output + block.cacheRead + block.cacheWrite;
}

/** How far through its window the clock is, 0..1 — the bar's fill, clamped at both ends. */
export function blockElapsed(block: UsageBlock, now: number): number {
  const span = block.end - block.start;
  if (span <= 0) return 0;
  return Math.min(1, Math.max(0, (now - block.start) / span));
}

/** The scope toggle both cards read: Claude's models alone (what a subscription meters), or
 *  every model — the routed provider ones included. One pressed button per scope. */
function ScopeToggle() {
  const { state, act } = useApp();
  const scopes = [
    { label: "Claude", value: "claude" },
    { label: "All models", value: "all" },
  ] as const;
  return (
    // biome-ignore lint/a11y/useSemanticElements: a toolbar of toggles, not a form; a fieldset would bring its own border and legend
    <div class="flex gap-1" role="group" aria-label="Model scope">
      {scopes.map((scope) => (
        <Button
          key={scope.value}
          variant="secondary"
          pressed={state.blocksModels === scope.value}
          onClick={() => act("blocks-models", scope.value)}
        >
          {scope.label}
        </Button>
      ))}
    </div>
  );
}

/** One figure of the card: its quiet label, its value, and what the value reads. */
function Figure({ label, value, sub }: { label: string; value: string; sub: string }) {
  return (
    <div class="min-w-0">
      <div class="text-xs text-[var(--ui-muted)]">{label}</div>
      <div class="num mt-0.5 text-xl font-semibold md:text-2xl">{value}</div>
      <div class="num mt-0.5 text-xs text-[var(--ui-muted)]">{sub}</div>
    </div>
  );
}

/** The card's body when a window is live: the countdown with the bar under it, and the four
 *  figures the bar's rate projects. */
function LiveBlock({ block, approximate, now }: { block: UsageBlock; approximate: boolean; now: number }) {
  const left = Math.max(0, block.end - now);
  const share = blockElapsed(block, now);
  const marks = approximate ? "~" : "";
  return (
    <div class="grid gap-4" data-active-block="">
      <div class="grid items-baseline gap-x-4 sm:grid-cols-[auto_1fr]">
        <div class="num text-3xl font-semibold" data-block-left="">
          {fmtDuration(left)}
          <span class="ml-1 text-sm font-normal text-[var(--ui-muted)]">left</span>
        </div>
        <p class="text-sm text-[var(--ui-muted)]">
          ends {fmtHour(block.end, "UTC")} UTC · started {fmtHour(block.firstTs, "UTC")} ·{" "}
          {block.requests === 1 ? "1 request" : `${fmtNum(block.requests)} requests`} so far
        </p>
      </div>
      <div>
        <div
          class="flex h-2 overflow-hidden rounded-full bg-[var(--ui-surface-2)]"
          role="img"
          aria-label={`The window is ${Math.round(share * 100)}% through`}
        >
          <div class="bar-seg h-full rounded-full" style={`width:${share * 100}%;background:${SERIES[0]}`} />
        </div>
        <div class="num mt-1 flex justify-between text-xs text-[var(--ui-muted)]">
          <span>{fmtHour(block.start, "UTC")}</span>
          <span>{fmtHour(block.end, "UTC")}</span>
        </div>
      </div>
      <div class="grid grid-cols-2 gap-4 md:grid-cols-4">
        <Figure
          label="Tokens so far"
          value={fmtTokens(blockTokens(block))}
          sub={`since ${fmtHour(block.firstTs, "UTC")}`}
        />
        <Figure label="Est. cost" value={`${marks}${fmtUsd(block.costUsd)}`} sub="at API list price" />
        <Figure
          label="Burn rate"
          value={`${fmtTokens(block.burnTokensPerHour)}/h`}
          sub={`${marks}${fmtUsd(block.burnCostPerHour)}/h`}
        />
        <Figure
          label="Projected to end"
          value={fmtTokens(block.projectedTokens)}
          sub={`${marks}${fmtUsd(block.projectedCostUsd)} at this rate`}
        />
      </div>
    </div>
  );
}

/** What the card says while the report is on its way, or why it did not come. */
function WaitingLine({ error }: { error: string | null }) {
  return (
    <div class="text-sm text-[var(--ui-muted)]" role="status" data-block-loading="">
      {error === null
        ? "Reading the metering blocks…"
        : `The blocks did not load (${error}). The next refresh tries again.`}
    </div>
  );
}

/** What an empty report says: nothing ran, or nothing the scope counts. */
export function noBlockHint(report: BlocksReport, models: "claude" | "all"): string {
  if (report.blocks.length > 0) {
    return `The last block ended ${fmtHour(report.blocks[0]?.end ?? 0, "UTC")} UTC. The next request starts a new one.`;
  }
  if (models === "claude") {
    return "No Claude-model requests in the window. The next one starts a block; “All models” counts the routed provider models too.";
  }
  return "No requests in the window. The next one starts a block.";
}

/** The card itself: the live window when there is one, the word on the last one when there is
 *  not, and the scope toggle either way. */
export function ActiveBlock() {
  const { state, now } = useApp();
  const held = state.blocks;
  const report = held?.report ?? null;
  const active = report === null ? null : activeBlockOf(report);
  return (
    <Card
      title={
        <span class="flex items-center gap-2">
          <Icon name="timer" class="size-4 text-[var(--ui-accent)]" />
          Active block
        </span>
      }
      meta={report === null || report.blocks.length === 0 ? undefined : `${report.blocks.length} in window`}
      actions={<ScopeToggle />}
    >
      <div data-block-card="">
        {report === null ? (
          <WaitingLine error={state.blocksError} />
        ) : active === null ? (
          <div data-block-none="">
            <p class="flex items-center gap-2 text-sm font-medium">
              <Icon name="timer" class="size-4" />
              No active block
            </p>
            <p class="mt-1 text-sm text-[var(--ui-muted)]">{noBlockHint(report, state.blocksModels)}</p>
          </div>
        ) : (
          <LiveBlock block={active} approximate={report.approximate} now={now} />
        )}
      </div>
    </Card>
  );
}
