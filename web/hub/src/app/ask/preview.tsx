// preview.tsx — what one tool's ask shows before the owner allows it: the Bash command with its
// why, an Edit or MultiEdit as a line diff with a fold, the Write's head, the fetch's URL and the
// search's query, and any other tool's input as one row per key. Every box wraps; nothing scrolls
// sideways.

import { Button } from "@huddle/ui/button.tsx";
import type { JSX } from "preact";
import { useState } from "preact/hooks";
import { lineDiff, type DiffLine } from "./diff.ts";

/** The code box every preview fact sits in: mono, wrapping, on the raised surface. */
const BOX =
  "ui-mono block whitespace-pre-wrap break-words rounded-[var(--ui-radius)] border border-[var(--ui-line)] bg-[var(--ui-surface-2)] p-3 text-sm";
const MUTED = "text-[var(--ui-muted)]";

/** The rows one fold shows before the owner asks for the whole diff. */
const FOLD = 12;

/** The JSON object an input carries, or null when the input is not one. */
const parseFacts = (input: string): Record<string, unknown> | null => {
  try {
    const v: unknown = JSON.parse(input);
    return isFacts(v) ? v : null;
  } catch {
    return null;
  }
};

/** Whether the value is a plain object (not an array, not null). */
const isFacts = (v: unknown): v is Record<string, unknown> =>
  v !== null && typeof v === "object" && !Array.isArray(v);

/** The string the fact carries, or null when it is not one. */
const str = (v: unknown): string | null => (typeof v === "string" ? v : null);

/** The edit list a MultiEdit carries, or null when the input is not a well-formed one. */
const editsOf = (facts: Record<string, unknown>): { old_string: string; new_string: string }[] | null => {
  const raw = facts.edits;
  if (!Array.isArray(raw) || raw.length === 0) return null;
  const out: { old_string: string; new_string: string }[] = [];
  for (const e of raw) {
    if (!isFacts(e)) return null;
    const before = e.old_string;
    const after = e.new_string;
    if (typeof before !== "string" || typeof after !== "string") return null;
    out.push({ old_string: before, new_string: after });
  }
  return out;
};

/** The count of rows carrying one sign. */
const count = (rows: DiffLine[], sign: DiffLine["sign"]): number =>
  rows.filter((r) => r.sign === sign).length;

/** The file row: the path with an ellipsis, then the change counts in the ok and fail inks. */
const FilePath = ({
  path,
  added,
  removed,
}: {
  path: string | null;
  added: number | null;
  removed: number | null;
}): JSX.Element => (
  <p class="m-0 flex items-center gap-2">
    {path === null ? null : <span class="ui-mono min-w-0 truncate text-sm">{path}</span>}
    {added === null || removed === null ? null : (
      <span class="shrink-0 text-xs">
        <span class="text-[var(--ui-ok-ink)]">+{added}</span>{" "}
        <span class="text-[var(--ui-fail-ink)]">−{removed}</span>
      </span>
    )}
  </p>
);

/** One diff row: the gutter sign, then the line, wrapping inside the row's own fill. */
const Row = ({ line }: { line: DiffLine }): JSX.Element => (
  <div
    data-sign={line.sign}
    class={`flex gap-2 px-3 ${
      line.sign === "-" ? "bg-[var(--ui-fail-soft)]" : line.sign === "+" ? "bg-[var(--ui-ok-soft)]" : ""
    }`}
  >
    <span
      aria-hidden="true"
      class={`ui-mono shrink-0 ${
        line.sign === "-"
          ? "text-[var(--ui-fail-ink)]"
          : line.sign === "+"
            ? "text-[var(--ui-ok-ink)]"
            : MUTED
      }`}
    >
      {line.sign === "-" ? "−" : line.sign === "+" ? "+" : " "}
    </span>
    <span class="min-w-0 whitespace-pre-wrap break-words">{line.text === "" ? " " : line.text}</span>
  </div>
);

/** The line diff, folded at twelve rows until the owner opens the rest in place. */
const DiffBlock = ({ rows }: { rows: DiffLine[] }): JSX.Element => {
  const [open, setOpen] = useState(false);
  const shown = open ? rows : rows.slice(0, FOLD);
  const hidden = rows.length - FOLD;
  return (
    <div
      data-diff
      class="flex flex-col divide-y divide-[var(--ui-line)] rounded-[var(--ui-radius)] border border-[var(--ui-line)] py-1"
    >
      {shown.map((line, i) => (
        <Row key={`${i}:${line.text}`} line={line} />
      ))}
      {open || hidden <= 0 ? null : (
        <Button variant="ghost" onClick={() => setOpen(true)}>
          Show the whole diff · {hidden} more changes
        </Button>
      )}
    </div>
  );
};

/** The Bash ask: the command to copy, then the why the caller gave. */
const BashFacts = ({ command, why }: { command: string; why: string | null }): JSX.Element => (
  <>
    <div class="relative">
      <code class={`${BOX} pr-12`}>{command}</code>
      <Button
        variant="ghost"
        class="absolute right-1 top-1"
        onClick={() => void navigator.clipboard?.writeText(command)}
      >
        Copy
      </Button>
    </div>
    {why === null ? null : (
      <>
        <h3 class="m-0 text-sm font-semibold">Why</h3>
        <p class={`m-0 whitespace-pre-wrap break-words text-sm ${MUTED}`}>{why}</p>
      </>
    )}
  </>
);

