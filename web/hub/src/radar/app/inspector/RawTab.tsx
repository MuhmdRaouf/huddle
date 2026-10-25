/**
 * The inspector's Raw tab: the stored request record and its content as pretty JSON in a code well,
 * with one "Copy as JSON" for the whole request — the research's answer to copying forty attributes one
 * by one.
 */

import type { RequestRecord } from "../../shared/model.ts";
import { CopyButton } from "../Content.tsx";
import { useApp } from "../context.ts";
import { prettyJson, rawDocument } from "./util.ts";

/** The raw record and content in a code well, with the whole-request copy button. */
export function RawTab({ request }: { request: RequestRecord }) {
  const { state } = useApp();
  const held = state.content[request.id];
  const content = held?.status === "ready" ? { input: held.input, output: held.output } : null;
  const text = prettyJson(rawDocument(request, content));
  return (
    <div class="flex flex-col gap-3">
      <div class="raw-actions flex items-center gap-2.5">
        <CopyButton text={text} label="Copy raw JSON" class="ui-btn ui-btn-ghost ui-btn-icon" />
        <span class="raw-hint text-xs text-[var(--ui-muted)]">
          {held === undefined || held.status === "loading"
            ? "The stored sides load with the Input or Output tab; showing the record alone."
            : held.status === "ready"
              ? "The request record and both stored sides."
              : "The request record; the stored sides are unavailable."}
        </span>
      </div>
      {/* the well scrolls on its own, so it takes the focus a keyboard scrolls it with */}
      <div
        class="raw-code max-h-[70vh] overflow-auto rounded-[var(--ui-radius)] border border-[var(--ui-line)] bg-[var(--ui-surface-2)] p-4 outline-none focus-visible:ring-2 focus-visible:ring-[var(--ui-accent)]"
        tabIndex={0}
      >
        <pre class="ui-mono m-0 whitespace-pre-wrap text-xs wrap-anywhere">
          <code>{text}</code>
        </pre>
      </div>
    </div>
  );
}
