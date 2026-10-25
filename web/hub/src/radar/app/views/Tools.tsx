/**
 * The tools view: the most-used ranking with success/failure bars, and the recent-calls table (dense rows on
 * a desk, a card per call on the phone) with an all/failed filter.
 */

import { fmtCount, fmtDuration, fmtNum, sessionName } from "../../fmt.ts";
import { STATUS_COLOR } from "../../palette.ts";
import type { ToolCallRecord } from "../../shared/model.ts";
import { type ClientState, type ToolSortKey, tableTools } from "../../state.ts";
import { StackedBar } from "../Chart.tsx";
import { useApp } from "../context.ts";
import { Badge, Code, EmptyState, Panel, Segmented } from "../kit.tsx";
import { type Column, ExportButton, Table, TableHead, TableRow, timeText } from "../table.tsx";

const TOOL_COLUMNS: Column<ToolSortKey>[] = [
  { key: "time", label: "Time", width: "w-28" },
  { key: "tool", label: "Tool", width: "w-40" },
  { key: "session", label: "Session" },
  { key: "agent", label: "Agent", width: "w-32" },
  { key: "duration", label: "Duration", numeric: true, width: "w-28" },
  { key: "result", label: "Result", width: "w-36" },
];

function nameOfSession(state: ClientState, id: string | null): string {
  if (id === null) return "No session";
  const item = state.sessions.find((s) => s.id === id);
  return item === undefined ? id.slice(0, 8) : sessionName(item);
}

/** One recent call: when it started, what ran, for whom, how long, and whether it worked. */
function ToolRow({ tool }: { tool: ToolCallRecord }) {
  const { state, now } = useApp();
  const when = timeText(state, tool.startedAt, now);
  const session = nameOfSession(state, tool.sessionId);
  return (
    <TableRow failed={!tool.ok}>
      <td class="time" data-label="Time" title={when.title}>
        {when.text}
      </td>
      <td class="min-w-0" data-label="Tool" data-cell="title" title={tool.name}>
        <span class="block truncate">
          <Code text={tool.name} class="font-semibold text-[var(--ui-text)]" />
        </span>
      </td>
      <td class="min-w-0" data-label="Session" title={session}>
        <span class="block truncate">{session}</span>
      </td>
      <td data-label="Agent">
        <Code text={(tool.agentId ?? "main").slice(0, 12)} class="text-[var(--ui-muted)]" />
      </td>
      <td class="num text-right" data-label="Duration">
        {fmtDuration(tool.durationMs)}
      </td>
      <td data-label="Result">
        {tool.ok ? (
          <Badge text="Succeeded" tone="ok" icon="check" />
        ) : (
          <Badge text="Failed" tone="err" icon="close" />
        )}
      </td>
    </TableRow>
  );
}

/** One ranking row: the tool, its call count, its failure badge, and the success/failure bar. */
function RankRow({ row, max }: { row: { name: string; count: number; failures: number }; max: number }) {
  return (
    <div class="grid gap-2 py-3.5 first:pt-0 last:pb-0">
      <div class="flex flex-wrap items-center gap-x-3 gap-y-1.5">
        <Code text={row.name} class="font-medium" data-rank-name />
        <span class="text-sm text-[var(--ui-muted)]" data-rank-meta>
          {fmtCount(row.count, "call")}
        </span>
        {row.failures > 0 ? (
          <Badge text={`${fmtNum(row.failures)} failed`} tone="err" />
        ) : (
          <Badge text="No failures" tone="ok" />
        )}
      </div>
      <StackedBar
        segments={[
          { value: row.count - row.failures, color: "var(--radar-series-5)" },
          { value: row.failures, color: STATUS_COLOR.err },
        ]}
        max={max}
      />
    </div>
  );
}

/** The tools tab: which tools run most across every session, then the view's recent calls. */
export function ToolsView() {
  const { state } = useApp();
  const ranking = [...(state.models?.tools ?? [])].sort((a, b) => b.count - a.count);
  const maxTool = Math.max(...ranking.map((t) => t.count), 1);
  const calls = tableTools(state);
  const all = tableTools({ ...state, toolFilter: "all" });
  const failures = all.filter((t) => !t.ok).length;
  const shown = calls.slice(0, 300);
  return (
    <div class="grid grid-cols-1 gap-5">
      <Panel icon="wrench" title="Most used tools" subtitle="Every session, success and failure">
        {ranking.length === 0 ? (
          <EmptyState
            title="No tool calls yet"
            hint="Tool calls appear once an agent reads, edits or runs something."
            icon="wrench"
          />
        ) : (
          <div class="divide-y divide-[var(--ui-line)]">
            {ranking.map((row) => (
              <RankRow key={row.name} row={row} max={maxTool} />
            ))}
          </div>
        )}
      </Panel>
      <Panel
        icon="clock"
        flush
        title="Recent calls"
        subtitle={state.toolFilter === "failed" ? "Only the calls that failed" : "Every call in this view"}
        actions={
          <>
            <Segmented
              action="tool-filter"
              label="Filter tool calls"
              segments={[
                { label: "All", value: "all", on: state.toolFilter === "all", count: fmtNum(all.length) },
                {
                  label: "Failed",
                  value: "failed",
                  on: state.toolFilter === "failed",
                  count: fmtNum(failures),
                },
              ]}
            />
            <ExportButton kind="tools" count={calls.length} />
          </>
        }
      >
        {shown.length === 0 ? (
          <EmptyState
            title={state.toolFilter === "failed" ? "No failed tool calls" : "No tool calls in this view"}
            hint={
              state.toolFilter === "failed"
                ? "Every tool call in this view succeeded."
                : "Calls land here as agents use their tools."
            }
            icon="check"
          />
        ) : (
          <Table dataKey="tools-table" columns={TOOL_COLUMNS}>
            <TableHead columns={TOOL_COLUMNS} sort={state.toolSort} action="sort-tools" />
            <tbody>
              {shown.map((tool) => (
                <ToolRow key={tool.id} tool={tool} />
              ))}
            </tbody>
          </Table>
        )}
      </Panel>
    </div>
  );
}
