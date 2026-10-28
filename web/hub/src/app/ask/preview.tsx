// preview.tsx — what one tool's ask shows before the owner allows it: the Bash command with its
// why, an Edit or MultiEdit as a line diff with a fold, the Write's head, the fetch's URL and the
// search's query, and any other tool's input as one row per key. Every box wraps; nothing scrolls
// sideways. A caller may fold the text blocks (the list's card shows four lines and a "+N lines"
// count where the sheet shows it all).

import { Button } from "@huddle/ui/button.tsx";
import type { JSX } from "preact";
import { useState } from "preact/hooks";
import { lineDiff, type DiffLine } from "./diff.ts";

/** The code box every preview fact sits in: mono, wrapping, on the raised surface. */
const BOX =
  "ui-mono block whitespace-pre-wrap break-words rounded-[var(--ui-radius)] bg-[var(--ui-surface-2)] p-3 text-[length:var(--ui-text-mono)] leading-[1.5]";

/** A section's heading: the mock's 13 px / 600 quiet label, inset 4 px. */
export const SECTION_LABEL = "m-0 ms-1 text-sm font-semibold text-[var(--ui-muted)]";

/** The quiet ink of a hint or a label. */
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
  <p class="m-0 flex min-h-[var(--ui-tap)] items-center gap-2 rounded-[var(--ui-radius)] bg-[var(--ui-surface-2)] px-3">
    {path === null ? null : <span class="ui-mono min-w-0 truncate text-sm">{path}</span>}
    {added === null || removed === null ? null : (
      <span class="shrink-0 text-sm font-semibold">
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
      {line.sign === "-" ? "−" : line.sign === "+" ? "+" : " "}
    </span>
    <span class="min-w-0 whitespace-pre-wrap break-words text-[length:var(--ui-text-mono)] leading-[1.5]">
      {line.text === "" ? " " : line.text}
    </span>
  </div>
);

/** The line diff on its raised surface, folded at twelve rows until the owner opens the rest in
 *  place — or at the caller's fold, as the list's card does. */
const DiffBlock = ({ rows, fold }: { rows: DiffLine[]; fold?: number | undefined }): JSX.Element => {
  const cap = fold ?? FOLD;
  const [open, setOpen] = useState(false);
  const shown = open ? rows : rows.slice(0, cap);
  const hidden = rows.length - cap;
  return (
    <div data-diff class="flex flex-col rounded-[var(--ui-radius)] bg-[var(--ui-surface-2)] py-2">
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

/** The head of a text block at the caller's fold, with the count of the lines that wait. */
const foldLines = (text: string, fold: number | undefined): { head: string; more: number } => {
  if (fold === undefined) return { head: text, more: 0 };
  const lines = text.split("\n");
  return { head: lines.slice(0, fold).join("\n"), more: Math.max(0, lines.length - fold) };
};

/** A text block in the code box, folded at the caller's cap with the count of the hidden lines
 *  tight under it. */
const Box = ({ text, fold }: { text: string; fold?: number | undefined }): JSX.Element => {
  const { head, more } = foldLines(text, fold);
  return (
    <div class="flex flex-col">
      <code class={BOX}>{head}</code>
      {more > 0 ? <p class="m-0 text-sm font-semibold text-[var(--ui-accent)]">+{more} lines</p> : null}
    </div>
  );
};

/** The Bash ask: the Command label with its Copy, the command to copy, then the why the caller
 *  gave. */
const BashFacts = ({
  command,
  why,
  fold,
}: {
  command: string;
  why: string | null;
  fold?: number | undefined;
}): JSX.Element => (
  <>
    <section class="flex flex-col">
      <div class="mb-2 flex items-center justify-between gap-2">
        <h3 class={SECTION_LABEL}>Command</h3>
        <Button variant="ghost" onClick={() => void navigator.clipboard?.writeText(command)}>
          Copy
        </Button>
      </div>
      <Box text={command} fold={fold} />
    </section>
    {why === null ? null : (
      <section class="flex flex-col">
        <h3 class={`${SECTION_LABEL} mb-2`}>Why</h3>
        <p class={`m-0 whitespace-pre-wrap break-words text-sm ${MUTED}`}>{why}</p>
      </section>
    )}
  </>
);

/** The Edit ask: the file row with its counts, then the change as a line diff. */
const EditFacts = ({
  path,
  before,
  after,
  fold,
}: {
  path: string | null;
  before: string;
  after: string;
  fold?: number | undefined;
}): JSX.Element => {
  const rows = lineDiff(before, after);
  return (
    <>
      <FilePath path={path} added={count(rows, "+")} removed={count(rows, "-")} />
      <DiffBlock rows={rows} fold={fold} />
    </>
  );
};

/** The MultiEdit ask: the file row with the totals, then one counted diff per edit. */
const MultiEditFacts = ({
  path,
  edits,
  fold,
}: {
  path: string | null;
  edits: { old_string: string; new_string: string }[];
  fold?: number | undefined;
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
          <DiffBlock rows={rows} fold={fold} />
        </div>
      ))}
    </>
  );
};

