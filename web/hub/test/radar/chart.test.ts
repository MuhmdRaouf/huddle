import { describe, expect, it } from "vite-plus/test";
import {
  agoWord,
  bucketizeRange,
  bucketizeTokenKindsRange,
  cacheHitRate,
  dayLabels,
  dayWords,
  layeredMax,
  monotonePath,
  nearestIndex,
  spanWord,
  windowLabels,
} from "../../src/radar/chart.ts";
import { type RequestRecord, ZERO_TOKENS } from "../../src/radar/shared/model.ts";
import { makeRequest } from "./helpers.ts";

const NOW = 300_000; // a 5m window that starts at ts 0

function req(ts: number, output = 0): RequestRecord {
  return makeRequest({ ts, tokens: { ...ZERO_TOKENS, output } });
}

/** Every y a path touches: the anchors it moves through and each cubic segment sampled inside,
 *  so a curve that only dips between the points still shows up. */
function ysOf(d: string): number[] {
  const out: number[] = [];
  let at: { x: number; y: number } | null = null;
  const num = "-?\\d+(?:\\.\\d+)?";
  const move = new RegExp(`M (${num}) (${num})`).exec(d);
  if (move !== null) {
    at = { x: Number(move[1]), y: Number(move[2]) };
    out.push(at.y);
  }
  const line = new RegExp(`L (${num}) (${num})`).exec(d);
  if (line !== null) out.push(Number(line[2]));
  const cubic = new RegExp(`C (${num}) (${num}), (${num}) (${num}), (${num}) (${num})`, "g");
  for (const seg of d.matchAll(cubic)) {
    const n = seg.map(Number);
    const c1y = n[2] ?? 0;
    const c2y = n[4] ?? 0;
    const y = n[6] ?? 0;
    const start = at ?? { x: n[1] ?? 0, y: c1y };
    for (const t of [0.25, 0.5, 0.75]) {
      const u = 1 - t;
      out.push(u * u * u * start.y + 3 * u * u * t * c1y + 3 * u * t * t * c2y + t * t * t * y);
    }
    at = { x: n[5] ?? 0, y };
    out.push(y);
  }
  return out;
}

/** The points a chart draws for values over a plot: the same scaleY the svgs use. */
function pointsOf(values: number[], height = 100, top = 4): { x: number; y: number }[] {
  const max = Math.max(...values, 1);
  return values.map((value, index) => ({
    x: index * 50,
    y: height - (value / max) * (height - top),
  }));
}

describe("monotonePath", () => {
  it("degrades to nothing, a single move, or a straight line for tiny series", () => {
    expect(monotonePath([])).toBe("");
    expect(monotonePath([{ x: 10, y: 20 }])).toBe("M 10.0 20.0");
    expect(
      monotonePath([
        { x: 0, y: 10 },
        { x: 30, y: 4 },
      ]),
    ).toBe("M 0.0 10.0 L 30.0 4.0");
  });

  it("keeps a monotone series' curve inside the data's min and max", () => {
    const points = pointsOf([0, 2, 5, 9, 14, 22, 30]);
    const ys = ysOf(monotonePath(points));
    const lo = Math.min(...points.map((p) => p.y));
    const hi = Math.max(...points.map((p) => p.y));
    for (const y of ys) {
      expect(y).toBeGreaterThanOrEqual(lo - 0.01);
      expect(y).toBeLessThanOrEqual(hi + 0.01);
    }
  });

  it("never dips below the x axis when a quiet series jumps, so the line cannot overshoot", () => {
    // the same points the usage chart draws: [0, 0, 0, 0, 100] over a plot of height 100
    const points = pointsOf([0, 0, 0, 0, 100]);
    const baseline = 100;
    const ys = ysOf(monotonePath(points));
    expect(ys.length).toBeGreaterThan(0);
    for (const y of ys) {
      expect(y).toBeLessThanOrEqual(baseline + 0.01);
      expect(y).toBeGreaterThanOrEqual(Math.min(...points.map((p) => p.y)) - 0.01);
    }
  });

  it("holds a spike inside its own value, neither bulging past it nor below the flat sides", () => {
    const points = pointsOf([0, 0, 100, 0, 0]);
    const ys = ysOf(monotonePath(points));
    const lo = Math.min(...points.map((p) => p.y));
    const hi = Math.max(...points.map((p) => p.y));
    for (const y of ys) {
      expect(y).toBeGreaterThanOrEqual(lo - 0.01);
      expect(y).toBeLessThanOrEqual(hi + 0.01);
    }
  });

  it("draws a flat series as the straight line along it", () => {
    const d = monotonePath(pointsOf([0, 0, 0, 0]));
    const ys = ysOf(d);
    for (const y of ys) expect(y).toBeCloseTo(100, 1);
  });
});

