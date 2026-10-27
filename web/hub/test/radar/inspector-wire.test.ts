// inspector-wire.test.ts — the request-body parser the inspector's Input and Output panes read
// through: an Anthropic /v1/messages body into system + turns, and an answer's bytes — SSE stream
// or one plain message — into blocks. Anything else stays unparsed: null means show the raw text.

import { describe, expect, it } from "vite-plus/test";
import { parseInput, parseOutput } from "../../src/radar/app/inspector/wire.ts";

describe("parseInput", () => {
  it("splits a messages body into its system text and its turns with their blocks", () => {
    const body = JSON.stringify({
      model: "claude-sonnet-5-5",
      max_tokens: 1024,
      system: [
        { type: "text", text: "You are terse." },
        { type: "text", text: "Never lie." },
      ],
      messages: [
        { role: "user", content: [{ type: "text", text: "List the files" }] },
        {
          role: "assistant",
          content: [
            { type: "text", text: "Running ls" },
            { type: "tool_use", id: "toolu_1", name: "Bash", input: { command: "ls -la" } },
          ],
        },
        {
          role: "user",
          content: [
            {
              type: "tool_result",
              tool_use_id: "toolu_1",
              content: [{ type: "text", text: "a.go\nb.go\nc.go" }],
            },
          ],
        },
      ],
    });
    const parsed = parseInput(body);
    expect(parsed?.system).toBe("You are terse.\n\nNever lie.");
    expect(parsed?.messages.map((m) => m.role)).toEqual(["user", "assistant", "user"]);
    expect(parsed?.messages[0]?.blocks).toEqual([{ kind: "text", text: "List the files" }]);
    expect(parsed?.messages[1]?.blocks).toEqual([
      { kind: "text", text: "Running ls" },
      { kind: "tool_use", name: "Bash", input: JSON.stringify({ command: "ls -la" }, null, 2) },
    ]);
    expect(parsed?.messages[2]?.blocks).toEqual([
      { kind: "tool_result", ok: true, text: "a.go\nb.go\nc.go", lines: 3 },
    ]);
  });

  it("takes a string system and string contents, and a failed tool result", () => {
    const parsed = parseInput(
      JSON.stringify({
        system: "You are terse.",
        messages: [
          { role: "user", content: "plain words" },
          {
            role: "user",
            content: [{ type: "tool_result", tool_use_id: "t", is_error: true, content: "boom" }],
          },
        ],
      }),
    );
    expect(parsed?.system).toBe("You are terse.");
    expect(parsed?.messages[0]?.blocks).toEqual([{ kind: "text", text: "plain words" }]);
    expect(parsed?.messages[1]?.blocks).toEqual([{ kind: "tool_result", ok: false, text: "boom", lines: 1 }]);
  });

  it("drops the blocks it cannot read and keeps the readable ones around them", () => {
    const parsed = parseInput(
      JSON.stringify({
        system: [{ type: "image" }, "words", { type: "text", text: 7 }],
        messages: [
          {
            role: "user",
            content: [
              "words",
              { type: "text" },
              { type: "tool_use" },
              { type: "tool_use", name: "Bash" },
              { type: "tool_result", content: 7 },
              { type: "tool_result", content: [{ type: "image" }, { type: "text", text: 7 }] },
              { type: "thinking", thinking: "hmm" },
            ],
          },
          { role: "user", content: 7 },
        ],
      }),
    );
    expect(parsed?.system).toBe("");
    expect(parsed?.messages[0]?.blocks).toEqual([
      { kind: "tool_use", name: "Bash", input: "{}" },
      { kind: "tool_result", ok: true, text: "", lines: 1 },
      { kind: "tool_result", ok: true, text: "\n", lines: 2 },
    ]);
    expect(parsed?.messages[1]?.blocks).toEqual([]);
  });

  it("stops reading a body whose message list holds something that is not a message", () => {
    expect(parseInput(JSON.stringify({ messages: [7] }))).toBeNull();
  });

  it("answers null for anything that is not an Anthropic messages body", () => {
    expect(parseInput("not json at all")).toBeNull();
    expect(parseInput("")).toBeNull();
    expect(parseInput("[1, 2, 3]")).toBeNull();
    expect(parseInput(JSON.stringify({ model: "glm-5.3" }))).toBeNull();
    expect(parseInput(JSON.stringify({ messages: "nope" }))).toBeNull();
    // an OpenAI chat body carries a system role; it shows as raw JSON instead
    expect(
      parseInput(JSON.stringify({ model: "gpt-5", messages: [{ role: "system", content: "be brief" }] })),
    ).toBeNull();
  });
});

