// transcript.ts — the fold of a session's transcript into the blocks its screen paints: a
// prompt, a reply, or one run of tool calls. The screen paints text and tool runs; a thinking
// line, an attachment and a textless line paint nothing. A run is every tool call between two
// text blocks, with its calls counted by tool, the last call's target, and whether it still
// runs — a call whose answer has not arrived keeps the run open. Every block and every call
// keys by its conversation item id, so a view never keys by index.

import type { ConvoItem } from "./types.ts";

/** One text block: the owner's prompt or the assistant's reply, as its line wrote it. */
export type FoldText = {
  kind: "prompt" | "reply";
  id: string;
  at: string;
  text: string;
};

/** One call of a run, as its row paints it: the target names what the call acted on. */
export type FoldCall = {
  id: string;
  tool: string;
  target: string;
  running: boolean;
  error: boolean;
};

/** One tool count of a run: the tool's name and how often the run called it. */
export type FoldCount = { tool: string; count: number };

/** One run: the calls between two text blocks, folded into one row. */
export type FoldRun = {
  kind: "run";
  id: string;
  at: string;
  counts: FoldCount[];
  last: string;
  running: boolean;
  calls: FoldCall[];
};

/** One block the screen paints. */
export type FoldBlock = FoldText | FoldRun;

/** The transcript's blocks in order: prompts and replies as they came, and every stretch of
 *  tool calls between them as one run. A textless user line, a thinking line, an attachment and
 *  a tool answer whose call a page boundary left behind paint nothing. */
export function fold(items: ConvoItem[]): FoldBlock[] {
  const blocks: FoldBlock[] = [];
  let run: FoldRun | undefined;
  let open = 0; // the run's calls still awaiting their answer
  const awaiting = new Map<string, FoldCall>(); // tool_use_id → the call still awaiting its answer

  const closeRun = () => {
    if (!run) return;
    run.running = open > 0;
    run = undefined;
    open = 0;
    awaiting.clear();
  };

  for (const item of items) {
    if (item.kind === "tool_use") {
      if (!run) {
        run = { kind: "run", id: item.id, at: item.at, counts: [], last: "", running: false, calls: [] };
        blocks.push(run);
      }
      const target = targetOf(item.input);
      const call: FoldCall = {
        id: item.id,
        tool: item.tool ?? "",
        target,
        running: true,
        error: false,
      };
      run.calls.push(call);
      count(run.counts, call.tool);
      run.last = target;
      open += 1;
      awaiting.set(item.tool_use_id ?? "", call);
      continue;
    }
    if (item.kind === "tool_result") {
      const id = item.tool_use_id ?? "";
      const call = awaiting.get(id);
      if (call) {
        call.running = false;
        call.error = item.is_error === true;
        open -= 1;
        awaiting.delete(id);
      }
      continue;
    }
    if ((item.kind === "user" || item.kind === "assistant") && item.text) {
      closeRun();
      blocks.push({
        kind: item.kind === "user" ? "prompt" : "reply",
        id: item.id,
        at: item.at,
        text: item.text,
      });
    }
  }
  closeRun();
  return blocks;
}

/** A call's target as the run row names it: the command the input runs, else the file it names,
 *  else nothing — a cut input is no longer JSON and names no target. */
function targetOf(input: string | undefined): string {
  if (!input) return "";
  let parsed: unknown;
  try {
    parsed = JSON.parse(input);
  } catch {
    return ""; // a cut input is no longer JSON and names no target
  }
  if (typeof parsed !== "object" || parsed === null) return "";
  const command = "command" in parsed ? parsed.command : undefined;
  if (typeof command === "string" && command.trim() !== "") return command;
  const file = "file_path" in parsed ? parsed.file_path : undefined;
  if (typeof file === "string" && file.trim() !== "") return file;
  return "";
}

/** Counts one more call of a tool, in first-appearance order. */
function count(counts: FoldCount[], tool: string): void {
  const seen = counts.find((c) => c.tool === tool);
  if (seen) seen.count += 1;
  else counts.push({ tool, count: 1 });
}
