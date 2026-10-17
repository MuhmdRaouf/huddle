import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vite-plus/test";
import {
  type ModelSheet,
  OFFERINGS,
  type Offering,
  PRICES_RETRIEVED,
  priceRequest,
} from "../src/cost/price-table.ts";

/**
 * Radar's price table and the Go engine's (internal/radar/cost/price_table.go) are one table in two copies;
 * neither may drift. The Go file is parsed as text — no toolchain needed — by a small reader of its literal shapes
 * (composite literals, f64Ptr/strPtr wrappers, string concatenation, underscored numbers), which throws when the
 * file's shape changes so a reshape never passes silently.
 */
const GO = join(import.meta.dirname, "../../../internal/radar/cost/price_table.go");

/** The text with its string literals blanked (the file's literals carry no escaped quotes), so brace and paren
 *  matching can run on it directly. */
function codeOnly(text: string): string {
  const chars = text.split("");
  let inString = false;
  for (let at = 0; at < chars.length; at++) {
    const char = chars[at];
    if (char === '"') inString = !inString;
    if (inString) chars[at] = " ";
  }
  return chars.join("");
}

/** The index of the `close` that matches the `open` at `openAt` (a quote-blanked text). */
function matching(code: string, openAt: number, close: string): number {
  let depth = 0;
  for (let at = openAt; at < code.length; at++) {
    if (code[at] === "{" || code[at] === "(") depth++;
    else if ((code[at] === "}" || code[at] === ")") && --depth === 0) {
      if (code[at] !== close) break;
      return at;
    }
  }
  throw new Error(`unbalanced brackets in price_table.go from ${openAt}`);
}

/** The text inside the brace block that starts at `open` (the index of "{"), string literals respected. */
function block(text: string, open: number): string {
  return text.slice(open + 1, matching(codeOnly(text), open, "}"));
}

/** The `{…}` blocks of a composite literal's body, at the body's own depth. */
function topBlocks(body: string): string[] {
  const blocks: string[] = [];
  const code = codeOnly(body);
  for (let at = 0; at < body.length; at++) {
    if (code[at] !== "{") continue;
    const inner = block(body, at);
    blocks.push(inner);
    at += inner.length + 1;
  }
  return blocks;
}

/** The text inside the parens that start at `open` (the index of "("), string literals respected. */
function parenBlock(text: string, open: number): string {
  return text.slice(open + 1, matching(codeOnly(text), open, ")"));
}

/** The value of a Go string initializer: one literal, or several joined with `+` across lines. */
function goString(text: string): string {
  const pieces = [...text.matchAll(/"((?:[^"\\]|\\.)*)"/g)].map((match) =>
    (match[1] ?? "").replace(/\\"/g, '"'),
  );
  if (pieces.length === 0) throw new Error(`no string literal in: ${text.slice(0, 80)}`);
  return pieces.join("");
}

/** Every top-level `const`/`var` string of price_table.go, by name (the sources, the notes, the retrieved day). */
function goStrings(source: string): Map<string, string> {
  const strings = new Map<string, string>();
  for (const match of source.matchAll(
    /^(?:const|var)\s+(\w+)\s*(?::\s*[\w[\]]+\s*)?=\s*((?:"(?:[^"\\]|\\.)*"\s*(?:\+\s*)?)+)/gm,
  ))
    strings.set(match[1] ?? "", goString(match[2] ?? ""));
  return strings;
}

/** A Go number with underscores, as a JavaScript number. */
function num(text: string): number {
  return Number(text.replace(/_/g, ""));
}

/** The fields of a `Rates{…}` body, with `f64Ptr(x)` unwrapped to x and Go names lower-cased to the table's. */
function ratesOf(body: string): Record<string, number> {
  const rates: Record<string, number> = {};
  for (const match of body.matchAll(/(\w+):\s*(?:f64Ptr\()?([\d_.]+)\)?/g)) {
    const name = match[1] ?? "";
    rates[name.charAt(0).toLowerCase() + name.slice(1)] = num(match[2] ?? "");
  }
  return rates;
}