describe("bucketizeTokenKindsRange", () => {
  it("splits tokens into the four kind series over the explicit window", () => {
    const tokens = { input: 1, output: 2, cacheRead: 3, cacheWrite: 4 };
    const series = bucketizeTokenKindsRange(
      [makeRequest({ ts: 0, tokens }), makeRequest({ ts: 10, tokens })],
      0,
      NOW,
    );
    expect(series).toHaveLength(4);
    expect(series.map((values) => values[0])).toEqual([2, 4, 6, 8]);
    expect(series.every((values) => values.length === 60)).toBe(true);
  });
});

describe("bucketizeRange", () => {
  it("counts per bucket over an explicit window and clamps its right edge into the last bucket", () => {
    const values = bucketizeRange([req(0), req(7_499), req(299_999), req(NOW)], 0, NOW, 60);
    expect(values).toHaveLength(60);
    expect(values[0]).toBe(1);
    expect(values[1]).toBe(1); // 7_499 sits in the second 5s bucket
    expect(values[59]).toBe(2); // 299_999 and the clamped edge both land in the last bucket
    expect(values.slice(2, 59).every((value) => value === 0)).toBe(true);
  });

  it("drops requests outside [from, to] entirely", () => {
    const values = bucketizeRange([req(-1), req(NOW + 1)], 0, NOW, 60);
    expect(values.reduce((sum, value) => sum + value, 0)).toBe(0);
  });

  it("sums a picked metric over any bucket count", () => {
    const counted = bucketizeRange([req(0, 4), req(1_000, 6)], 0, NOW, 60, (r) => r.tokens.output);
    expect(counted[0]).toBe(10);
    const halved = bucketizeRange([req(100_000), req(200_000)], 0, NOW, 2);
    expect(halved).toEqual([1, 1]);
  });

  it("reads a degenerate window as all zeros instead of dividing by zero", () => {
    expect(bucketizeRange([req(5)], 5, 5, 3)).toEqual([0, 0, 0]);
  });
});

describe("bucketizeTokenKindsRange", () => {
  it("splits tokens into the four kind series over the explicit window", () => {
    const tokens = { input: 1, output: 2, cacheRead: 3, cacheWrite: 4 };
    const series = bucketizeTokenKindsRange(
      [makeRequest({ ts: 0, tokens }), makeRequest({ ts: 10, tokens })],
      0,
      NOW,
    );
    expect(series).toHaveLength(4);
    expect(series.map((values) => values[0])).toEqual([2, 4, 6, 8]);
    expect(series.every((values) => values.length === 60)).toBe(true);
  });
});

describe("nearestIndex", () => {
  it("maps a pointer x to its bucket, null outside", () => {
    expect(nearestIndex([], 10, 200)).toBeNull();
    expect(nearestIndex([1, 2, 3, 4], 10, 0)).toBeNull();
    expect(nearestIndex([1, 2, 3, 4], 0, 200)).toBe(0);
    expect(nearestIndex([1, 2, 3, 4], 49, 200)).toBe(0);
    expect(nearestIndex([1, 2, 3, 4], 50, 200)).toBe(1);
    expect(nearestIndex([1, 2, 3, 4], 199, 200)).toBe(3);
    expect(nearestIndex([1, 2, 3, 4], 200, 200)).toBeNull();
    expect(nearestIndex([1, 2, 3, 4], -1, 200)).toBeNull();
  });
});

