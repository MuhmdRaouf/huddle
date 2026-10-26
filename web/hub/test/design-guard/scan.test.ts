// @vitest-environment node
// scan.test.ts — the design guard's pure parts: how each rule matches, when a literal counts as a
// class literal, and which sentences the ratchet answers. The guard that walks web/hub/src lives in
// ../design-guard.test.ts.
import { describe, expect, it } from "vite-plus/test";
import { legacyWords, ratchet, scanFile } from "./scan.ts";

const NO_LEGACY = new Set<string>();

describe("design guard scan", () => {
  it("finds one offence per rule, with its line and text", () => {
    expect(scanFile("a.tsx", 'one\ntwo <div class="btn">', NO_LEGACY)).toEqual([
      { rule: "daisy", line: 2, text: "btn" },
    ]);
    expect(scanFile("a.tsx", '<div class="hr-panel">', NO_LEGACY)).toEqual([
      { rule: "hr", line: 1, text: "hr-panel" },
    ]);
    expect(scanFile("a.tsx", '<div class="muted">', new Set(["muted"]))).toEqual([
      { rule: "legacy", line: 1, text: "muted" },
    ]);
    expect(scanFile("a.ts", 'const C = "#ff0000";', NO_LEGACY)).toEqual([
      { rule: "colour", line: 1, text: "#ff0000" },
    ]);
    expect(scanFile("a.ts", 'const W = "16px";', NO_LEGACY)).toEqual([{ rule: "px", line: 1, text: "16px" }]);
    expect(scanFile("a.tsx", "const A = () => <button>go</button>;", NO_LEGACY)).toEqual([
      { rule: "control", line: 1, text: "<button" },
    ]);
    // the control rule reads elements, so it stays silent outside .tsx
    expect(scanFile("a.ts", "const A = () => <button>go</button>;", NO_LEGACY)).toEqual([]);
  });

  it("reads a css file's selectors as tokens and its whole text for colours and px", () => {
    expect(
      scanFile("a.css", ".card > .btn-primary {\n  color: #fff;\n  padding: 4px;\n}", NO_LEGACY),
    ).toEqual([
      { rule: "daisy", line: 1, text: "card" },
      { rule: "daisy", line: 1, text: "btn-primary" },
      { rule: "colour", line: 2, text: "#fff" },
      { rule: "px", line: 3, text: "4px" },
    ]);
  });

  it("counts every daisy token in one class literal", () => {
    expect(scanFile("a.tsx", '<div class="btn btn-primary">', NO_LEGACY)).toEqual([
      { rule: "daisy", line: 1, text: "btn" },
      { rule: "daisy", line: 1, text: "btn-primary" },
    ]);
  });

  it("strips tailwind variants and the leading ! before matching a token", () => {
    expect(scanFile("a.tsx", '<div class="hover:btn-ghost">', NO_LEGACY)).toEqual([
      { rule: "daisy", line: 1, text: "btn-ghost" },
    ]);
    expect(scanFile("a.tsx", '<span class="!badge-sm" />', NO_LEGACY)).toEqual([
      { rule: "daisy", line: 1, text: "badge-sm" },
    ]);
  });

  it("leaves tailwind utilities, prose, routes and zero px alone", () => {
    const quiet = [
      '<div class="select-none list-disc tab-4 table filter collapse text-sm bg-[var(--ui-surface)]">',
      'use("menu");',
      'const label = "Open the menu";',
      'go("#/sessions");',
      'const zero = "0px";',
      'const B = <Button size="sm">go</Button>;',
    ].join("\n");
    expect(scanFile("a.tsx", quiet, NO_LEGACY)).toEqual([]);
  });

  it("takes a single-token literal as classes after class=, className= and an upper-case const", () => {
    expect(scanFile("a.tsx", '<div class="card">', NO_LEGACY)).toEqual([
      { rule: "daisy", line: 1, text: "card" },
    ]);
    expect(scanFile("a.tsx", '<Comp className="badge" />', NO_LEGACY)).toEqual([
      { rule: "daisy", line: 1, text: "badge" },
    ]);
    expect(scanFile("a.ts", 'const MUTED = "muted";', new Set(["muted"]))).toEqual([
      { rule: "legacy", line: 1, text: "muted" },
    ]);
  });

  it("reads rgb(var()) as no colour and rgba(0, …) as one", () => {
    expect(scanFile("a.ts", 'const tint = "rgb(var(--x))";', NO_LEGACY)).toEqual([]);
    expect(scanFile("a.ts", 'const shadow = "rgba(0, 0, 0, 0.5)";', NO_LEGACY)).toEqual([
      { rule: "colour", line: 1, text: "rgba(0, 0, 0, 0.5)" },
    ]);
  });

  it("reads the class names out of a selector list", () => {
    const words = legacyWords(".chart-tip .tip-head, .muted:hover {");
    expect([...words].sort()).toEqual(["chart-tip", "muted", "tip-head"]);
  });

  it("answers nothing when the counts equal the baseline", () => {
    const counts = { "a.tsx": { daisy: 1, px: 2 } };
    expect(ratchet(counts, { "a.tsx": { daisy: 1, px: 2 } })).toEqual([]);
  });

  it("answers one sentence for a count over its baseline", () => {
    expect(ratchet({ "a.tsx": { daisy: 1 } }, { "a.tsx": { daisy: 2 } })).toEqual([
      "a.tsx: daisy is 2, over its baseline of 1",
    ]);
  });

  it("answers one sentence for an offence in a file the baseline lacks", () => {
    expect(ratchet({}, { "b.tsx": { px: 3 } })).toEqual(["b.tsx: px is 3, over its baseline of 0"]);
  });

  it("answers one sentence for a count under its baseline", () => {
    expect(ratchet({ "a.tsx": { daisy: 2 } }, { "a.tsx": { daisy: 1 } })).toEqual([
      "a.tsx: daisy is 1, under its baseline of 2: lower the baseline to 1",
    ]);
  });

  it("answers one sentence for a baseline entry whose file is gone", () => {
    expect(ratchet({ "gone.tsx": { daisy: 2 } }, {})).toEqual([
      "gone.tsx: daisy is 0, under its baseline of 2: lower the baseline to 0",
    ]);
  });
});
