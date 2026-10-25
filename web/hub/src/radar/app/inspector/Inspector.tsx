/**
 * The request inspector: a big slide-over from the right (the shared SlideOver on a native dialog) that
 * floats over the whole dashboard — Escape, a click outside or the X close it, focus returns to the row
 * that opened it. On a phone it covers the whole viewport, tab bar included, and a 44 px Back control
 * leads the header. Tabs — Overview, Input, Output, Context, Raw, each with its count where one exists —
 * sit under the sticky header; previous/next step through the current scope's requests and "Copy as JSON"
 * copies the whole record. Nothing renders without a picked request.
 */

import { Badge } from "@huddle/ui/badge.tsx";
import { SlideOver } from "@huddle/ui/slide-over.tsx";
import { fmtAgo, fmtClock, sessionName } from "../../fmt.ts";
import type { IconName } from "../../icons.ts";
import { isInFlight, type RequestRecord } from "../../shared/model.ts";
import {
  agentDisplayName,
  blocksOf,
  DRAWER_TABS,
  type DrawerTab,
  drawerRequest,
  stepRequest,
} from "../../state.ts";
import { Blocks, CopyButton } from "../Content.tsx";
import { useApp } from "../context.ts";
import { Icon } from "../Icon.tsx";
import { ModelChip } from "../kit.tsx";
import { ContextTab, ErrorAlert } from "./ContextTab.tsx";
import { OverviewTab } from "./Overview.tsx";
import { RawTab } from "./RawTab.tsx";
import { httpStatusOf, prettyJson, rawDocument } from "./util.ts";

/** One icon per inspector tab, so the tab row scans the way the nav does. */
const TAB_ICONS: Record<DrawerTab, IconName> = {
  overview: "gauge",
  input: "arrows",
  output: "sparkle",
  context: "message",
  raw: "cpu",
};

/** The count a tab wears when the stored answer is in hand: its blocks, or its turns; null hides it. */
function tabCount(
  state: ReturnType<typeof useApp>["state"],
  id: DrawerTab,
  requestId: string,
): number | null {
  if (id === "input" || id === "output") {
    const held = state.content[requestId];
    if (held?.status !== "ready") return null;
    return blocksOf(id === "input" ? held.input : held.output)?.length ?? 0;
  }
  if (id === "context") {
    const held = state.context[requestId];
    return held?.status === "ready" ? held.totals.messages : null;
  }
  return null;
}

/** The quiet line a tab shows in place of content it does not hold. */
const QUIET = "m-0 text-sm text-[var(--ui-muted)]";

/** The words for a side the history store cannot give back, and the alert when it broke. */
function ContentSide({ side }: { side: "input" | "output" }) {
  const { state } = useApp();
  if (state.request === null) return null;
  const answer = state.content[state.request];
  if (answer === undefined || answer.status === "loading") {
    return <p class={QUIET}>Reading the stored side…</p>;
  }
  if (answer.status === "missing") {
    return <p class={QUIET}>Not recorded: older than history or captured before history existed.</p>;
  }
  if (answer.status === "off") return <p class={QUIET}>History is off.</p>;
  if (answer.status === "error") {
    return <ErrorAlert words="The stored side could not be read" cause={answer.cause} />;
  }
  return <Blocks side={answer[side]} />;
}

/** The header's two lines: model, agent and stop reason over the time and the session it belongs to;
 *  on a phone the Back control stands before them and closes the dialog the way the X does. */
