// wire.ts — the request bodies as they crossed the wire, parsed for the inspector's Input and
// Output panes: an Anthropic /v1/messages ask into its system text and its turns, an answer's
// bytes — an SSE stream or one plain message — into text and tool blocks. Anything else is not
// parsed: null means the pane shows the raw text.

/** One piece of a body: words, a tool call with its arguments, or a tool's answer. */
export type BodyBlock =
  | { kind: "text"; text: string }
  | { kind: "tool_use"; name: string; input: string }
  | { kind: "tool_result"; ok: boolean; text: string; lines: number };

/** A parsed request body: the system text and each turn with its blocks. */
export type ParsedInput = { system: string; messages: { role: string; blocks: BodyBlock[] }[] };

/** A parsed answer: its blocks, the tokens it reported, and whether it arrived as a stream. */
export type ParsedOutput = { blocks: BodyBlock[]; outputTokens: number | null; streamed: boolean };

const asRecord = (value: unknown): Record<string, unknown> | null =>
  typeof value === "object" && value !== null && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;

const asText = (value: unknown): string | null => (typeof value === "string" ? value : null);

/** One content block as Anthropic carries it, turned into a pane block; unknown kinds drop out. */
function blockOf(raw: unknown): BodyBlock | null {
  const block = asRecord(raw);
  if (block === null) return null;
  if (block.type === "text") {
    const text = asText(block.text);
    return text === null ? null : { kind: "text", text };
  }
  if (block.type === "tool_use") {
    const name = asText(block.name);
    if (name === null) return null;
    return { kind: "tool_use", name, input: JSON.stringify(block.input ?? {}, null, 2) };
  }
  if (block.type === "tool_result") {
    const content = block.content;
    let text: string;
    if (typeof content === "string") text = content;
    else if (Array.isArray(content)) {
      text = content
        .map((part) => asRecord(part))
        .map((part) => (part?.type === "text" ? (asText(part.text) ?? "") : ""))
        .join("\n");
    } else text = "";
    return { kind: "tool_result", ok: block.is_error !== true, text, lines: text.split("\n").length };
  }
  return null;
}

const blocksOf = (content: unknown): BodyBlock[] => {
  if (typeof content === "string") return [{ kind: "text", text: content }];
  if (!Array.isArray(content)) return [];
  return content.map(blockOf).filter((block): block is BodyBlock => block !== null);
};

/** Reads an Anthropic /v1/messages body; an OpenAI chat body (a system role) is not parsed. */
export function parseInput(text: string): ParsedInput | null {
  let body: unknown;
  try {
    body = JSON.parse(text);
  } catch {
    return null;
  }
  const record = asRecord(body);
  if (record === null || !Array.isArray(record.messages)) return null;
  const system = asText(record.system);
  const parts =
    system !== null
      ? [system]
      : Array.isArray(record.system)
        ? record.system
            .map((part) => asRecord(part))
            .map((part) => (part?.type === "text" ? asText(part.text) : null))
            .filter((words): words is string => words !== null)
        : [];
  const messages: ParsedInput["messages"] = [];
  for (const raw of record.messages) {
    const message = asRecord(raw);
    if (message === null) return null;
    const role = asText(message.role);
    if (role !== "user" && role !== "assistant") return null;
    messages.push({ role, blocks: blocksOf(message.content) });
  }
  return { system: parts.join("\n\n"), messages };
}

/** One SSE event as it flies past: its name and its parsed data line. */
type StreamEvent = { name: string; data: Record<string, unknown> | null };

const streamEvents = (text: string): StreamEvent[] => {
  const events: StreamEvent[] = [];
  let name = "";
  let data = "";
  const flush = (): void => {
    if (name === "" && data === "") return;
    let parsed: unknown = null;
    try {
      parsed = JSON.parse(data);
    } catch {
      parsed = null;
    }
    events.push({ name, data: asRecord(parsed) });
    name = "";
    data = "";
  };
  for (const line of text.split("\n")) {
    if (line.startsWith("event:")) name = line.slice("event:".length).trim();
    else if (line.startsWith("data:")) data += line.slice("data:".length).trim();
    else if (line === "") flush();
  }
  flush();
  return events;
};

