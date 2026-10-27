/**
 * The inspector's Input and Output panes for a request that kept its bodies. The input pane reads as the
 * conversation did on the wire — the system text, then one section per turn behind its role chip, every
 * tool call and result folded into a row that opens. The output pane reads as the answer did — its words
 * and its own tool calls, headed by the token count. When the store gives no bodies, the pane says why,
 * and points at Settings › Hub when bodies are off. A request without a body id renders nothing: its
 * panes keep the stored history sides.
 */

import { Button } from "@huddle/ui/button.tsx";
import type { ComponentChildren } from "preact";
import { useState } from "preact/hooks";
import type { RequestRecord } from "../../shared/model.ts";
import { fmtNum } from "../../fmt.ts";
import { useApp } from "../context.ts";
import { Icon } from "../Icon.tsx";
import { ErrorAlert } from "./ContextTab.tsx";
import { parseInput, parseOutput, type BodyBlock } from "./wire.ts";

/** The quiet ink of a hint, a label or a size. */
const MUTED = "text-[var(--ui-muted)]";

/** The quiet line a state shows in place of the bodies. */
const QUIET = `m-0 text-sm ${MUTED}`;

/** A role chip: the small square tag each turn wears. */
const CHIP =
  "rounded-[var(--ui-radius-sm)] bg-[var(--ui-surface-2)] px-2 py-0.5 text-xs font-semibold text-[var(--ui-text)]";

/** A body shown raw when it does not parse as what it claims to be. */
function RawBody({ text }: { text: string }) {
  return (
    <pre class="ui-mono m-0 whitespace-pre-wrap break-words rounded-[var(--ui-radius-sm)] bg-[var(--ui-surface-2)] p-3 text-xs">
      {text}
    </pre>
  );
}

const firstLine = (text: string): string => {
  const line = text.split("\n").find((candidate) => candidate.trim() !== "");
  return line === undefined ? "" : line.trim();
};

/** The first argument of a tool call, as the row's summary: the first key's value when it is words. */
function firstArgument(input: string): string {
  let parsed: unknown;
  try {
    parsed = JSON.parse(input);
  } catch {
    return firstLine(input);
  }
  if (typeof parsed !== "object" || parsed === null) return firstLine(input);
  for (const [key, value] of Object.entries(parsed)) {
    return typeof value === "string" ? value : key;
  }
  return "";
}

/** One folded row: the tap-height summary — icon, name, one line of mono — opening into its body. */
function FoldRow({
  icon,
  ink,
  label,
  summary,
  body,
}: {
  icon: "wrench" | "check";
  ink: string;
  label: string;
  summary: string;
  body: string;
}) {
  const [open, setOpen] = useState(false);
  return (
    <div class="flex w-full flex-col gap-2">
      <div class="w-full rounded-[var(--ui-radius)] border border-[var(--ui-line)]">
        <Button
          variant="ghost"
          class="w-full"
          aria-expanded={open ? "true" : "false"}
          onClick={() => setOpen(!open)}
        >
          <span
            class={`flex size-7 shrink-0 items-center justify-center rounded-[var(--ui-radius-sm)] bg-[var(--ui-surface-2)] ${ink}`}
          >
            <Icon name={icon} class="size-4" />
          </span>
          <span class="shrink-0 text-sm font-medium text-[var(--ui-text)]">{label}</span>
          <span class={`ui-mono min-w-0 flex-1 truncate text-xs ${MUTED}`}>{summary}</span>
          <Icon
            name="chevronRight"
            class={`size-4 shrink-0 transition-transform ${open ? "rotate-90" : ""} ${MUTED}`}
          />
        </Button>
      </div>
      {open && <RawBody text={body} />}
    </div>
  );
}

/** One block: plain words, or a tool call or result folded into a row. */
function BlockView({ block }: { block: BodyBlock }) {
  if (block.kind === "text") {
    return <p class="m-0 whitespace-pre-wrap break-words text-sm">{block.text}</p>;
  }
  if (block.kind === "tool_use") {
    return (
      <FoldRow
        icon="wrench"
        ink="text-[var(--ui-text)]"
        label={block.name}
        summary={firstArgument(block.input)}
        body={block.input}
      />
    );
  }
  return (
    <FoldRow
      icon="check"
      ink={block.ok ? "text-[var(--ui-ok)]" : "text-[var(--ui-fail-ink)]"}
      label="Result"
      summary={`${block.ok ? "ok" : "error"} · ${block.lines} lines`}
      body={block.text}
    />
  );
}