/** The fields of one `ModelSheet`, in radar's shape; every helper throws when price_table.go's shape changes. */
function sheetOf(
  body: string,
  strings: ReadonlyMap<string, string>,
  offPeaks: ReadonlyMap<
    string,
    { multiplier: number; peak: { days: number[]; start: number; end: number }[] }
  >,
): ModelSheet {
  const base = /Base:\s*Rates\{([^}]*)\}/.exec(body);
  if (base === null) throw new Error(`price_table.go: a sheet has no Base rates: ${body.slice(0, 80)}`);
  const sheet: Record<string, unknown> = { base: ratesOf(base[1] ?? "") };
  const tiers = tiersOf(body);
  if (tiers !== undefined) sheet.tiers = tiers;
  const offPeak = /OffPeak:\s*&?(\w+)/.exec(body);
  if (offPeak !== null) sheet.offPeak = offPeakOf(offPeak[1] ?? "", offPeaks);
  const fast = /Fast:\s*&FastRates\{([^}]*)\}/.exec(body);
  if (fast !== null) sheet.fast = ratesOf(fast[1] ?? "");
  const priority = ptrField(body, "Priority");
  if (priority !== undefined) sheet.priority = priority;
  const geoUs = ptrField(body, "GeoUS");
  if (geoUs !== undefined) sheet.geoUs = geoUs;
  sheet.verified = verifiedOf(body);
  const note = noteOf(body, strings);
  if (note !== undefined) sheet.note = note;
  return sheet as unknown as ModelSheet;
}

/** The `Tiers` slice of a sheet body, or undefined when the sheet carries none. */
function tiersOf(body: string): { over: number; rates: Record<string, number> }[] | undefined {
  const tiers = /Tiers:\s*\[\]Tier\{/.exec(body);
  if (tiers === null) return undefined;
  // The match ends at the slice's opening brace: that brace is the container.
  const tiersBody = block(body, (tiers.index ?? 0) + tiers[0].length - 1);
  return topBlocks(tiersBody).map((tier) => ({
    over: num(/Over:\s*([\d_.]+)/.exec(tier)?.[1] ?? Number.NaN.toString()),
    rates: ratesOf((/Rates:\s*Rates\{([^}]*)\}/.exec(tier)?.[1] ?? "").toString()),
  }));
}

/** The off-peak conditions a sheet references, by the var's name. */
function offPeakOf(
  name: string,
  offPeaks: ReadonlyMap<
    string,
    { multiplier: number; peak: { days: number[]; start: number; end: number }[] }
  >,
): { multiplier: number; peak: { days: number[]; start: number; end: number }[] } {
  const value = offPeaks.get(name);
  if (value === undefined) throw new Error(`price_table.go: unknown OffPeak var ${name}`);
  return value;
}

/** A `f64Ptr(n)` field of a sheet body (Priority, GeoUS), or undefined when absent. */
function ptrField(body: string, name: "Priority" | "GeoUS"): number | undefined {
  const field =
    name === "Priority"
      ? /Priority:\s*f64Ptr\(([\d.]+)\)/.exec(body)
      : /GeoUS:\s*f64Ptr\(([\d.]+)\)/.exec(body);
  return field === null ? undefined : num(field?.[1] ?? "");
}

function verifiedOf(body: string): boolean {
  const verified = /Verified:\s*(true|false)/.exec(body);
  if (verified === null) throw new Error(`price_table.go: a sheet has no Verified: ${body.slice(0, 80)}`);
  return verified[1] === "true";
}

