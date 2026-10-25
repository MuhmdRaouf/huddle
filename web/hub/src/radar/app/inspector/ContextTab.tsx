/**
 * The inspector's Context tab: the conversation as the model saw it at this request, rebuilt on the server
 * from the stored sides of the agent's earlier requests. Every turn is a collapsed row — role, kind, size,
 * first line — that opens into its blocks; the totals are a row of three stat cards and earlier pages load on
 * demand. Above the messages sits the prompt the request itself carried — its system prompt and tool
 * definitions, as the router captured them — collapsed by default, with the response headers it answered
 * under.
 */

import { Badge } from "@huddle/ui/badge.tsx";
import { Button } from "@huddle/ui/button.tsx";
import type { ComponentChildren } from "preact";
import { Fragment } from "preact";
import { fmtBytes, fmtCount, fmtNum } from "../../fmt.ts";
import type { RequestRecord } from "../../shared/model.ts";
import type {
  CapturedTool,
  CaptureState,
  ContentBlock,
  ContextMessage,
  ContextState,
  ContextTotals,
} from "../../state.ts";
import { Block } from "../Content.tsx";
import { useApp } from "../context.ts";
import { Code } from "../kit.tsx";
import { captureSystemText, captureToolName, captureTools, firstLine, prettyJson } from "./util.ts";

/** The quiet ink of a hint, a label or a size. */
const MUTED = "text-[var(--ui-muted)]";

/** The quiet line a state shows in place of the conversation. */
const QUIET = `m-0 text-sm ${MUTED}`;

/** A placeholder bar on the well colour, pulsing while the server rebuilds the conversation. */
const PLACEHOLDER = "animate-pulse rounded-[var(--ui-radius-sm)] bg-[var(--ui-surface-2)]";

/** The skeleton of the shape the conversation renders in, while the server rebuilds it. */
function ContextLoading() {
  return (
    <div class="flex flex-col gap-3" role="status" data-context-loading>
      <span class="sr-only">Rebuilding the conversation…</span>
      <div class={`${PLACEHOLDER} h-4 w-3/5`} />
      {[0, 1, 2, 3, 4, 5].map((row) => (
        <div class={`${PLACEHOLDER} h-9`} key={row} />
      ))}
    </div>
  );
}

/** One totals card: its title, its figure and, when it has one, the line that says what it counts. */
function Total({ title, value, desc }: { title: string; value: string; desc?: string }) {
  return (
    <div class="ui-card flex min-w-0 flex-col gap-1 p-4" data-stat={title}>
      <span class={`text-xs ${MUTED}`}>{title}</span>
      <span class="ui-mono text-base font-semibold">{value}</span>
      {desc !== undefined && <span class={`text-xs ${MUTED}`}>{desc}</span>}
    </div>
  );
}

/** A folded row on the card surface: the summary a 44 px tap target with its chevron, the body under it. */
function Fold({
  head,
  children,
  class: cls = "",
}: {
  head: ComponentChildren;
  children: ComponentChildren;
  class?: string;
}) {
  return (
    <details class={`ui-card group ${cls}`.trim()}>
      <summary class="flex min-h-[var(--ui-tap)] cursor-pointer list-none items-center gap-2 px-4 py-2 [&::-webkit-details-marker]:hidden">
        <span class="flex min-w-0 flex-1 items-center gap-2">{head}</span>
        <svg
          class={`size-4 shrink-0 transition-transform group-open:rotate-180 ${MUTED}`}
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          stroke-width="2"
          stroke-linecap="round"
          stroke-linejoin="round"
          aria-hidden="true"
        >
          <path d="m6 9 6 6 6-6" />
        </svg>
      </summary>
      <div class="flex flex-col gap-3 border-t border-[var(--ui-line)] px-4 py-3">{children}</div>
    </details>
  );
}

/** The header numbers: how many turns, roughly how many tokens, how much of it was cache. */
function Totals({ totals }: { totals: ContextTotals }) {
  const usage = totals.usage;
  const contextTokens = usage === null ? null : usage.input + usage.cacheRead + usage.cacheWrite;
  return (
    <div class="grid grid-cols-1 gap-3 sm:grid-cols-3" data-context-totals>
      <Total title="Turns" value={fmtCount(totals.messages, "message")} />
      <Total
        title="From stored text"
        value={`≈ ${fmtNum(totals.approxTokens)}`}
        desc={`tokens${contextTokens !== null && contextTokens > 0 ? `, ${fmtNum(contextTokens)} reported` : ""}`}
      />
      <Total
        title="Cache"
        value={usage === null ? "–" : fmtNum(totals.cacheTokens)}
        desc={
          usage === null
            ? "No usage reported for this request"
            : `of it (read ${fmtNum(usage.cacheRead)}, written ${fmtNum(usage.cacheWrite)})`
        }
      />
    </div>
  );
}

/** One turn: a folded row — role, kind, size, first line — opening into the blocks the side stored. */
function Message({ message }: { message: ContextMessage }) {
  return (
    <Fold
      class="ctx-msg"
      head={
        <>
          <Badge tone={message.role === "user" ? "info" : "ok"}>{message.role}</Badge>
          <Code text={message.kind} class={`shrink-0 text-xs ${MUTED}`} />
          <Badge>{fmtBytes(message.bytes)}</Badge>
          <span class="min-w-0 flex-1 truncate text-sm">{firstLine(message.preview)}</span>
        </>
      }
    >
      {message.blocks.map((block, at) => (
        <Block key={at} block={block as ContentBlock} />
      ))}
    </Fold>
  );
}

