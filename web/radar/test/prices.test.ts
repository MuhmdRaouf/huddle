import { describe, expect, it } from "vite-plus/test";
import {
  addCost,
  costByFamily,
  costFamilyOf,
  costOfAll,
  flashOf,
  isSmallModel,
  normalizeModel,
  offeringHostOf,
  pluginOf,
  requestCost,
} from "../src/cost/prices.ts";
import { ZERO_TOKENS } from "../src/shared/model.ts";
import { makeRequest } from "./helpers.ts";

/** The price table the dashboard's runtime library reads: list prices per model and token kind. */
const M = { ...ZERO_TOKENS, input: 1_000_000 };
const ZAI = "https://api.z.ai";

describe("prices", () => {
  it("normalizes ids and finds rows case-insensitively", () => {
    expect(normalizeModel("zai/GLM-5.3[1m]")).toBe("glm-5.3");
    expect(normalizeModel("minimax-m3")).toBe("minimax-m3");
  });

  it("prices a claude request with its 1 h writes, fast mode and US geo", () => {
    // 1 M in, 1 M out, 1 M cache read, 1 M 5 m + 1 M 1 h writes: fast rates 8/40, cache 0.4/10/16, then ×1.1
    const cost = requestCost(
      makeRequest({
        model: "claude-opus-5-5",
        ts: Date.parse("2026-10-08T12:00:00Z"),
        tokens: { input: 1e6, output: 1e6, cacheRead: 1e6, cacheWrite: 2e6 },
        cacheWrite1h: 1e6,
        speed: "fast",
        geo: "us",
      }),
    );
    expect(cost?.usd).toBeCloseTo(81.84);
    expect(cost?.detail).toEqual(["Anthropic list", "fast mode", "US geo ×1.1", "1 h cache writes"]);
  });

  it("prices a zai request by its upstream host and every token kind at its own rate", () => {
    // stopReason set: an in-flight record (no stop reason, no output) is never priced
    const cost = requestCost(
      makeRequest({ model: "glm-5.3", upstream: "https://api.z.ai", tokens: M, stopReason: "end_turn" }),
    );
    expect(cost?.usd).toBeCloseTo(1.4);
    expect(cost?.detail).toEqual(["Z.ai list"]);
    const kinds = requestCost(
      makeRequest({
        model: "glm-5.3",
        upstream: "https://api.z.ai",
        tokens: { input: 0, output: 1e6, cacheRead: 1e6, cacheWrite: 1e6 },
      }),
    );
    expect(kinds?.usd).toBeCloseTo(4.66);
    const writes = requestCost(
      makeRequest({
        model: "kimi-k3",
        upstream: "https://api.moonshot.ai",
        tokens: { ...ZERO_TOKENS, cacheWrite: 1e6 },
        stopReason: "end_turn",
      }),
    );
    expect(writes?.usd).toBeCloseTo(3);
  });

  it("prices a deepseek request off-peak at half", () => {
    // Saturday 12:00 UTC: outside both weekday peak windows, so the 1.32 input rate bills at half
    const cost = requestCost(
      makeRequest({
        model: "deepseek-v4-pro",
        upstream: "https://api.deepseek.com",
        ts: Date.parse("2026-10-10T12:00:00Z"),
        tokens: M,
        stopReason: "end_turn",
      }),
    );
    expect(cost?.usd).toBeCloseTo(0.66);
    expect(cost?.detail).toEqual(["DeepSeek list", "off-peak ×0.5"]);
  });

  it("leaves what nothing prices at null", () => {
    expect(requestCost(makeRequest({ model: "not-a-model" }))).toBeNull();
    expect(requestCost(makeRequest({ model: "claude-x" }))).toBeNull();
  });

  it("never prices a request still streaming: no stop reason and no output yet", () => {
    expect(requestCost(makeRequest({ model: "glm-5.3", upstream: ZAI, tokens: M }))).toBeNull();
    // the same numbers once the request has finished do price
    expect(
      requestCost(makeRequest({ model: "glm-5.3", upstream: ZAI, tokens: M, stopReason: "end_turn" }))?.usd,
    ).toBeCloseTo(1.4);
  });

  it("sums costs, treating null as nothing priced", () => {
    expect(addCost(null, null)).toBeNull();
    expect(addCost(null, 1)).toBe(1);
    expect(addCost(2, null)).toBe(2);
    expect(addCost(1, 2)).toBe(3);
    expect(costOfAll([])).toBeNull();
    expect(
      costOfAll([
        { model: "glm-5.3", upstream: "https://api.z.ai", ts: 0, tokens: M },
        { model: "claude-x", ts: 0, tokens: M },
      ]),
    ).toBeCloseTo(1.4);
  });

  it("splits a request list's cost by family, each family null until something of it prices", () => {
    expect(costFamilyOf("zai/glm-5.3[1m]")).toBe("glm");
    expect(costFamilyOf("claude-opus-5-5")).toBe("claude");
    expect(costFamilyOf("Opus-5.5")).toBe("claude"); // a sonnet/haiku/opus id reads as Claude's
    expect(costFamilyOf("kimi-k3")).toBe("other");
    expect(costFamilyOf("not-a-model")).toBe("other");
    const split = costByFamily([
      { model: "claude-opus-5-5", upstream: "https://api.anthropic.com", ts: 0, tokens: M },
      { model: "glm-5.3", upstream: "https://api.z.ai", ts: 0, tokens: M },
      { model: "kimi-k3", upstream: "https://api.moonshot.ai", ts: 0, tokens: M },
      { model: "not-a-model", ts: 0, tokens: M }, // unpriced: no family, no sum
    ]);
    expect(split.claude).toBeGreaterThan(0);
    expect(split.glm).toBeCloseTo(1.4);
    expect(split.other).toBeGreaterThan(0);
    // the parts and costOfAll price the very same requests, so they always agree
    expect(addCost(addCost(split.claude, split.glm), split.other)).toBeCloseTo(
      costOfAll([
        { model: "claude-opus-5-5", upstream: "https://api.anthropic.com", ts: 0, tokens: M },
        { model: "glm-5.3", upstream: "https://api.z.ai", ts: 0, tokens: M },
        { model: "kimi-k3", upstream: "https://api.moonshot.ai", ts: 0, tokens: M },
      ]) ?? -1,
    );
    expect(costByFamily([{ model: "not-a-model", ts: 0, tokens: M }])).toEqual({
      claude: null,
      glm: null,
      other: null,
    });
    expect(costByFamily([])).toEqual({ claude: null, glm: null, other: null });
  });

  it("maps models to plugins and to their flash siblings", () => {
    expect(pluginOf("glm-5.3")).toBe("zai");
    expect(pluginOf("kimi-k3")).toBe("kimi");
    expect(pluginOf("deepseek-flash")).toBe("deepseek");
    expect(pluginOf("MiniMax-M3")).toBe("minimax");
    expect(pluginOf("qwen3.8-max")).toBe("qwen");
    expect(pluginOf("claude-opus-5")).toBeNull();
    expect(flashOf("glm-5.3")).toBe("glm-5.3-flash");
    expect(flashOf("claude-opus-5")).toBeNull();
    expect(isSmallModel("glm-5.3-flash")).toBe(true);
    expect(isSmallModel("claude-haiku-5")).toBe(true);
    expect(isSmallModel("glm-5.3")).toBe(false);
  });

  it("names the host a model's own offering serves it on, and nobody's else", () => {
    expect(offeringHostOf("glm-5.3")).toBe("https://api.z.ai");
    expect(offeringHostOf("Kimi-K3")).toBe("https://api.moonshot.ai");
    expect(offeringHostOf("claude-opus-5-5")).toBeNull(); // Anthropic's own ids stay unnamed
    expect(offeringHostOf("not-a-model")).toBeNull();
  });
});