/** A sheet's note: the const its `strPtr(name)` names, or its joined string literals. */
function noteOf(body: string, strings: ReadonlyMap<string, string>): string | undefined {
  const note = /Note:\s*strPtr\(/.exec(body);
  if (note === null) return undefined;
  const noteBody = parenBlock(body, (note.index ?? 0) + note[0].length - 1);
  const name = /^(\w+)\s*$/.exec(noteBody.trim())?.[1];
  return name === undefined ? goString(noteBody) : (strings.get(name) ?? "");
}

/** One offering's body, in radar's shape. */
function offeringOf(
  body: string,
  strings: ReadonlyMap<string, string>,
  offPeaks: ReadonlyMap<
    string,
    { multiplier: number; peak: { days: number[]; start: number; end: number }[] }
  >,
): Offering {
  const id = /ID:\s+Offering(\w+),/.exec(body)?.[1];
  const host = /Host:\s+"([^"]+)",/.exec(body)?.[1];
  const sourceMatch = /Source:\s*([\w"]+)/.exec(body)?.[1];
  if (id === undefined || host === undefined || sourceMatch === undefined)
    throw new Error(`price_table.go: an offering is missing ID, Host or Source: ${body.slice(0, 80)}`);
  const modelsStart = body.indexOf("map[string]ModelSheet{");
  if (modelsStart < 0) throw new Error(`price_table.go: offering ${id} has no Models map`);
  const models: Record<string, ModelSheet> = {};
  const mapBody = block(body, body.indexOf("{", modelsStart));
  for (const match of mapBody.matchAll(/"((?:[^"\\])*)":\s*\{/g)) {
    const key = match[1] ?? "";
    const sheetBody = block(mapBody, (match.index ?? 0) + match[0].length - 1);
    models[key] = sheetOf(sheetBody, strings, offPeaks);
  }
  return {
    id: id.charAt(0).toLowerCase() + id.slice(1),
    host,
    source: sourceMatch.startsWith('"') ? goString(sourceMatch) : (strings.get(sourceMatch) ?? ""),
    retrieved: strings.get("PricesRetrieved") ?? "",
    models,
  } as Offering;
}

/** The Go file's price data, in radar's shape. */
function goTable(): { retrieved: string; offerings: Offering[] } {
  const source = readFileSync(GO, "utf8");
  const strings = goStrings(source);

  const retrieved = strings.get("PricesRetrieved");
  if (retrieved === undefined) throw new Error("price_table.go: no PricesRetrieved string");

  const offPeakVar = /var\s+(\w+)\s*=\s*OffPeak\{/.exec(source);
  const offPeaks = new Map<
    string,
    { multiplier: number; peak: { days: number[]; start: number; end: number }[] }
  >();
  if (offPeakVar !== null) offPeaks.set(offPeakVar[1] ?? "", offPeakVarOf(source, offPeakVar));

  const offeringsStart = source.indexOf("var Offerings = []Offering{");
  if (offeringsStart < 0) throw new Error("price_table.go: no Offerings table");
  const offerings = topBlocks(block(source, source.indexOf("{", offeringsStart))).map((body) =>
    offeringOf(body, strings, offPeaks),
  );
  return { retrieved, offerings };
}

/** The off-peak var a match names: its multiplier and its peak windows. */
function offPeakVarOf(
  source: string,
  match: RegExpExecArray,
): { multiplier: number; peak: { days: number[]; start: number; end: number }[] } {
  // The match ends at the composite's opening brace: that brace is the container.
  const body = block(source, match.index + match[0].length - 1);
  const multiplier = num(/Multiplier:\s*([\d.]+)/.exec(body)?.[1] ?? Number.NaN.toString());
  const peak = topBlocks(block(body, body.indexOf("{", body.indexOf("Peak:")))).map((window) => ({
    days: (/Days:\s*\[\]int\{([^}]*)\}/.exec(window)?.[1] ?? "")
      .split(",")
      .map((day) => num(day.trim()))
      .filter((day) => !Number.isNaN(day)),
    start: num(/Start:\s*(\d+)/.exec(window)?.[1] ?? Number.NaN.toString()),
    end: num(/End:\s*(\d+)/.exec(window)?.[1] ?? Number.NaN.toString()),
  }));
  return { multiplier, peak };
}

