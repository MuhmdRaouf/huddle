// diff.test.ts — the line diff the Edit preview draws: context, removed and added lines, in order.

import { describe, expect, it } from "vite-plus/test";
import { lineDiff } from "../../../src/app/ask/diff.ts";

describe("lineDiff", () => {
  it("marks unchanged lines as context", () => {
    expect(lineDiff("a\nb\nc", "a\nb\nc")).toEqual([
      { sign: " ", text: "a" },
      { sign: " ", text: "b" },
      { sign: " ", text: "c" },
    ]);
  });

  it("marks an added block with +", () => {
    expect(lineDiff("a\nd", "a\nb\nc\nd")).toEqual([
      { sign: " ", text: "a" },
      { sign: "+", text: "b" },
      { sign: "+", text: "c" },
      { sign: " ", text: "d" },
    ]);
  });

  it("marks a removed block with -", () => {
    expect(lineDiff("a\nb\nc\nd", "a\nd")).toEqual([
      { sign: " ", text: "a" },
      { sign: "-", text: "b" },
      { sign: "-", text: "c" },
      { sign: " ", text: "d" },
    ]);
  });

  it("shows a changed middle line as - then +", () => {
    expect(lineDiff("a\nb\nc", "a\nB\nc")).toEqual([
      { sign: " ", text: "a" },
      { sign: "-", text: "b" },
      { sign: "+", text: "B" },
      { sign: " ", text: "c" },
    ]);
  });

  it("matches repeated lines without swallowing the one between them", () => {
    expect(lineDiff("x\ny\nx", "x\nx")).toEqual([
      { sign: " ", text: "x" },
      { sign: "-", text: "y" },
      { sign: " ", text: "x" },
    ]);
  });
});
