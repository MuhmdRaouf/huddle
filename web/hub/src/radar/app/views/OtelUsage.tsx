/**
 * The usage view's second source: Claude Code's own OpenTelemetry export, which the hub takes at
 * /api/radar/otlp/v1/metrics. Its tokens and cost stand per model on their own, beside the
 * transcripts' report and never summed into it, so the two can be read against each other.
 */

import { useEffect, useState } from "preact/hooks";
import { fmtNum, fmtTokens, fmtUsd } from "../../fmt.ts";
import { Icon } from "../Icon.tsx";
import { EmptyState, ModelChip, Panel } from "../kit.tsx";
import { Table, TableHeadStatic } from "../table.tsx";

/** The read the hub answers with the rows Claude Code's export left. */
export const OTEL_PATH = "/api/radar/api/otel";

/** One model's totals as the export reported them. */
export type OtelModelTotals = {
  model: string;
  input: number;
  output: number;
  cacheRead: number;
  cacheCreation: number;
  costUsd: number;
  sessions: number;
};

/** One data point of the export. */
export type OtelRow = {
  source: "otel";
  metric: string;
  sessionId: string;
  model: string;
  type?: string;
  value: number;
  startTs: number;
  ts: number;
  cumulative: boolean;
};

/** The /api/otel answer. */
export type OtelAnswer = { source: "otel"; points: number; byModel: OtelModelTotals[]; rows: OtelRow[] };

/** Which source the usage view shows. */
export type UsageSource = "transcripts" | "otel";

type Load =
  | { status: "loading" }
  | { status: "error"; error: string }
  | { status: "ready"; answer: OtelAnswer };

const COLUMNS: { key: string; label: string; numeric?: boolean }[] = [
  { key: "model", label: "Model" },
  { key: "sessions", label: "Sessions", numeric: true },
  { key: "input", label: "Input", numeric: true },
  { key: "output", label: "Output", numeric: true },
  { key: "cacheRead", label: "Cache read", numeric: true },
  { key: "cacheCreation", label: "Cache write", numeric: true },
  { key: "cost", label: "Cost", numeric: true },
];

/** The two-way source switch at the head of the usage view. */
export function SourceToggle({ source, pick }: { source: UsageSource; pick: (source: UsageSource) => void }) {
  const segments: { label: string; value: UsageSource }[] = [
    { label: "Transcripts", value: "transcripts" },
    { label: "OpenTelemetry", value: "otel" },
  ];
  return (
    // biome-ignore lint/a11y/useSemanticElements: a toolbar of toggles, not a form; a fieldset would bring its own border and legend
    <div class="segmented join justify-self-start" role="group" aria-label="Source">
      {segments.map((segment) => (
        <button
          key={segment.value}
          type="button"
          class={
            segment.value === source
              ? "segment segment-on btn btn-sm btn-soft btn-primary join-item"
              : "segment btn btn-sm btn-ghost join-item"
          }
          aria-pressed={segment.value === source ? "true" : "false"}
          onClick={() => pick(segment.value)}
        >
          <span>{segment.label}</span>
        </button>
      ))}
    </div>
  );
}

/** The export's totals per model, read once when the source is picked. */
export function OtelUsage() {
  const [load, setLoad] = useState<Load>({ status: "loading" });
  useEffect(() => {
    let live = true;
    void (async () => {
      try {
        const response = await fetch(OTEL_PATH);
        if (!response.ok) throw new Error(`GET ${OTEL_PATH} → ${response.status}`);
        const answer = (await response.json()) as OtelAnswer;
        if (live) setLoad({ status: "ready", answer });
      } catch (error) {
        if (live) setLoad({ status: "error", error: error instanceof Error ? error.message : String(error) });
      }
    })();
    return () => {
      live = false;
    };
  }, []);
  if (load.status === "loading")
    return <div class="skeleton h-64" role="status" aria-label="Loading OpenTelemetry" />;
  if (load.status === "error") {
    return (
      <div role="alert" class="alert alert-error">
        <Icon name="alert" class="icon" />
        <span>Claude Code's OpenTelemetry rows did not load ({load.error}).</span>
      </div>
    );
  }
  const { answer } = load;
  if (answer.byModel.length === 0) {
    return (
      <EmptyState
        title="No OpenTelemetry export yet"
        hint="Start Claude Code with CLAUDE_CODE_ENABLE_TELEMETRY=1, OTEL_METRICS_EXPORTER=otlp and OTEL_EXPORTER_OTLP_ENDPOINT pointing at this hub's /api/radar/otlp; the usage guide has the full setup."
        icon="history"
      />
    );
  }
  return (
    <Panel
      icon="cpu"
      title="By model"
      subtitle={`${fmtNum(answer.points)} points from Claude Code's export`}
      flush
    >
      <p class="px-5 py-3 text-sm opacity-70">
        Claude Code's own counts, kept apart from the transcripts: these numbers are never added to the other
        views.
      </p>
      <Table dataKey="usage-otel">
        <TableHeadStatic columns={COLUMNS} />
        <tbody>
          {answer.byModel.map((totals) => (
            <tr key={totals.model}>
              <td class="min-w-0">
                <ModelChip model={totals.model} />
              </td>
              <td class="num text-right">{fmtNum(totals.sessions)}</td>
              <td class="num text-right">{fmtTokens(totals.input)}</td>
              <td class="num text-right">{fmtTokens(totals.output)}</td>
              <td class="num text-right">{fmtTokens(totals.cacheRead)}</td>
              <td class="num text-right">{fmtTokens(totals.cacheCreation)}</td>
              <td class="num text-right">{fmtUsd(totals.costUsd)}</td>
            </tr>
          ))}
        </tbody>
      </Table>
    </Panel>
  );
}
