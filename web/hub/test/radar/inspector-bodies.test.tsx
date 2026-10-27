// inspector-bodies.test.tsx — the inspector's Input and Output panes when the request carried bodies:
// the kept panes fold each tool call and result into a row that opens, and the words for a body the
// store cannot give back (missing, off, owner-only, broken) come from the mock.

import { screen } from "@testing-library/preact";
import { userEvent } from "@testing-library/user-event";
import { describe, expect, it } from "vite-plus/test";
import { Bodies } from "../../src/radar/app/inspector/Bodies.tsx";
import { Inspector } from "../../src/radar/app/inspector/Inspector.tsx";
import { OverviewTab } from "../../src/radar/app/inspector/Overview.tsx";
import type { BodiesState } from "../../src/radar/state.ts";
import { makeRequest } from "./helpers.ts";
import { renderApp } from "./ui/render.tsx";

const NOW = 1_790_000_000_000;
const REQUEST = makeRequest({ id: "a", model: "glm-5.3", ts: NOW - 5_000, bodyId: "b1" });

const INPUT_BODY = JSON.stringify({
  model: "claude-sonnet-5-5",
  system: "You are terse.",
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

const OUTPUT_BODY = [
  "event: content_block_start",
  'data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}',
  "",
  "event: content_block_delta",
  'data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello world"}}',
  "",
  "event: message_delta",
  'data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":41}}',
  "",
].join("\n");

function open(side: "input" | "output", answer: BodiesState, over = {}) {
  return renderApp(
    <Bodies side={side} request={REQUEST} />,
    { request: "a", requests: [REQUEST], drawerTab: side, bodies: { b1: answer }, ...over },
    NOW,
  );
}

describe("the bodies panes", () => {
  it("renders a kept input as the system text, the turns' role chips and folded tool rows that open", async () => {
    const { container } = open("input", { status: "kept", input: INPUT_BODY, output: "" });
    expect(container.textContent).toContain("As sent to Anthropic · redacted · kept 7 days");
    expect(container.textContent).toContain("You are terse.");
    expect(container.textContent).toContain("List the files");
    const roles = [...container.querySelectorAll("[data-role-chip]")].map((chip) => chip.textContent);
    expect(roles).toEqual(["user", "assistant", "user"]);
    // the tool call folds into a row: name and first argument, closed until the reader asks
    const call = screen.getByRole("button", { name: /Bash/ });
    expect(call.getAttribute("aria-expanded")).toBe("false");
    expect(call.textContent).toContain("ls -la");
    await userEvent.click(call);
    expect(call.getAttribute("aria-expanded")).toBe("true");
    expect(container.querySelector("pre")?.textContent).toContain('"command": "ls -la"');
    await userEvent.click(call);
    expect(call.getAttribute("aria-expanded")).toBe("false");
    // the result folds too, as its line count
    const result = screen.getByRole("button", { name: /Result/ });
    expect(result.textContent).toContain("ok · 3 lines");
    expect(result.getAttribute("aria-expanded")).toBe("false");
  });

  it("renders a kept output as its header with the token count and its text", () => {
    const { container } = open("output", { status: "kept", input: "", output: OUTPUT_BODY });
    expect(container.textContent).toContain("Streamed answer · 41 tokens");
    expect(container.textContent).toContain("Hello world");
    expect(screen.queryByRole("button", { name: /Bash|Result/ })).toBeNull();
  });

  it("clamps a long system prompt and unfolds it on Show all", async () => {
    const long = "You are terse. ".repeat(30);
    const body = JSON.stringify({
      system: long,
      messages: [{ role: "user", content: [{ type: "text", text: "hi" }] }],
    });
    const { container } = open("input", { status: "kept", input: body, output: "" });
    const show = screen.getByRole("button", { name: "Show all" });
    await userEvent.click(show);
    expect(container.textContent).toContain(long.trim());
    expect(screen.getByRole("button", { name: "Show less" })).toBeTruthy();
  });

  it("says when the bodies are not kept, off, owner-only, being read or broken", () => {
    const missing = open("input", { status: "missing" });
    expect(missing.container.textContent).toContain(
      "This request's bodies were not kept (the store was full or they are past 7 days).",
    );
    missing.unmount();
    const off = open("input", { status: "off" });
    expect(off.container.textContent).toContain("Request bodies are off. Turn them on in");
    const link = off.container.querySelector("a");
    expect(link?.getAttribute("href")).toBe("#/settings/hub");
    expect(link?.textContent).toBe("Settings › Hub");
    off.unmount();
    const forbidden = open("input", { status: "forbidden" });
    expect(forbidden.container.textContent).toContain("Only the owner reads request bodies.");
    forbidden.unmount();
    const loading = open("input", { status: "loading" });
    expect(loading.container.textContent).toContain("Reading the request bodies…");
    loading.unmount();
    const broken = open("input", { status: "error", cause: "GET /api/bodies/b1 → 500" });
    expect(broken.container.textContent).toContain("The request bodies could not be read");
    expect(broken.container.querySelector("[role='alert']")).toBeTruthy();
  });

  it("renders nothing for a request that carried no body id", () => {
    const bare = makeRequest({ id: "a", model: "glm-5.3", ts: NOW - 5_000 });
    const view = renderApp(
      <Bodies side="input" request={bare} />,
      {
        request: "a",
        requests: [bare],
        drawerTab: "input",
        bodies: { b1: { status: "kept", input: "{}", output: "" } },
      },
      NOW,
    );
    expect(view.container.textContent).toBe("");
  });

  it("folds a failed tool result in the fail ink, and a call whose first argument is not words", () => {
    const body = JSON.stringify({
      messages: [
        {
          role: "user",
          content: [{ type: "tool_result", tool_use_id: "t", is_error: true, content: "boom\nbang" }],
        },
        {
          role: "assistant",
          content: [{ type: "tool_use", name: "Edit", input: { file_path: { deep: 1 }, command: "ls" } }],
        },
      ],
    });
    const { container } = open("input", { status: "kept", input: body, output: "" });
    const result = screen.getByRole("button", { name: /Result/ });
    expect(result.textContent).toContain("error · 2 lines");
    expect(container.querySelector("[class*='ui-fail-ink']")).toBeTruthy();
    expect(screen.getByRole("button", { name: /Edit/ }).textContent).toContain("file_path");
  });

  it("shows an input body that is not a messages body as its raw bytes", () => {
    const { container } = open("input", { status: "kept", input: "just words", output: "" });
    expect(container.textContent).toContain("As sent to Anthropic · redacted · kept 7 days");
    expect(container.querySelector("pre")?.textContent).toBe("just words");
  });

  it("heads a plain answer with its tokens, one without any, and shows an unreadable answer raw", () => {
    const plain = open("output", {
      status: "kept",
      input: "",
      output: JSON.stringify({ content: [{ type: "text", text: "Done." }], usage: { output_tokens: 7 } }),
    });
    expect(plain.container.querySelector("p")?.textContent).toBe("Answer · 7 tokens");
    plain.unmount();
    const tokenless = open("output", {
      status: "kept",
      input: "",
      output: JSON.stringify({ content: [{ type: "text", text: "Done." }] }),
    });
    expect(tokenless.container.querySelector("p")?.textContent).toBe("Answer");
    tokenless.unmount();
    const raw = open("output", { status: "kept", input: "", output: "no json here" });
    expect(raw.container.querySelector("p")?.textContent).toBe("Answer");
    expect(raw.container.querySelector("pre")?.textContent).toBe("no json here");
  });

  it("folds a stream's half-spelled tool calls into rows that open on their raw text", async () => {
    const stream = [
      "event: content_block_start",
      'data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","name":"Bash"}}',
      "",
      "event: content_block_delta",
      'data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"7"}}',
      "",
      "event: content_block_start",
      'data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","name":"Wait"}}',
      "",
    ].join("\n");
    const { container } = open("output", { status: "kept", input: "", output: stream });
    const spelled = screen.getByRole("button", { name: /Bash/ });
    expect(spelled.textContent).toContain("7");
    await userEvent.click(spelled);
    expect(container.querySelectorAll("pre")[0]?.textContent).toBe("7");
    const empty = screen.getByRole("button", { name: /Wait/ });
    await userEvent.click(empty);
    expect(container.querySelectorAll("pre")[1]?.textContent).toBe("");
  });

  it("shows the bodies inside the inspector's Input and Output tabs, with no counts on those tabs", () => {
    const bodies = { b1: { status: "kept" as const, input: INPUT_BODY, output: OUTPUT_BODY } };
    const input = renderApp(
      <Inspector />,
      { request: "a", requests: [REQUEST], drawerTab: "input", bodies },
      NOW,
    );
    expect(input.container.textContent).toContain("As sent to Anthropic · redacted · kept 7 days");
    expect(screen.getAllByRole("tab").find((tab) => tab.textContent === "Input")).toBeTruthy();
    input.unmount();
    const output = renderApp(
      <Inspector />,
      { request: "a", requests: [REQUEST], drawerTab: "output", bodies },
      NOW,
    );
    expect(output.container.textContent).toContain("Streamed answer · 41 tokens");
  });

  it("announces a cancelled request in its own words, and a failed one with its cause", () => {
    const cancelled = renderApp(
      <OverviewTab request={makeRequest({ id: "c", model: "glm-5.3", ts: NOW - 5_000, status: "499" })} />,
      {},
      NOW,
    );
    expect(cancelled.container.querySelector("[data-failed]")?.textContent).toContain("Cancelled");
    expect(cancelled.container.textContent).toContain("The request was cancelled before the model finished.");
    cancelled.unmount();
    const failed = renderApp(
      <OverviewTab
        request={makeRequest({
          id: "f",
          model: "glm-5.3",
          ts: NOW - 5_000,
          status: "429",
          error: "rate limited",
        })}
      />,
      {},
      NOW,
    );
    const card = failed.container.querySelector("[data-failed]");
    expect(card?.textContent).toContain("Failed");
    expect(card?.textContent).toContain("rate limited");
  });
});
