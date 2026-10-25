import { readFileSync } from "node:fs";
import { describe, expect, it } from "vite-plus/test";
import { ICONS } from "../../src/radar/icons.ts";
import {
  alertLabel,
  budgetTone,
  eventMeta,
  latencyClass,
  modelColor,
  providerColor,
  SERIES,
  STATUS_COLOR,
  TOKEN_KINDS,
} from "../../src/radar/palette.ts";
import type { AlertKind } from "../../src/radar/shared/api.ts";

/** A colour a scheme and a flavour follow: one --ui-* token, or one of radar.css's series. */
const TOKEN_COLOUR = /^var\(--(ui-[a-z-]+|radar-series-[1-8])\)$/;

/** radar.css's series declarations, name to value. */
const SERIES_CSS = new Map(
  [...readFileSync("src/radar/radar.css", "utf8").matchAll(/(--radar-series-\d+):\s*([^;]+);/g)].map(
    (m) => [m[1] ?? "", m[2] ?? ""] as const,
  ),
);

describe("TOKEN_KINDS", () => {
  it("gives each token kind its own series colour and a readable label", () => {
    expect(TOKEN_KINDS.map((kind) => kind.key)).toEqual(["input", "output", "cacheRead", "cacheWrite"]);
    expect(TOKEN_KINDS.map((kind) => kind.label)).toEqual(["Input", "Output", "Cache read", "Cache write"]);
    expect(new Set(TOKEN_KINDS.map((kind) => kind.color)).size).toBe(4);
    for (const kind of TOKEN_KINDS) expect(kind.color).toMatch(TOKEN_COLOUR);
  });
});

describe("latencyClass", () => {
  it("is success under 60% of budget, warning under 80%, danger at or beyond", () => {
    expect(latencyClass(0)).toBe("var(--ui-ok)");
    expect(latencyClass(0.59)).toBe("var(--ui-ok)");
    expect(latencyClass(0.6)).toBe("var(--ui-warn)");
    expect(latencyClass(0.79)).toBe("var(--ui-warn)");
    expect(latencyClass(0.8)).toBe("var(--ui-fail)");
    expect(latencyClass(1.4)).toBe("var(--ui-fail)");
  });
});

describe("STATUS_COLOR", () => {
  it("maps every tone to its tokens.css mark", () => {
    expect(Object.keys(STATUS_COLOR).sort()).toEqual(["err", "idle", "info", "ok", "warn"]);
    expect(STATUS_COLOR.ok).toBe("var(--ui-ok)");
    expect(STATUS_COLOR.err).toBe("var(--ui-fail)");
    expect(STATUS_COLOR.warn).toBe("var(--ui-warn)");
    expect(STATUS_COLOR.info).toBe("var(--ui-info)");
    expect(STATUS_COLOR.idle).toBe("var(--ui-muted)");
  });
});

describe("eventMeta", () => {
  it("maps every hook event and route to an icon, a tone and plain words", () => {
    expect(eventMeta("SessionStart")).toEqual({ icon: "play", tone: "ok", label: "Session started" });
    expect(eventMeta("SessionEnd")).toEqual({ icon: "stop", tone: "idle", label: "Session ended" });
    expect(eventMeta("UserPromptSubmit")).toEqual({ icon: "message", tone: "info", label: "Prompt" });
    expect(eventMeta("PreToolUse")).toEqual({ icon: "wrench", tone: "info", label: "Tool started" });
    expect(eventMeta("PostToolUse")).toEqual({ icon: "check", tone: "ok", label: "Tool finished" });
    expect(eventMeta("PostToolUseFailure")).toEqual({ icon: "close", tone: "err", label: "Tool failed" });
    expect(eventMeta("SubagentStart")).toEqual({ icon: "branch", tone: "info", label: "Subagent started" });
    expect(eventMeta("SubagentStop")).toEqual({ icon: "merge", tone: "idle", label: "Subagent finished" });
    expect(eventMeta("Stop")).toEqual({ icon: "pause", tone: "idle", label: "Turn ended" });
    expect(eventMeta("PreCompact")).toEqual({ icon: "fold", tone: "warn", label: "Compacting" });
    expect(eventMeta("Notification")).toEqual({ icon: "bell", tone: "warn", label: "Notification" });
    expect(eventMeta("route")).toEqual({ icon: "arrows", tone: "info", label: "Routed" });
  });

  it("falls back to a neutral dot that keeps the raw kind", () => {
    expect(eventMeta("SomethingNew")).toEqual({ icon: "dot", tone: "idle", label: "SomethingNew" });
    expect(eventMeta("")).toEqual({ icon: "dot", tone: "idle", label: "Event" });
  });

  it("only ever names icons that exist", () => {
    const kinds = ["SessionStart", "SessionEnd", "UserPromptSubmit", "PreToolUse", "PostToolUse"];
    for (const kind of [...kinds, "PostToolUseFailure", "SubagentStart", "SubagentStop", "Stop", "x"]) {
      expect(Object.keys(ICONS)).toContain(eventMeta(kind).icon);
    }
  });
});