describe("radar's price table and the Go core's are one table", () => {
  it("holds the same offerings, rates, conditions and sources, model for model", () => {
    const go = goTable();

    expect(PRICES_RETRIEVED).toBe(go.retrieved);
    expect(OFFERINGS).toEqual(go.offerings);
  });

  it("prices the same request the same way (a tiered, off-peak, US-geo, fast, priority case each)", () => {
    const glm = priceRequest({
      offering: "zai",
      model: "glm-5.3",
      tokens: { input: 1_000_000, output: 100_000, cacheRead: 0, cacheWrite5m: 0, cacheWrite1h: 0 },
      ts: Date.parse("2026-10-08T12:00:00Z"),
    });
    expect(glm?.usd).toBeCloseTo((1_000_000 / 1e6) * 1.4 + (100_000 / 1e6) * 4.4, 12);

    // DeepSeek off peak: Wednesday 12:00 UTC is outside both peak windows, so half price.
    const offPeak = priceRequest({
      offering: "deepseek",
      model: "deepseek-flash",
      tokens: { input: 1_000_000, output: 0, cacheRead: 0, cacheWrite5m: 0, cacheWrite1h: 0 },
      ts: Date.parse("2026-10-07T12:00:00Z"),
    });
    expect(offPeak?.detail).toContain("off-peak ×0.5");
    expect(offPeak?.usd).toBeCloseTo(0.3 * 0.5, 12);

    const haikuTier = priceRequest({
      offering: "anthropic",
      model: "claude-haiku-5-5",
      tokens: { input: 200_000, output: 0, cacheRead: 0, cacheWrite5m: 0, cacheWrite1h: 0 },
      ts: Date.parse("2026-10-08T12:00:00Z"),
    });
    expect(haikuTier?.detail).toContain("tier over 100k");
    expect(haikuTier?.usd).toBeCloseTo(0.2 * 0.5, 12);

    const fast = priceRequest({
      offering: "anthropic",
      model: "claude-opus-5-5",
      tokens: { input: 1_000_000, output: 1_000_000, cacheRead: 0, cacheWrite5m: 0, cacheWrite1h: 0 },
      ts: Date.parse("2026-10-08T12:00:00Z"),
      speed: "fast",
    });
    expect(fast?.detail).toContain("fast mode");
    expect(fast?.usd).toBeCloseTo(8 + 40, 12);

    const us = priceRequest({
      offering: "anthropic",
      model: "claude-sonnet-5-5",
      tokens: { input: 1_000_000, output: 0, cacheRead: 0, cacheWrite5m: 0, cacheWrite1h: 0 },
      ts: Date.parse("2026-10-08T12:00:00Z"),
      geo: "us",
    });
    expect(us?.detail).toContain("US geo ×1.1");
    expect(us?.usd).toBeCloseTo(2 * 1.1, 12);

    // MiniMax's 1M prompt tokens land in the over-512k tier (input 0.6), and priority multiplies it.
    const priority = priceRequest({
      offering: "minimax",
      model: "MiniMax-M3",
      tokens: { input: 1_000_000, output: 0, cacheRead: 0, cacheWrite5m: 0, cacheWrite1h: 0 },
      ts: Date.parse("2026-10-08T12:00:00Z"),
      serviceTier: "priority",
    });
    expect(priority?.detail).toContain("tier over 512k");
    expect(priority?.detail).toContain("priority ×1.5");
    expect(priority?.usd).toBeCloseTo(0.6 * 1.5, 12);

    expect(
      priceRequest({
        offering: null,
        model: "made-up-model",
        tokens: { input: 1, output: 1, cacheRead: 0, cacheWrite5m: 0, cacheWrite1h: 0 },
        ts: 0,
      }),
    ).toBe(null);
  });
});
