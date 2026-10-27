// questions.test.ts — the AskUserQuestion input's reader at the edge and the answers a pick set
// builds: the input decodes into questions or the ask keeps today's preview, and the answers go
// to the server keyed by each question's exact text.

import { describe, expect, it } from "vite-plus/test";
import { answersOf, questionsOf } from "../../../src/app/ask/questions.ts";
import type { AskQuestion } from "../../../src/app/ask/questions.ts";

/** One single-choice question and one multi-select, the shape Claude's input carries; a tuple so
 *  an index reads as one question, not maybe-one. */
const QUESTIONS: [AskQuestion, AskQuestion] = [
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
];

/** The two questions as the ask's raw input string. */
const input = JSON.stringify({ questions: QUESTIONS });

describe("questionsOf", () => {
  it("reads a one-question input", () => {
    expect(questionsOf(JSON.stringify({ questions: [QUESTIONS[0]] }))).toEqual([QUESTIONS[0]]);
  });

  it("reads a two-question input with a multi-select", () => {
    expect(questionsOf(input)).toEqual(QUESTIONS);
  });

  it("refuses an input that is not JSON", () => {
    expect(questionsOf("npm test -- --run e2e")).toBeNull();
  });

  it("refuses an input without questions", () => {
    expect(questionsOf(JSON.stringify({ command: "bun test" }))).toBeNull();
  });

  it("refuses a question without text", () => {
    expect(questionsOf(JSON.stringify({ questions: [{ header: "Lint", options: [] }] }))).toBeNull();
  });
});

describe("answersOf", () => {
  /** The two questions decoded, for the tests that pick over both. */
  const both = questionsOf(input) ?? [];

  it("names one picked label", () => {
    expect(answersOf([QUESTIONS[0]], [{ picked: ["Postgres"] }])).toEqual({
      "Which database should this project use?": "Postgres",
    });
  });

  it("joins a multi-select's picks in option order, however they were picked", () => {
    expect(answersOf(both, [{ picked: ["Postgres"] }, { picked: ["Biome", "ESLint"] }])).toEqual({
      "Which database should this project use?": "Postgres",
      "Which linters should run?": "ESLint,Biome",
    });
  });

  it("sends a typed answer trimmed, and the picks once it clears", () => {
    expect(answersOf([QUESTIONS[0]], [{ picked: ["SQLite"], typed: "  Dolt  " }])).toEqual({
      "Which database should this project use?": "Dolt",
    });
    expect(answersOf([QUESTIONS[0]], [{ picked: ["SQLite"], typed: "   " }])).toEqual({
      "Which database should this project use?": "SQLite",
    });
  });

  it("waits while a question has no answer", () => {
    expect(answersOf(both, [{ picked: ["Postgres"] }, { picked: [] }])).toBeNull();
  });
});