/** The Write ask: the file row, then the head of the content. */
const WriteFacts = ({
  path,
  content,
  fold,
}: {
  path: string | null;
  content: string;
  fold?: number | undefined;
}): JSX.Element => (
  <>
    <FilePath path={path} added={null} removed={null} />
    <Box text={content} fold={fold ?? FOLD} />
  </>
);

/** Any other tool: one row per top-level key, objects and arrays as compact JSON. */
const KeyRows = ({
  facts,
  fold,
}: {
  facts: Record<string, unknown>;
  fold?: number | undefined;
}): JSX.Element => (
  <dl class="m-0 flex flex-col gap-2">
    {Object.entries(facts).map(([k, v]) => (
      <div key={k} class="flex flex-col gap-0.5">
        <dt class={`text-xs ${MUTED}`}>{k}</dt>
        <dd class="m-0">
          <Box text={isFacts(v) || Array.isArray(v) ? JSON.stringify(v) : String(v)} fold={fold} />
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
const AskBody = ({ view, fold }: { view: AskView; fold?: number | undefined }): JSX.Element => {
  switch (view.kind) {
    case "bash":
      return <BashFacts command={view.command} why={view.why} fold={fold} />;
    case "edit":
      return <EditFacts path={view.path} before={view.before} after={view.after} fold={fold} />;
    case "multi":
      return <MultiEditFacts path={view.path} edits={view.edits} fold={fold} />;
    case "write":
      return <WriteFacts path={view.path} content={view.content} fold={fold} />;
    case "fetch":
      return (
        <>
          <Box text={view.url} fold={fold} />
          {view.prompt === null ? null : (
            <p class={`m-0 whitespace-pre-wrap break-words text-sm ${MUTED}`}>{view.prompt}</p>
          )}
        </>
      );
    case "search":
      return <Box text={view.query} fold={fold} />;
    case "keys":
      return <KeyRows facts={view.facts} fold={fold} />;
    case "plain":
      return <Box text={view.text} fold={fold} />;
  }
};

/** What one tool's ask shows, from the input string exactly as the item carried it. `fold` caps
 *  the text blocks at that many lines with a "+N lines" count; the sheet leaves it out and shows
 *  it all. */
export function AskPreview({
  tool,
  input,
  truncated,
  fold,
}: {
  tool: string;
  input: string;
  truncated?: boolean | undefined;
  fold?: number | undefined;
}): JSX.Element {
  const view = askView(tool, input, parseFacts(input));
  return (
    <div class="flex flex-col gap-4" data-ask-preview="">
      <AskBody view={view} fold={fold} />
      {truncated ? <p class={`m-0 text-sm ${MUTED}`}>the input was cut; allow it in the terminal</p> : null}
    </div>
  );
}