/** One section of the bodies card: its chip when it wears one, then its blocks; the first section
 *  carries no top rule. */
function Section({ chip, roleChip, blocks }: { chip?: string; roleChip?: boolean; blocks: BodyBlock[] }) {
  return (
    <div class="flex flex-col items-start gap-2 border-t border-[var(--ui-line)] p-3 first:border-t-0">
      {chip !== undefined && (
        <span class={CHIP} data-role-chip={roleChip ? "" : undefined}>
          {chip}
        </span>
      )}
      {blocks.map((block, at) => (
        <BlockView key={at} block={block} />
      ))}
    </div>
  );
}

/** The card the kept side renders in: surface, hairline, its sections stacked. */
function BodiesCard({ children }: { children: ComponentChildren }) {
  return <div class="ui-card flex flex-col items-stretch overflow-hidden">{children}</div>;
}

/** The system prompt's own section, clamped to three lines until Show all unfolds it. */
function SystemSection({ system }: { system: string }) {
  const [open, setOpen] = useState(false);
  const long = system.length > 240;
  return (
    <div class="flex flex-col items-start gap-2 p-3">
      <span class={CHIP}>system</span>
      <p class={`m-0 whitespace-pre-wrap break-words text-sm ${long && !open ? "line-clamp-3" : ""}`}>
        {system}
      </p>
      {long && (
        <Button variant="ghost" onClick={() => setOpen(!open)}>
          {open ? "Show less" : "Show all"}
        </Button>
      )}
    </div>
  );
}

/** The Input and Output panes for one request, reading its kept bodies through the hub. */
export function Bodies({ side, request }: { side: "input" | "output"; request: RequestRecord }) {
  const { state } = useApp();
  const bodyId = request.bodyId;
  if (!bodyId) return null;
  const answer = state.bodies[bodyId];
  if (answer === undefined || answer.status === "loading") {
    return <p class={QUIET}>Reading the request bodies…</p>;
  }
  if (answer.status === "missing") {
    return (
      <p class={QUIET}>This request's bodies were not kept (the store was full or they are past 7 days).</p>
    );
  }
  if (answer.status === "off") {
    return (
      <p class={QUIET}>
        Request bodies are off. Turn them on in{" "}
        <a href="#/settings/hub" class="text-[var(--ui-accent)]">
          Settings › Hub
        </a>
        .
      </p>
    );
  }
  if (answer.status === "forbidden") {
    return <p class={QUIET}>Only the owner reads request bodies.</p>;
  }
  if (answer.status === "error") {
    return <ErrorAlert words="The request bodies could not be read" cause={answer.cause} />;
  }
  if (side === "input") {
    const parsed = parseInput(answer.input);
    return (
      <div class="flex flex-col gap-2">
        <p class={`m-0 text-xs ${MUTED}`}>As sent to {request.provider} · redacted · kept 7 days</p>
        <BodiesCard>
          {parsed === null ? (
            <div class="p-3">
              <RawBody text={answer.input} />
            </div>
          ) : (
            <>
              {parsed.system !== "" && <SystemSection system={parsed.system} />}
              {parsed.messages.map((message, at) => (
                <Section key={at} chip={message.role} roleChip blocks={message.blocks} />
              ))}
            </>
          )}
        </BodiesCard>
      </div>
    );
  }
  const parsed = parseOutput(answer.output);
  const head =
    parsed === null
      ? "Answer"
      : `${parsed.streamed ? "Streamed answer" : "Answer"}${
          parsed.outputTokens === null ? "" : ` · ${fmtNum(parsed.outputTokens)} tokens`
        }`;
  return (
    <div class="flex flex-col gap-2">
      <p class={`m-0 text-xs ${MUTED}`}>{head}</p>
      <BodiesCard>
        {parsed === null ? (
          <div class="p-3">
            <RawBody text={answer.output} />
          </div>
        ) : (
          parsed.blocks.map((block, at) => <Section key={at} blocks={[block]} />)
        )}
      </BodiesCard>
    </div>
  );
}