/* ------------------------------- window helpers ------------------------------ */

describe("spanWord", () => {
  it("reads a span in compact words, rounding to the unit", () => {
    expect(spanWord(45_000)).toBe("45 s");
    expect(spanWord(300_000)).toBe("5 min");
    expect(spanWord(10_800_000)).toBe("3 h");
    expect(spanWord(604_800_000)).toBe("7 d");
  });

  it("never rounds a short span below one second", () => {
    expect(spanWord(400)).toBe("1 s");
  });
});

describe("agoWord", () => {
  it("uses minutes under the hour, hours under two days, days beyond", () => {
    expect(agoWord(300_000)).toBe("5 min");
    expect(agoWord(3_600_000)).toBe("1 h");
    expect(agoWord(86_400_000)).toBe("24 h");
    expect(agoWord(3 * 86_400_000)).toBe("3 d");
  });
});

describe("windowLabels", () => {
  it("marks an open-ended window by how far back it reaches, ending at now", () => {
    expect(windowLabels(NOW - 300_000, NOW, NOW).map((label) => label.text)).toEqual(["5 min ago", "now"]);
  });

  it("adds the midpoint to long spans", () => {
    const marks = windowLabels(NOW - 86_400_000, NOW, NOW);
    expect(marks.map((label) => label.text)).toEqual(["24 h ago", "12 h", "now"]);
    expect(marks.map((label) => label.at)).toEqual([0, 0.5, 1]);
  });

  it("stamps a fixed window's ends and its midpoint", () => {
    const year = new Date(NOW).getFullYear();
    const at = (month: number, day: number, hour: number, minute: number): number =>
      new Date(year, month, day, hour, minute).getTime();
    const from = at(9, 6, 14, 0);
    const to = at(9, 6, 20, 0);
    expect(windowLabels(from, to, to + 60_000).map((label) => label.text)).toEqual([
      "6 Oct 14:00",
      "6 Oct 17:00",
      "6 Oct 20:00",
    ]);
  });

  it("draws nothing for a degenerate window", () => {
    expect(windowLabels(NOW, NOW, NOW)).toEqual([]);
  });
});

describe("cacheHitRate", () => {
  it("computes the cache hit rate over everything the model read", () => {
    expect(cacheHitRate({ input: 0, cacheRead: 0, cacheWrite: 0 })).toBeNull();
    expect(cacheHitRate({ input: 10, cacheRead: 80, cacheWrite: 10 })).toBe(0.8);
  });
});

describe("dayLabels", () => {
  it("marks the first, middle and last day of a span as '17 Oct'", () => {
    const dates = [
      "2026-10-11",
      "2026-10-12",
      "2026-10-13",
      "2026-10-14",
      "2026-10-15",
      "2026-10-16",
      "2026-10-17",
    ];
    expect(dayLabels(dates)).toEqual([
      { text: "11 Oct", at: 0 },
      { text: "14 Oct", at: 0.5 },
      { text: "17 Oct", at: 1 },
    ]);
  });

  it("marks both ends of a two-day span, the one day it has, and nothing for none", () => {
    expect(dayLabels(["2026-10-11", "2026-10-12"])).toEqual([
      { text: "11 Oct", at: 0 },
      { text: "12 Oct", at: 1 },
    ]);
    expect(dayLabels(["2026-10-11"])).toEqual([{ text: "11 Oct", at: 0 }]);
    expect(dayLabels([])).toEqual([]);
  });
});

describe("layeredMax", () => {
  it("answers the tallest value across every series, never under 1", () => {
    expect(
      layeredMax([
        [0, 0],
        [0, 5, 3],
      ]),
    ).toBe(5);
    expect(layeredMax([[0], [0]])).toBe(1);
    expect(layeredMax([])).toBe(1);
  });
});

describe("dayWords", () => {
  it("reads nothing for a day that does not spell a date, and still names the day of a bad month", () => {
    expect(dayWords(undefined)).toBe("");
    expect(dayWords("")).toBe("");
    expect(dayWords("2026-99-17")).toBe("17");
    expect(dayWords("garbage")).toBe("");
  });
});
