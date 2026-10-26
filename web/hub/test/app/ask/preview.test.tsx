// preview.test.tsx — what AskPreview draws per tool: the Bash command with its why, the Edit line
// diff with its fold, the Write head, the fetch's URL and the search's query, the generic key rows,
// and the cut-input notice.

import { act, cleanup, render, screen } from "@testing-library/preact";
import { afterEach, describe, expect, it } from "vite-plus/test";
import { AskPreview } from "../../../src/app/ask/preview.tsx";
import { stubClipboard } from "../../helpers.tsx";

afterEach(cleanup);

/** The JSON input a tool carries, as the registry stores it: one string. */
const json = (v: unknown): string => JSON.stringify(v);

describe("AskPreview", () => {
  it("shows a Bash command in a wrapping code box with a copy button and its why", async () => {
    render(
      <AskPreview tool="Bash" input={json({ command: "bun run test", description: "Build the bundle" })} />,
    );
    const box = screen.getByText("bun run test");
    expect(box.tagName).toBe("CODE");
    expect(box.className).toContain("whitespace-pre-wrap");
    const clip = stubClipboard();
    await act(async () => {
      screen.getByRole("button", { name: "Copy" }).click();
    });
    expect(clip.writes).toEqual(["bun run test"]);
    expect(screen.getByRole("heading", { name: "Why" })).toBeDefined();
    expect(screen.getByText("Build the bundle")).toBeDefined();
  });

  it("draws an Edit as a file row with its counts and a line diff with gutter signs", () => {
    render(
      <AskPreview
        tool="Edit"
        input={json({
          file_path: "/repo/web/main.ts",
          old_string: "const a = 1;\nkeep();",
          new_string: "const a = 2;\nkeep();",
        })}
      />,
    );
    expect(screen.getByText("/repo/web/main.ts")).toBeDefined();
    const plus = screen.getByText("+1");
    expect(plus.className).toContain("ok-ink");
    const minus = screen.getByText("−1");
    expect(minus.className).toContain("fail-ink");
    const gone = screen.getByText("const a = 1;").closest("[data-sign]");
    expect(gone?.getAttribute("data-sign")).toBe("-");
    expect(gone?.className).toContain("fail-soft");
    const added = screen.getByText("const a = 2;").closest("[data-sign]");
    expect(added?.getAttribute("data-sign")).toBe("+");
    expect(added?.className).toContain("ok-soft");
    expect(screen.getByText("keep();").closest("[data-sign]")?.getAttribute("data-sign")).toBe(" ");
  });

  it("draws MultiEdit as one block per edit", () => {
    render(
      <AskPreview
        tool="MultiEdit"
        input={json({
          file_path: "/repo/web/main.ts",
          edits: [
            { old_string: "a", new_string: "b" },
            { old_string: "c", new_string: "d\ne" },
          ],
        })}
      />,
    );
    expect(screen.getByText("+1")).toBeDefined();
    expect(screen.getByText("+2")).toBeDefined();
    expect(screen.getAllByText("−1").length).toBe(2);
    expect(document.querySelectorAll("[data-sign='-']").length).toBe(2);
    expect(document.querySelectorAll("[data-sign='+']").length).toBe(3);
  });

  it("folds a long diff at twelve lines and opens the rest in place", async () => {
    const old_lines = ["1", "2", "3"].join("\n");
    const new_lines = Array.from({ length: 15 }, (_, i) => String(i + 1)).join("\n");
    render(
      <AskPreview
        tool="Edit"
        input={json({ file_path: "/repo/big.ts", old_string: old_lines, new_string: new_lines })}
      />,
    );
    // three context lines plus the first nine added rows fill the fold; rows 13 to 15 wait
    expect(document.querySelectorAll("[data-diff] [data-sign]").length).toBe(12);
    expect(screen.queryByText("13")).toBeNull();
    await act(async () => {
      screen.getByRole("button", { name: "Show the whole diff · 3 more changes" }).click();
    });
    expect(document.querySelectorAll("[data-diff] [data-sign]").length).toBe(15);
    expect(screen.getByText("13")).toBeDefined();
    expect(screen.getByText("15")).toBeDefined();
    expect(screen.queryByRole("button", { name: /Show the whole diff/ })).toBeNull();
  });

  it("shows a Write's path and the head of its content", () => {
    const lines = Array.from({ length: 14 }, (_, i) => `l-${String(i + 1).padStart(2, "0")}`);
    render(
      <AskPreview tool="Write" input={json({ file_path: "/repo/new.ts", content: lines.join("\n") })} />,
    );
    expect(screen.getByText("/repo/new.ts")).toBeDefined();
    const code = screen.getByText(/l-01 l-02/).closest("code");
    expect(code?.textContent).toContain("l-12");
    expect(code?.textContent).not.toContain("l-13");
    expect(screen.getByText("2 more lines")).toBeDefined();
  });

  it("shows a WebFetch's URL in a code box and its prompt", () => {
    render(
      <AskPreview
        tool="WebFetch"
        input={json({ url: "https://example.com/a", prompt: "Summarise the page" })}
      />,
    );
    expect(screen.getByText("https://example.com/a").closest("code")).not.toBeNull();
    expect(screen.getByText("Summarise the page")).toBeDefined();
  });

  it("shows a WebSearch's query in a code box", () => {
    render(<AskPreview tool="WebSearch" input={json({ query: "huddle kanban" })} />);
    expect(screen.getByText("huddle kanban").closest("code")).not.toBeNull();
  });

  it("shows another tool's input as one row per key, objects and arrays as compact JSON", () => {
    render(
      <AskPreview tool="NotebookEdit" input={json({ path: "/x/y.ipynb", cells: [1, 2], opts: { a: 1 } })} />,
    );
    expect(screen.getByText("path")).toBeDefined();
    expect(screen.getByText("/x/y.ipynb")).toBeDefined();
    expect(screen.getByText("cells")).toBeDefined();
    expect(screen.getByText("[1,2]")).toBeDefined();
    expect(screen.getByText("opts")).toBeDefined();
    expect(screen.getByText('{"a":1}')).toBeDefined();
  });

  it("keeps the cut-input notice when the item says the input was truncated", () => {
    render(<AskPreview tool="Bash" input={json({ command: "make all" })} truncated />);
    expect(screen.getByText(/the input was cut; allow it in the terminal/)).toBeDefined();
  });

  it("shows input that is not JSON as plain text in the code box", () => {
    render(<AskPreview tool="Bash" input="npm run build" />);
    const box = screen.getByText("npm run build");
    expect(box.tagName).toBe("CODE");
  });
});