describe("SERIES", () => {
  it("draws eight distinct series, each from the tokens", () => {
    expect(SERIES).toHaveLength(8);
    expect(new Set(SERIES).size).toBe(8);
    for (const colour of SERIES) expect(colour).toMatch(TOKEN_COLOUR);
    // radar.css builds every series from the token roles alone: one role, or a mix of two
    expect(new Set(SERIES_CSS.values()).size).toBe(8);
    for (const colour of SERIES) {
      const value = SERIES_CSS.get(colour.slice(4, -1));
      expect(value).toMatch(
        /^(var\(--ui-[a-z-]+\)|color-mix\(in oklab, var\(--ui-[a-z-]+\) \d+%, var\(--ui-[a-z-]+\)\))$/,
      );
    }
  });
});

describe("providerColor", () => {
  it("colours every known provider from the series and falls back for the rest", () => {
    expect(providerColor("Anthropic")).toBe(SERIES[3]);
    expect(providerColor("Z.ai")).toBe(SERIES[0]);
    expect(providerColor("Moonshot")).toBe(SERIES[4]);
    expect(providerColor("DeepSeek")).toBe(SERIES[1]);
    expect(providerColor("MiniMax")).toBe(SERIES[5]);
    expect(providerColor("Qwen")).toBe(SERIES[7]);
    expect(providerColor("route")).toBe(SERIES[0]);
    expect(providerColor("other")).toBe("var(--ui-muted)");
  });

  it("treats missing as other", () => {
    expect(providerColor(null)).toBe("var(--ui-muted)");
    expect(providerColor(undefined)).toBe("var(--ui-muted)");
    expect(providerColor("made-up")).toBe("var(--ui-muted)");
  });
});

describe("modelColor", () => {
  it("stays loyal to a model and spreads several across the series", () => {
    expect(modelColor("glm-5.3")).toBe(modelColor("glm-5.3"));
    for (const color of ["glm-5.3", "claude-opus-5", "kimi-k3", "deepseek-v4-pro", ""].map(modelColor))
      expect(SERIES).toContain(color);
    expect(
      new Set(["glm-5.3", "claude-opus-5", "kimi-k3", "deepseek-v4-pro"].map(modelColor)).size,
    ).toBeGreaterThan(1);
  });
});

describe("alertLabel", () => {
  it("names every alert kind in plain words", () => {
    const kinds: AlertKind[] = [
      "stuck",
      "loop",
      "retry_storm",
      "context",
      "budget",
      "upstream",
      "quota_nearly_used",
    ];
    expect(kinds.map(alertLabel)).toEqual([
      "Stuck",
      "Loop",
      "Retry storm",
      "Context nearly full",
      "Budget",
      "Claude cannot reach Anthropic through huddle-router",
      "Quota nearly used",
    ]);
  });
});

describe("budgetTone", () => {
  it("is ok under 80% of the budget, warn from there and err at the limit", () => {
    expect(budgetTone(10)).toBe("ok");
    expect(budgetTone(79.9)).toBe("ok");
    expect(budgetTone(80)).toBe("warn");
    expect(budgetTone(99)).toBe("warn");
    expect(budgetTone(100)).toBe("err");
    expect(budgetTone(120)).toBe("err");
  });
});