describe("parseOutput", () => {
  it("reads an SSE stream into text and tool blocks, with the final token count", () => {
    const stream = [
      "event: message_start",
      'data: {"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":10,"output_tokens":1}}}',
      "",
      "event: content_block_start",
      'data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}',
      "",
      "event: content_block_delta",
      'data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}',
      "",
      "event: content_block_delta",
      'data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}',
      "",
      "event: content_block_start",
      'data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_9","name":"Bash","input":{}}}',
      "",
      "event: content_block_delta",
      'data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\\"command\\":"}}',
      "",
      "event: content_block_delta",
      'data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\\"ls -la\\"}"}}',
      "",
      "event: content_block_stop",
      'data: {"type":"content_block_stop","index":1}',
      "",
      "event: message_delta",
      'data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":41}}',
      "",
      "event: message_stop",
      'data: {"type":"message_stop"}',
      "",
    ].join("\n");
    const parsed = parseOutput(stream);
    expect(parsed?.streamed).toBe(true);
    expect(parsed?.outputTokens).toBe(41);
    expect(parsed?.blocks).toEqual([
      { kind: "text", text: "Hello world" },
      { kind: "tool_use", name: "Bash", input: JSON.stringify({ command: "ls -la" }, null, 2) },
    ]);
  });

  it("reads one plain JSON message, tokens included", () => {
    const parsed = parseOutput(
      JSON.stringify({
        role: "assistant",
        content: [{ type: "text", text: "Done." }],
        stop_reason: "end_turn",
        usage: { input_tokens: 5, output_tokens: 7 },
      }),
    );
    expect(parsed?.streamed).toBe(false);
    expect(parsed?.outputTokens).toBe(7);
    expect(parsed?.blocks).toEqual([{ kind: "text", text: "Done." }]);
  });

  it("keeps reading a stream whose events are bent: missing indexes, unnamed tools, junk data lines", () => {
    const stream = [
      "event: message_start",
      'data: {"type":"message_start","message":{"usage":{"output_tokens":"7"}}}',
      "",
      "event: content_block_start",
      'data: {"type":"content_block_start","content_block":{"type":"text"}}',
      "",
      "event: content_block_delta",
      'data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lost"}}',
      "",
      "event: content_block_delta",
      'data: {"type":"content_block_delta","index":-1,"delta":{"type":"text_delta"}}',
      "",
      "event: content_block_start",
      'data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use"}}',
      "",
      "event: content_block_delta",
      'data: {"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{"}}',
      "",
      "event: content_block_start",
      'data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","name":"Bash"}}',
      "",
      "event: content_block_delta",
      'data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta"}}',
      "",
      "event: message_delta",
      "data: {}",
      "",
      "event: ping",
      "data: not json",
      "",
    ].join("\n");
    const parsed = parseOutput(stream);
    expect(parsed?.streamed).toBe(true);
    expect(parsed?.outputTokens).toBeNull();
    expect(parsed?.blocks).toEqual([{ kind: "tool_use", name: "Bash", input: "" }]);
  });

  it("answers null for a plain answer that is not one message, and reads one without tokens", () => {
    expect(parseOutput("[]")).toBeNull();
    const bare = parseOutput(JSON.stringify({ content: [{ type: "text", text: "Done." }] }));
    expect(bare?.streamed).toBe(false);
    expect(bare?.outputTokens).toBeNull();
    expect(bare?.blocks).toEqual([{ kind: "text", text: "Done." }]);
    const untallied = parseOutput(JSON.stringify({ usage: { output_tokens: "7" }, content: 7 }));
    expect(untallied?.outputTokens).toBeNull();
    expect(untallied?.blocks).toEqual([]);
  });
});