/** A failed read on the fail tone, with the cause when the server named one. */
export function ErrorAlert({ words, cause }: { words: string; cause: string | undefined }) {
  return (
    <div
      role="alert"
      class="rounded-[var(--ui-radius)] border border-[var(--ui-fail)] bg-[var(--ui-fail-soft)] p-3 text-sm text-[var(--ui-fail-ink)]"
    >
      <span>
        {words}
        {cause === undefined ? "." : `: ${cause}`}
      </span>
    </div>
  );
}

/** The captured response headers as a small key/value list — the few the allow-list lets through. */
function CaptureHeaders({ headers }: { headers: Record<string, string> }) {
  return (
    <div class="flex flex-col gap-1">
      <span class={`text-xs ${MUTED}`}>Answered under</span>
      {Object.entries(headers).map(([name, value]) => (
        <div class="flex min-w-0 gap-2 text-xs" key={name}>
          <Code text={name} class={`shrink-0 ${MUTED}`} />
          <span class="min-w-0 break-all text-sm">{value}</span>
        </div>
      ))}
    </div>
  );
}

/** One captured tool: its name and description on the collapsed row, its input schema in a collapsed pre. */
function CapturedToolRow({ tool }: { tool: CapturedTool }) {
  const description = typeof tool.description === "string" ? tool.description : "";
  const schema = tool.input_schema;
  return (
    <Fold
      head={
        <>
          <Code text={captureToolName(tool)} class="shrink-0 text-sm" />
          <span class={`min-w-0 flex-1 truncate text-xs ${MUTED}`}>{firstLine(description)}</span>
        </>
      }
    >
      {description !== "" && <p class="m-0 whitespace-pre-wrap text-sm">{description}</p>}
      {schema !== undefined && schema !== null && (
        <Code text={prettyJson(schema)} class="whitespace-pre-wrap text-xs" />
      )}
    </Fold>
  );
}

/** The prompt the request carried, collapsed by default: its size and tool count on the row; the system
 *  prompt text, each tool and the response headers inside. A request without a capture renders nothing. */
function PromptCapture({ capture }: { capture: CaptureState | undefined }) {
  if (capture === undefined || capture.status === "loading") {
    return (
      <p class={`m-0 text-xs ${MUTED}`} role="status">
        Reading the captured prompt…
      </p>
    );
  }
  if (capture.status === "missing") return null;
  if (capture.status === "error") {
    return (
      <ErrorAlert
        words="The captured prompt could not be read; opening the tab again retries"
        cause={capture.cause}
      />
    );
  }
  const tools = captureTools(capture.tools);
  const system = captureSystemText(capture.system);
  return (
    <Fold
      head={
        <span class="flex min-w-0 flex-wrap items-center gap-2">
          <span class="shrink-0 text-sm font-medium">System prompt and tools</span>
          <Badge>{fmtBytes(capture.bytes)}</Badge>
          <Badge>{fmtCount(tools.length, "tool")}</Badge>
        </span>
      }
    >
      {system !== "" && <pre class="ui-mono m-0 whitespace-pre-wrap text-sm">{system}</pre>}
      {tools.map((tool, at) => (
        <CapturedToolRow key={at} tool={tool} />
      ))}
      {capture.headers !== null && <CaptureHeaders headers={capture.headers} />}
    </Fold>
  );
}

/** The states the rebuilt conversation can arrive in, then its turns — with the prompt the request
 *  carried above them whenever the router captured one. */
export function ContextTab({ request }: { request: RequestRecord }) {
  const { state, act } = useApp();
  const held: ContextState | undefined = state.context[request.id];
  const capture: CaptureState | undefined = state.capture[request.id];
  if (held === undefined || held.status === "loading") return <ContextLoading />;
  if (held.status === "missing") {
    return (
      <div class="flex flex-col gap-5">
        <p class={QUIET}>
          Not in the history store: older than retention, or captured before history was on. The conversation
          cannot be rebuilt for this request.
        </p>
      </div>
    );
  }
  if (held.status === "off") {
    return (
      <div class="flex flex-col gap-5">
        <p class={QUIET}>History is off, so the conversation cannot be rebuilt.</p>
      </div>
    );
  }
  if (held.status === "error") {
    return (
      <div class="flex flex-col gap-5">
        <ErrorAlert
          words="The stored conversation could not be read; opening the tab again retries"
          cause={held.cause}
        />
      </div>
    );
  }
  return (
    <div class="flex flex-col gap-5">
      <Totals totals={held.totals} />
      {held.note !== null && <p class={QUIET}>{held.note}</p>}
      <PromptCapture capture={capture} />
      {held.messages.length === 0 ? (
        <p class={QUIET}>
          Nothing was captured for this request's turn: the provider sent no content, or capture was off while
          it ran.
        </p>
      ) : (
        <Fragment>
          {held.next !== null && (
            <Button
              variant="secondary"
              class="w-full"
              data-action="context-more"
              onClick={() => act("context-more")}
            >
              {held.loadingOlder
                ? "Loading…"
                : `Load ${Math.min(40, held.totals.messages - held.messages.length)} earlier messages`}
            </Button>
          )}
          {held.messages.map((message, at) => (
            <Message key={`${message.requestId}:${message.role}:${at}`} message={message} />
          ))}
        </Fragment>
      )}
      {held.messages.length > 0 && held.next === null && (
        <p class={`m-0 text-xs ${MUTED}`}>
          The conversation begins here: everything the transcript kept for this agent.
        </p>
      )}
    </div>
  );
}
