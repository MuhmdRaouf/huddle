// TranscriptPane.tsx — the session page's transcript: a prompt and a reply as text, each stretch
// of tool calls as one folded row that opens to its calls. The page opens at its end once and
// never scrolls by itself afterwards: items that arrive later sit under a New pill, a tap on the
// pill goes to the first of them, and reading past it clears the pill. Every row keys by its
// conversation item id. Session text is rendered as text, never HTML.
import { Button } from "@huddle/ui/button.tsx";
import { type JSX } from "preact";
import { useLayoutEffect, useRef, useState } from "preact/hooks";
import { Icon, type IconName } from "../icons.tsx";
import { ageText } from "../registry.ts";
import { newSince } from "./conversation.ts";
import { fold, type FoldBlock, type FoldCall, type FoldRun } from "./transcript.ts";
import type { ConvoItem } from "./types.ts";

const MUTED = "text-[var(--ui-muted)]";

/** The reading room the first new item keeps above it when a tap on the pill goes to it, in px. */
const NEW_ROOM = 8;

/** A run row's noun for each kind of tool: the singular and the plural the count takes. */
type Noun = { one: string; many: string };

const READS: Noun = { one: "read or search", many: "reads and searches" };
const COMMANDS: Noun = { one: "command", many: "commands" };
const EDITS: Noun = { one: "edit", many: "edits" };

/** The noun a tool's calls count under; a tool the row has no word for counts under its name. */
function nounOf(tool: string): Noun {
  switch (tool) {
    case "Read":
    case "Grep":
    case "Glob":
    case "LS":
    case "WebSearch":
    case "WebFetch":
      return READS;
    case "Bash":
      return COMMANDS;
    case "Edit":
    case "MultiEdit":
    case "Write":
    case "NotebookEdit":
      return EDITS;
    default:
      return { one: tool || "tool call", many: tool || "tool calls" };
  }
}

/** A run row's words: "Ran 7 reads and searches", "Running 2 commands, 1 edit". */
function runLabel(run: FoldRun): string {
  const counts = new Map<Noun, number>();
  for (const c of run.counts) {
    const noun = nounOf(c.tool);
    counts.set(noun, (counts.get(noun) ?? 0) + c.count);
  }
  const parts = [...counts].map(([noun, n]) => `${n} ${n === 1 ? noun.one : noun.many}`);
  return `${run.running ? "Running" : "Ran"} ${parts.join(", ")}`;
}

/** A run's mark: failed when a call failed, running while a call awaits its answer, else done. */
type Tone = "ok" | "info" | "fail";

function toneOf(run: FoldRun): Tone {
  if (run.calls.some((c) => c.error)) return "fail";
  return run.running ? "info" : "ok";
}

const TONE_TILE: Record<Tone, { icon: IconName; tile: string }> = {
  ok: { icon: "check", tile: "bg-[var(--ui-ok-soft)] text-[var(--ui-ok-ink)]" },
  info: { icon: "clock", tile: "bg-[var(--ui-info-soft)] text-[var(--ui-info-ink)]" },
  fail: { icon: "x", tile: "bg-[var(--ui-fail-soft)] text-[var(--ui-fail-ink)]" },
};

/** The 24 px tile at a row's left. */
function Tile({ tone }: { tone: Tone }): JSX.Element {
  const { icon, tile } = TONE_TILE[tone];
  return (
    <span class={`flex size-6 shrink-0 items-center justify-center rounded-lg ${tile}`}>
      <Icon name={icon} class="size-3.5" />
    </span>
  );
}

/** One call a run opens to: its mark, its tool and its target in the mono face. */
function CallLine({ call }: { call: FoldCall }): JSX.Element {
  const tone: Tone = call.error ? "fail" : call.running ? "info" : "ok";
  return (
    <div
      data-call=""
      {...(call.running ? { "data-running": "" } : {})}
      {...(call.error ? { "data-error": "" } : {})}
      class="flex min-h-9 items-center gap-2.5 pr-2 pl-3"
    >
      <Tile tone={tone} />
      <span class="shrink-0 text-sm font-semibold">{call.tool}</span>
      <code class={`ui-mono min-w-0 flex-1 truncate text-[0.78rem] ${MUTED}`}>{call.target}</code>
    </div>
  );
}

/** One run as its row: a button that opens to the run's calls. */
function RunRow({ run, now }: { run: FoldRun; now: number }): JSX.Element {
  const [open, setOpen] = useState(false);
  const age = ageText(run.at, now);
  return (
    <div
      data-item={run.id}
      data-run=""
      {...(run.running ? { "data-running": "" } : {})}
      class="shrink-0 overflow-hidden rounded-[var(--ui-radius)] bg-[var(--ui-surface)] shadow-[var(--ui-shadow-1)]"
    >
      <Button
        variant="ghost"
        expanded={open}
        onClick={() => setOpen(!open)}
        class="!h-auto !min-h-11 w-full !justify-start !gap-2.5 !rounded-none !px-3 !py-2 !text-left !text-[var(--ui-text)]"
      >
        <Tile tone={toneOf(run)} />
        <span class="min-w-0 text-sm font-semibold">{runLabel(run)}</span>
        {run.last ? (
          <code class={`ui-mono min-w-0 flex-1 truncate text-[0.78rem] ${MUTED}`}>last: {run.last}</code>
        ) : (
          <span class="flex-1" />
        )}
        {age ? <span class={`shrink-0 text-xs ${MUTED}`}>{age}</span> : null}
        <Icon name={open ? "up" : "down"} class={`size-3.5 ${MUTED}`} />
      </Button>
      {open ? (
        <div data-run-calls="" class="flex flex-col pb-1.5">
          {run.calls.map((call) => (
            <CallLine key={call.id} call={call} />
          ))}
        </div>
      ) : null}
    </div>
  );
}

