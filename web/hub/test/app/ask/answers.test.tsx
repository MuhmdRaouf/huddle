// answers.test.tsx — the answer sheet's behaviour: the caption counts answered questions, Send
// waits until every question has an answer, a radio row picks one option, a checkbox row toggles,
// a typed answer replaces the picks, and Send carries the answers keyed by each question's text.

import { act, cleanup, fireEvent, render, screen } from "@testing-library/preact";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { AskAnswers } from "../../../src/app/ask/answers.tsx";
import type { NeedItem } from "../../../src/registry.ts";

/** The two questions as the ask's raw input string: one single choice, one multi-select. */
const input = JSON.stringify({
  questions: [
    {
      question: "Which database should this project use?",
      header: "Database",
      options: [
        { label: "Postgres", description: "The managed default" },
        { label: "SQLite", description: "One file on disk" },
      ],
      multiSelect: false,
    },
    {
      question: "Which linters should run?",
      header: "Lint",
      options: [
        { label: "ESLint", description: "The usual rules" },
        { label: "Biome", description: "One binary" },
      ],
      multiSelect: true,
    },
  ],
});

/** One open AskUserQuestion ask carrying those questions. */
const item: NeedItem = {
  id: 7,
  session: "s-1",
  kind: "permission",
  summary: "AskUserQuestion: 2 questions",
  created_at: "2026-10-08T11:59:30Z",
  state: "open",
  tool: "AskUserQuestion",
  input,
};

describe("AskAnswers", () => {
  const onSend = vi.fn();
  const onTerminal = vi.fn();

  afterEach(() => {
    cleanup();
    onSend.mockClear();
    onTerminal.mockClear();
  });

  /** Draws one sheet body over the two-question ask. */
  const draw = () => render(<AskAnswers item={item} onSend={onSend} onTerminal={onTerminal} />);

  /** Picks Postgres for the single choice and ESLint on the multi-select. */
  const answerBoth = async () => {
    await act(async () => {
      screen.getByRole("radio", { name: /Postgres/ }).click();
    });
    await act(async () => {
      screen.getByRole("checkbox", { name: /ESLint/ }).click();
    });
  };

  it("counts the answered questions in the caption", async () => {
    draw();
    expect(screen.getByText("0 of 2 answered")).toBeDefined();
    await answerBoth();
    expect(screen.getByText("2 of 2 answered")).toBeDefined();
  });

  it("keeps Send out of reach until every question is answered", async () => {
    draw();
    const send = screen.getByRole("button", { name: "Send answers" });
    expect(send.hasAttribute("disabled")).toBe(true);
    await act(async () => {
      send.click();
    });
    expect(onSend).not.toHaveBeenCalled();
    await answerBoth();
    expect(screen.getByRole("button", { name: "Send answers" }).hasAttribute("disabled")).toBe(false);
  });

  it("picking a radio row checks it and unchecks the other", async () => {
    draw();
    await act(async () => {
      screen.getByRole("radio", { name: /Postgres/ }).click();
    });
    expect(screen.getByRole("radio", { name: /Postgres/ }).getAttribute("aria-checked")).toBe("true");
    await act(async () => {
      screen.getByRole("radio", { name: /SQLite/ }).click();
    });
    expect(screen.getByRole("radio", { name: /Postgres/ }).getAttribute("aria-checked")).toBe("false");
    expect(screen.getByRole("radio", { name: /SQLite/ }).getAttribute("aria-checked")).toBe("true");
  });

  it("a checkbox row toggles", async () => {
    draw();
    await act(async () => {
      screen.getByRole("checkbox", { name: /ESLint/ }).click();
    });
    expect(screen.getByRole("checkbox", { name: /ESLint/ }).getAttribute("aria-checked")).toBe("true");
    await act(async () => {
      screen.getByRole("checkbox", { name: /ESLint/ }).click();
    });
    expect(screen.getByRole("checkbox", { name: /ESLint/ }).getAttribute("aria-checked")).toBe("false");
  });

  it("a typed answer replaces the picks and clearing brings them back", async () => {
    draw();
    await act(async () => {
      screen.getAllByRole("button", { name: "Type your own answer" })[0]?.click();
    });
    expect(screen.queryByRole("radio", { name: /Postgres/ })).toBeNull();
    fireEvent.input(screen.getByLabelText("Your answer"), { target: { value: "Dolt" } });
    expect(screen.getByText("1 of 2 answered")).toBeDefined();
    fireEvent.input(screen.getByLabelText("Your answer"), { target: { value: "" } });
    expect(screen.getByRole("radio", { name: /Postgres/ })).toBeDefined();
    expect(screen.queryByLabelText("Your answer")).toBeNull();
  });

  it("Send hands over the answers keyed by each question's text", async () => {
    draw();
    await answerBoth();
    await act(async () => {
      screen.getByRole("button", { name: "Send answers" }).click();
    });
    expect(onSend).toHaveBeenCalledTimes(1);
    expect(onSend).toHaveBeenCalledWith({
      "Which database should this project use?": "Postgres",
      "Which linters should run?": "ESLint",
    });
    expect(onTerminal).not.toHaveBeenCalled();
  });

  it("Answer in terminal hands the ask back without deciding", async () => {
    draw();
    await act(async () => {
      screen.getByRole("button", { name: "Answer in terminal" }).click();
    });
    expect(onTerminal).toHaveBeenCalledTimes(1);
    expect(onSend).not.toHaveBeenCalled();
  });
});