/** The Edit ask: the file row with its counts, then the change as a line diff. */
const EditFacts = ({
  path,
  before,
  after,
}: {
  path: string | null;
  before: string;
  after: string;
}): JSX.Element => {
  const rows = lineDiff(before, after);
  return (
    <>
      <FilePath path={path} added={count(rows, "+")} removed={count(rows, "-")} />
      <DiffBlock rows={rows} />
    </>
  );
};

/** The MultiEdit ask: the file row with the totals, then one counted diff per edit. */
const MultiEditFacts = ({
  path,
  edits,
}: {
  path: string | null;
  edits: { old_string: string; new_string: string }[];
}): JSX.Element => {
  const all = edits.map((e) => lineDiff(e.old_string, e.new_string));
  return (
    <>
      <FilePath
        path={path}
        added={all.reduce((n, rows) => n + count(rows, "+"), 0)}
        removed={all.reduce((n, rows) => n + count(rows, "-"), 0)}
      />
      {all.map((rows, i) => (
        <div key={i} class="flex flex-col gap-2">
          <FilePath path={null} added={count(rows, "+")} removed={count(rows, "-")} />
          <DiffBlock rows={rows} />
        </div>
      ))}
    </>
  );
};

/** The Write ask: the file row, then the head of the content. */
const WriteFacts = ({ path, content }: { path: string | null; content: string }): JSX.Element => {
  const lines = content.split("\n");
  return (
    <>
      <FilePath path={path} added={null} removed={null} />
      <code class={BOX}>{lines.slice(0, FOLD).join("\n")}</code>
      {lines.length > FOLD ? <p class={`m-0 text-xs ${MUTED}`}>{lines.length - FOLD} more lines</p> : null}
    </>
  );
};

/** Any other tool: one row per top-level key, objects and arrays as compact JSON. */
const KeyRows = ({ facts }: { facts: Record<string, unknown> }): JSX.Element => (
  <dl class="m-0 flex flex-col gap-2">
    {Object.entries(facts).map(([k, v]) => (
      <div key={k} class="flex flex-col gap-0.5">
        <dt class={`text-xs ${MUTED}`}>{k}</dt>
        <dd class="m-0">
          <code class={BOX}>{isFacts(v) || Array.isArray(v) ? JSON.stringify(v) : String(v)}</code>
        </dd>
      </div>
    ))}
  </dl>
);

/** What one tool's ask shows, picked once from the tool name and the parsed input. */
type AskView =
  | { kind: "plain"; text: string }
  | { kind: "bash"; command: string; why: string | null }
  | { kind: "edit"; path: string | null; before: string; after: string }
  | { kind: "multi"; path: string | null; edits: { old_string: string; new_string: string }[] }
  | { kind: "write"; path: string | null; content: string }
  | { kind: "fetch"; url: string; prompt: string | null }
  | { kind: "search"; query: string }
  | { kind: "keys"; facts: Record<string, unknown> };

/** The view one tool's parsed input asks for; a tool whose facts miss its key falls back to the
 *  generic key rows. */
const askView = (tool: string, input: string, facts: Record<string, unknown> | null): AskView => {
  if (facts === null) return { kind: "plain", text: input };
  const path = str(facts.file_path);
  if (tool === "Bash") {
    const command = str(facts.command);
    if (command !== null) return { kind: "bash", command, why: str(facts.description) };
  } else if (tool === "Edit") {
    const before = str(facts.old_string);
    const after = str(facts.new_string);
    if (before !== null && after !== null) return { kind: "edit", path, before, after };
  } else if (tool === "MultiEdit") {
    const edits = editsOf(facts);
    if (edits !== null) return { kind: "multi", path, edits };
  } else if (tool === "Write") {
    const content = str(facts.content);
    if (content !== null) return { kind: "write", path, content };
  } else if (tool === "WebFetch") {
    const url = str(facts.url);
    if (url !== null) return { kind: "fetch", url, prompt: str(facts.prompt) };
  } else if (tool === "WebSearch") {
    const query = str(facts.query);
    if (query !== null) return { kind: "search", query };
  }
  return { kind: "keys", facts };
};

/** The facts part of one ask: the view's own layout, nothing else. */
const AskBody = ({ view }: { view: AskView }): JSX.Element => {
  switch (view.kind) {
    case "bash":
      return <BashFacts command={view.command} why={view.why} />;
    case "edit":
      return <EditFacts path={view.path} before={view.before} after={view.after} />;
    case "multi":
      return <MultiEditFacts path={view.path} edits={view.edits} />;
    case "write":
      return <WriteFacts path={view.path} content={view.content} />;
    case "fetch":
      return (
        <>
          <code class={BOX}>{view.url}</code>
          {view.prompt === null ? null : (
            <p class={`m-0 whitespace-pre-wrap break-words text-sm ${MUTED}`}>{view.prompt}</p>
          )}
        </>
      );
    case "search":
      return <code class={BOX}>{view.query}</code>;
    case "keys":
      return <KeyRows facts={view.facts} />;
    case "plain":
      return <code class={BOX}>{view.text}</code>;
  }
};

/** What one tool's ask shows, from the input string exactly as the item carried it. */
export function AskPreview({
  tool,
  input,
  truncated,
}: {
  tool: string;
  input: string;
  truncated?: boolean | undefined;
}): JSX.Element {
  const view = askView(tool, input, parseFacts(input));
  return (
    <div class="flex flex-col gap-3">
      <AskBody view={view} />
      {truncated ? <p class={`m-0 text-sm ${MUTED}`}>the input was cut; allow it in the terminal</p> : null}
    </div>
  );
}