/** One block as its row: a prompt as the owner's bubble, a reply as plain text, a run folded. */
function Row({ block, now }: { block: FoldBlock; now: number }): JSX.Element {
  if (block.kind === "run") return <RunRow run={block} now={now} />;
  if (block.kind === "prompt") {
    return (
      <div data-item={block.id} class="flex shrink-0 justify-end">
        <div class="max-w-[82%] rounded-[1.25rem_1.25rem_0.375rem_1.25rem] bg-[var(--ui-accent)] px-3.5 py-2.5 break-words whitespace-pre-wrap text-[var(--ui-on-accent)]">
          {block.text}
        </div>
      </div>
    );
  }
  return (
    <div data-item={block.id} class="shrink-0 px-1 break-words whitespace-pre-wrap">
      {block.text}
    </div>
  );
}

/** The New divider: a line, the pill that goes to the first new item, a line. The pill is a
 *  full tap target tall and keeps the divider's own height with negative margins. */
function NewDivider({ onGo }: { onGo: () => void }): JSX.Element {
  return (
    <div data-new="" class="flex shrink-0 items-center gap-2">
      <span class="h-px flex-1 bg-[var(--ui-line)]" />
      <Button
        variant="ghost"
        onClick={onGo}
        class="!min-h-[var(--ui-tap)] !px-1 -my-[0.6875rem] !rounded-full"
      >
        <span class="flex h-[1.375rem] items-center rounded-full bg-[var(--ui-accent-soft)] px-2.5 text-xs font-semibold text-[var(--ui-accent)]">
          New
        </span>
      </Button>
      <span class="h-px flex-1 bg-[var(--ui-line)]" />
    </div>
  );
}

/** The transcript of a session's items. It opens at the end once, and keeps the reading place
 *  after that: the page never scrolls by itself. */
export function TranscriptPane({ items, now }: { items: ConvoItem[]; now: number }): JSX.Element {
  const pane = useRef<HTMLDivElement>(null);
  // the last item the reader had at the first paint, or at the last time they read past the pill
  const seen = useRef<string | null>(null);
  const [, repaint] = useState(0);
  const last = items[items.length - 1];
  if (seen.current === null && last) seen.current = last.id;

  const blocks = fold(items);
  const fresh = new Set(newSince({ items, next: 0 }, seen.current).map((i) => i.id));
  const firstNew = blocks.findIndex((b) => fresh.has(b.id));

  const opened = items.length > 0;
  useLayoutEffect(() => {
    const el = pane.current;
    if (el && opened) el.scrollTop = el.scrollHeight;
  }, [opened]);

  const firstNewEl = (): HTMLElement | null => {
    const id = firstNew >= 0 ? blocks[firstNew]?.id : undefined;
    const rows = pane.current?.querySelectorAll<HTMLElement>("[data-item]") ?? [];
    return Array.from(rows).find((r) => r.dataset.item === id) ?? null;
  };
  const goToNew = (): void => {
    const el = pane.current;
    const target = firstNewEl();
    if (!el || !target) return;
    el.scrollTop += target.getBoundingClientRect().top - el.getBoundingClientRect().top - NEW_ROOM;
  };
  // reading past the first new item — its bottom edge inside the window — clears the pill
  const onScroll = (): void => {
    const el = pane.current;
    const target = firstNewEl();
    if (!el || !target || !last) return;
    if (target.getBoundingClientRect().bottom <= el.getBoundingClientRect().bottom) {
      seen.current = last.id;
      repaint((n) => n + 1);
    }
  };

  return (
    <div
      ref={pane}
      onScroll={onScroll}
      data-block="transcript"
      aria-label="Transcript"
      tabIndex={0}
      class="min-h-0 flex-1 overflow-y-auto px-4 pt-2 pb-3.5 tablet:px-0"
    >
      <div class="mx-auto flex max-w-[51.25rem] flex-col gap-2 text-[0.9375rem] leading-[1.4]">
        {blocks.map((block, i) => (
          <>
            {i === firstNew ? <NewDivider key="new" onGo={goToNew} /> : null}
            <Row key={block.id} block={block} now={now} />
          </>
        ))}
        {blocks.length === 0 ? <p class={`py-6 text-center text-sm ${MUTED}`}>Nothing said yet.</p> : null}
      </div>
    </div>
  );
}
