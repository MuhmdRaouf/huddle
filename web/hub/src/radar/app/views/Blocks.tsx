/**
 * The blocks view: the metering windows the subscription counts — the active-block card on
 * top (time left, tokens, cost, burn rate, the projection bar), then every window in the
 * chosen span as one row: when it ran, what it holds, what it cost, and — for the live one —
 * the burn rate and the projection to its end. Claude's models count by default; the card's
 * toggle widens the scope to the routed provider models.
 */

import { ESTIMATE_NOTE, fmtHour, fmtNum, fmtTokens, fmtUsd } from "../../fmt.ts";
import { BLOCKS_WINDOWS, type BlocksReport, type UsageBlock } from "../../shared/usage.ts";
import { ActiveBlock, blockTokens } from "../ActiveBlock.tsx";
import { useApp } from "../context.ts";
import { EmptyState, Panel, Segmented } from "../kit.tsx";
import { Table, TableHeadStatic, TableRow } from "../table.tsx";

/** One window's span in words: its clock ends, and the local date when it did not start today. */
export function blockWhen(block: UsageBlock, now: number): string {
  const sameDay = new Date(block.start).toDateString() === new Date(now).toDateString();
  const day = sameDay
    ? ""
    : `${new Date(block.start).toLocaleDateString("en-US", { month: "short", day: "numeric" })} `;
  return `${day}${fmtHour(block.start)} – ${fmtHour(block.end)}`;
}

/** One row of the table: a metering window with its usage, its burn when it is the live one. */
function BlockRow({ block, approximate }: { block: UsageBlock; approximate: boolean }) {
  const marks = approximate ? "~" : "";
  return (
    <TableRow key={block.start} active={block.active} label={`The block of ${blockWhen(block, Date.now())}`}>
      <td class="min-w-0">
        <span class="flex items-center gap-2">
          <span
            class={`inline-block size-2.5 shrink-0 ${block.active ? "rounded-full bg-success" : "rounded-full bg-base-content/25"}`}
            role="img"
            aria-label={block.active ? "active" : "ended"}
          />
          <span class="num whitespace-nowrap font-medium">{blockWhen(block, Date.now())}</span>
        </span>
      </td>
      <td class="num text-right">{fmtNum(block.requests)}</td>
      <td class="num text-right">{fmtTokens(blockTokens(block))}</td>
      <td class="num text-right">
        {marks}
        {fmtUsd(block.costUsd)}
      </td>
      <td class="num whitespace-nowrap text-right text-base-content/80">
        {block.active
          ? `${fmtTokens(block.burnTokensPerHour)}/h · ${marks}${fmtUsd(block.burnCostPerHour)}/h`
          : "–"}
      </td>
      <td class="num whitespace-nowrap text-right text-base-content/80">
        {block.active
          ? `${fmtTokens(block.projectedTokens)} · ${marks}${fmtUsd(block.projectedCostUsd)}`
          : "–"}
      </td>
    </TableRow>
  );
}

/** The list: one row per window, the live one marked, newest first as the report carries them. */
function BlocksTable({ report }: { report: BlocksReport }) {
  return (
    <Table dataKey="blocks">
      <TableHeadStatic
        columns={[
          { key: "when", label: "Window" },
          { key: "requests", label: "Requests", numeric: true },
          { key: "tokens", label: "Tokens", numeric: true },
          { key: "cost", label: "Est. cost", numeric: true },
          { key: "burn", label: "Burn rate", numeric: true },
          { key: "projected", label: "Projected to end", numeric: true },
        ]}
      />
      <tbody>
        {report.blocks.map((block) => (
          <BlockRow key={block.start} block={block} approximate={report.approximate} />
        ))}
      </tbody>
    </Table>
  );
}

/** The whole view: the active-block card over the windows table. */
export function BlocksView() {
  const { state } = useApp();
  const held = state.blocks;
  if (held === null) {
    if (state.blocksError !== null) {
      return (
        <div role="alert" class="alert alert-error">
          <span>
            The blocks did not load ({state.blocksError}). The next refresh tries again; the windows come back
            with it.
          </span>
        </div>
      );
    }
    return (
      <div class="grid gap-5" aria-live="polite">
        <div class="skeleton h-40" role="status" aria-label="Loading blocks" />
        <div class="skeleton h-64" />
      </div>
    );
  }
  const report = held.report;
  return (
    <div class="grid gap-5" data-blocks-view="">
      <ActiveBlock />
      <Panel
        icon="history"
        title="Metering blocks"
        subtitle={`${report.blocks.length} in the last ${state.blocksDays} days`}
        actions={
          <Segmented
            action="blocks-days"
            label="Window"
            segments={BLOCKS_WINDOWS.map((days) => ({
              label: `${days} days`,
              value: String(days),
              on: state.blocksDays === days,
            }))}
          />
        }
        flush
      >
        {report.blocks.length === 0 ? (
          <EmptyState
            title="No blocks in this window"
            hint={
              state.blocksModels === "claude"
                ? "A block starts at the first request after five quiet hours. No Claude-model request ran in this window; “All models” counts the routed provider models too."
                : "A block starts at the first request after five quiet hours. Nothing ran in this window."
            }
            icon="timer"
          />
        ) : (
          <div data-blocks-table="">
            <BlocksTable report={report} />
          </div>
        )}
      </Panel>
      <p class="text-meta text-base-content/60">{ESTIMATE_NOTE}</p>
    </div>
  );
}