/** Reads an answer's bytes: an Anthropic SSE stream or one plain JSON message. */
export function parseOutput(text: string): ParsedOutput | null {
  if (text === "") return null;
  const lines = text.split("\n").filter((line) => line !== "");
  const streamed =
    lines.length > 0 && lines.every((line) => line.startsWith("event:") || line.startsWith("data:"));
  if (streamed) return readStream(text);
  return readMessage(text);
}

/** What one stream index holds while its deltas are still flying: words, or a tool call's JSON. */
type OpenBlock = { kind: "text"; text: string } | { kind: "tool"; name: string; json: string };

/** The output token count one usage event reports, or null when it carries none. */
function outputTokensOf(data: Record<string, unknown>): number | null {
  const usage = asRecord(data.usage) ?? asRecord(asRecord(data.message)?.usage);
  const count = usage === null ? null : usage.output_tokens;
  return typeof count === "number" ? count : null;
}

/** Opens one stream index: the block the event says started, words or a tool call. */
function openStreamBlock(data: Record<string, unknown>, open: Map<number, OpenBlock>): void {
  const block = asRecord(data.content_block);
  const index = typeof data.index === "number" ? data.index : -1;
  if (block?.type === "text") open.set(index, { kind: "text", text: asText(block.text) ?? "" });
  if (block?.type === "tool_use" && typeof block.name === "string") {
    open.set(index, { kind: "tool", name: block.name, json: "" });
  }
}

/** Fills one stream index from its delta: words appended, the tool call's JSON spelled on. */
function feedStreamDelta(data: Record<string, unknown>, open: Map<number, OpenBlock>): void {
  const delta = asRecord(data.delta);
  const index = typeof data.index === "number" ? data.index : -1;
  const held = open.get(index);
  if (delta?.type === "text_delta" && held?.kind === "text") held.text += asText(delta.text) ?? "";
  if (delta?.type === "input_json_delta" && held?.kind === "tool")
    held.json += asText(delta.partial_json) ?? "";
}

/** Folds one block event into the map: a block's start opens it, its deltas fill it. */
function foldStreamEvent(event: StreamEvent, open: Map<number, OpenBlock>): void {
  const data = event.data;
  if (data === null) return;
  if (event.name === "content_block_start") openStreamBlock(data, open);
  if (event.name === "content_block_delta") feedStreamDelta(data, open);
}

/** The stream's blocks in index order: finished words kept, a tool call's JSON re-spelled pretty. */
function foldedBlocks(open: Map<number, OpenBlock>): BodyBlock[] {
  const blocks: BodyBlock[] = [];
  for (const held of [...open.entries()].sort((a, b) => a[0] - b[0]).map((entry) => entry[1])) {
    if (held.kind === "text") {
      if (held.text !== "") blocks.push({ kind: "text", text: held.text });
      continue;
    }
    let input = held.json;
    try {
      input = JSON.stringify(JSON.parse(held.json), null, 2);
    } catch {
      // the deltas never spelled valid JSON: show the raw text
    }
    blocks.push({ kind: "tool_use", name: held.name, input });
  }
  return blocks;
}

/** The SSE walk: the events fold into blocks, the usage events hand over the token count. */
function readStream(text: string): ParsedOutput {
  let tokens: number | null = null;
  const open = new Map<number, OpenBlock>();
  for (const event of streamEvents(text)) {
    if (event.data === null) continue;
    if (event.name === "message_start" || event.name === "message_delta") {
      const count = outputTokensOf(event.data);
      if (count !== null) tokens = count;
    }
    foldStreamEvent(event, open);
  }
  return { blocks: foldedBlocks(open), outputTokens: tokens, streamed: true };
}

/** One plain JSON answer message: its blocks and its reported output tokens. */
function readMessage(text: string): ParsedOutput | null {
  let body: unknown;
  try {
    body = JSON.parse(text);
  } catch {
    return null;
  }
  const record = asRecord(body);
  if (record === null) return null;
  const usage = asRecord(record.usage);
  const tokens = usage === null || typeof usage.output_tokens !== "number" ? null : usage.output_tokens;
  return { blocks: blocksOf(record.content), outputTokens: tokens, streamed: false };
}