function DrawerHeader({ request }: { request: RequestRecord }) {
  const { state, now } = useApp();
  const session = state.sessions.find((s) => s.id === request.sessionId);
  const status = httpStatusOf(request);
  const streaming = isInFlight(request);
  return (
    <div class="flex min-w-0 items-start gap-2">
      {/* the kit's button sets its own display, so the wrapper is what hides it from 640 px */}
      <span class="-ml-2 shrink-0 sm:hidden">
        <button
          type="button"
          class="ui-btn ui-btn-ghost ui-btn-icon"
          aria-label="Back to requests"
          onClick={(e) => e.currentTarget.closest("dialog")?.close()}
        >
          <svg
            class="size-5"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            stroke-width="2"
            stroke-linecap="round"
            stroke-linejoin="round"
            aria-hidden="true"
          >
            <path d="m15 18-6-6 6-6" />
          </svg>
        </button>
      </span>
      <div class="min-w-0 flex-1" id="drawer-title">
        <div class="flex flex-wrap items-center gap-x-3 gap-y-1.5">
          <span class="text-lg font-semibold">
            <ModelChip model={request.model} />
          </span>
          <span class="text-sm font-medium text-[var(--ui-text)]" title={request.agentId}>
            {agentDisplayName(state, request.sessionId, request.agentId)}
          </span>
          {status !== null ? (
            <Badge tone={status >= 400 ? "fail" : "neutral"}>HTTP {status}</Badge>
          ) : streaming ? (
            <Badge tone="info">
              <span class="mr-1.5 size-1.5 animate-pulse rounded-full bg-current" aria-hidden="true" />
              Streaming
            </Badge>
          ) : request.stopReason === null ? (
            <Badge>No stop reason</Badge>
          ) : (
            <Badge tone="info">{request.stopReason}</Badge>
          )}
          {request.error !== undefined && (
            <span class="min-w-0 max-w-96 truncate text-sm text-[var(--ui-fail-ink)]" title={request.error}>
              {request.error}
            </span>
          )}
        </div>
        <p class="m-0 mt-1 flex flex-wrap items-center gap-x-3 gap-y-0.5 text-xs text-[var(--ui-muted)]">
          <span>
            {fmtClock(request.ts)}, {fmtAgo(request.ts, now)}
          </span>
          {session !== undefined && <span>{sessionName(session)}</span>}
        </p>
      </div>
    </div>
  );
}

/** Everything recorded about the picked request, floating over the table in a slide-over. */
export function Inspector() {
  const { state, act } = useApp();
  const request = drawerRequest(state);
  // Opening hands focus to the dialog (the native dialog focus lands on its first control); closing hands
  // it back to the row that opened the drawer (SlideOver remembers the opener).
  if (request === null) return null;
  const prevId = stepRequest(state, -1);
  const nextId = stepRequest(state, 1);
  const held = state.content[request.id];
  const rawText = prettyJson(
    rawDocument(request, held?.status === "ready" ? { input: held.input, output: held.output } : null),
  );
  return (
    <SlideOver
      open
      onClose={() => act("close-drawer")}
      label="Request details"
      header={<DrawerHeader request={request} />}
      width="w-full sm:w-[min(68rem,94vw)]"
      actions={
        <>
          <button
            type="button"
            class="ui-btn ui-btn-ghost ui-btn-icon"
            data-action="prev-request"
            aria-label="Newer request (K)"
            title="Newer request (K)"
            disabled={prevId === null}
            onClick={() => act("prev-request")}
          >
            <Icon name="chevronUp" class="size-4.5" />
          </button>
          <button
            type="button"
            class="ui-btn ui-btn-ghost ui-btn-icon"
            data-action="next-request"
            aria-label="Older request (J)"
            title="Older request (J)"
            disabled={nextId === null}
            onClick={() => act("next-request")}
          >
            <Icon name="chevronDown" class="size-4.5" />
          </button>
          <CopyButton text={rawText} label="Copy as JSON" class="ui-btn ui-btn-ghost">
            Copy as JSON
          </CopyButton>
        </>
      }
      tabs={
        <div role="tablist" class="ui-tabs" aria-label="Request detail">
          {DRAWER_TABS.map((tab) => {
            const active = state.drawerTab === tab.id;
            const count = tabCount(state, tab.id, request.id);
            return (
              <button
                key={tab.id}
                type="button"
                role="tab"
                data-action="drawer-tab"
                data-value={tab.id}
                aria-selected={active ? "true" : "false"}
                aria-controls="inspector-panel"
                onClick={() => act("drawer-tab", tab.id)}
              >
                <Icon name={TAB_ICONS[tab.id]} class="size-4.5" />
                <span>{tab.label}</span>
                {count !== null && <Badge>{count}</Badge>}
              </button>
            );
          })}
        </div>
      }
    >
      <div id="inspector-panel" role="tabpanel">
        {state.drawerTab === "overview" && <OverviewTab request={request} />}
        {state.drawerTab === "input" && (
          <div class="inspector-content flex flex-col gap-3">
            <ContentSide side="input" />
          </div>
        )}
        {state.drawerTab === "output" && (
          <div class="inspector-content flex flex-col gap-3">
            <ContentSide side="output" />
          </div>
        )}
        {state.drawerTab === "context" && <ContextTab request={request} />}
        {state.drawerTab === "raw" && <RawTab request={request} />}
      </div>
    </SlideOver>
  );
}
