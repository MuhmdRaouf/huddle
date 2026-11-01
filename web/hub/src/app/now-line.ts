// now-line.ts — the one Now line a session paints: what it is doing or waits on, read from its
// registry row, its open needs-you item and its turn's facts. The list's rows and the Session
// screen's Now card show it; it decides nothing else.

import { ageText, type NeedItem, type RegistrySession } from "../registry.ts";
import type { TurnFacts } from "../session/types.ts";

/** A registry row as the Now line reads it, with the listening flag a row carries while an
 *  idle-wake waiter is armed for it. */
export type NowLineRow = RegistrySession & { listening?: boolean };

/** The Now line's text: an ended session is over even when an old ask of its is still open; a
 *  live session's open ask is its now; a listening session names its channel; a stuck one names
 *  its silence; a working or idle one names its work, corrected by the turn's facts (the
 *  transcript outranks the row's last report). A state the line knows nothing about says
 *  nothing. */
export function nowLine(
  row: NowLineRow,
  item?: NeedItem,
  turn?: TurnFacts,
  now: number = Date.now(),
): string {
  if (row.state === "ended") return "Session ended";
  if (item) return askLine(item);
  if (row.listening) return row.channel ? `Listening on #${row.channel}` : "Listening";
  if (row.state === "stuck") return stuckLine(row, now);
  if (row.state === "working") return turn?.ended ? "Finished, waiting for you" : workLine(row);
  if (row.state === "idle") return turn && !turn.ended ? "Working" : "Finished, waiting for you";
  if (row.state === "needs-you") return "Waiting for you";
  return "";
}

/** The line an open ask paints: a permission names the command it waits to run, or the tool it
 *  waits to use; a finished turn waits for the owner; a question waits for the answer; a
 *  blocked task waits on the owner. */
function askLine(item: NeedItem): string {
  if (item.kind === "permission") {
    const tool = item.tool ?? "";
    const command = tool === "Bash" ? bashCommand(item) : "";
    if (command) return `Waiting for you to allow a command: ${command}`;
    if (tool) return `Waiting for you to allow ${tool}`;
    return "Waiting for you";
  }
  if (item.kind === "turn") return "Finished, waiting for you";
  if (item.kind === "question") {
    return item.summary ? `Waiting for your answer: ${item.summary}` : "Waiting for your answer";
  }
  if (item.kind === "blocked") return item.summary ? `Waiting on you: ${item.summary}` : "Waiting on you";
  return item.summary ? `Waiting for you: ${item.summary}` : "Waiting for you";
}

/** A Bash ask's command as the terminal runs it: the input's `command` member when the hook sent
 *  the call's JSON, else the input itself (an older hook's plain text). */
function bashCommand(item: NeedItem): string {
  const raw = item.input ?? "";
  try {
    const command = (JSON.parse(raw) as { command?: unknown } | null)?.command;
    if (typeof command === "string" && command.trim() !== "") return command;
  } catch {
    // a plain or cut input is not JSON and shows as it came
  }
  return raw.trim();
}

/** The line a working session paints: the command it runs, the file it edits, the tool and
 *  target no verb fits, or the bare tool. */
function workLine(row: NowLineRow): string {
  const tool = row.tool ?? "";
  const target = row.tool_target ?? "";
  if (!tool) return "Working";
  if (tool === "Bash") return target ? `Running ${target}` : "Running Bash";
  if (tool === "Edit" || tool === "MultiEdit" || tool === "Write") {
    return target ? `Editing ${target}` : "Editing";
  }
  return target ? `Running ${tool} ${target}` : `Running ${tool}`;
}

/** The line a stuck session paints: how long it has been silent, in the one age style, or the
 *  bare silence when its last report carries no time. */
function stuckLine(row: NowLineRow, now: number): string {
  const age = ageText(row.last_event_at, now);
  return age ? `No signal for ${age}` : "No signal";
}
