/**
 * Chart geometry as pure math, shared by the Preact charts. Series go through monotone cubic
 * interpolation (Fritsch-Carlson, the curve d3's curveMonotoneX draws) so the line is smooth and
 * never overshoots past a neighbouring value or under the x axis. No external chart library —
 * everything here is plain arithmetic; app/Chart.tsx turns it into svg.
 */

import { TOKEN_KINDS } from "./palette.ts";
import type { RequestRecord, Tokens } from "./shared/model.ts";
import { MONTHS, stampOf } from "./shared/time-range.ts";

export type Point = { x: number; y: number };

/** The same count over an explicit window: index 0 is `from`, the window's right edge
 *  clamps into the last bucket, and a degenerate window reads as all zeros. */
export function bucketizeRange(
  requests: RequestRecord[],
  from: number,
  to: number,
  buckets = 60,
  pick: (r: RequestRecord) => number = () => 1,
): number[] {
  const values = Array.from({ length: buckets }, () => 0);
  const width = (to - from) / buckets;
  if (!(width > 0)) return values;
  for (const request of requests) {
    if (request.ts < from || request.ts > to) continue;
    const index = Math.min(buckets - 1, Math.floor((request.ts - from) / width));
    values[index] = (values[index] ?? 0) + pick(request);
  }
  return values;
}

/** Per-bucket token kinds over an explicit window, in TOKEN_KINDS order (input, output, cache r/w). */
export function bucketizeTokenKindsRange(
  requests: RequestRecord[],
  from: number,
  to: number,
  buckets = 60,
): number[][] {
  return TOKEN_KINDS.map((kind) =>
    bucketizeRange(requests, from, to, buckets, (r) => r.tokens[kind.key as keyof Tokens] ?? 0),
  );
}

/** Share of prompt tokens served from cache: cache reads over everything the model read. */
export function cacheHitRate(tokens: {
  input: number;
  cacheRead: number;
  cacheWrite: number;
}): number | null {
  const read = tokens.input + tokens.cacheRead + tokens.cacheWrite;
  return read === 0 ? null : tokens.cacheRead / read;
}

/** A span in compact words: "45 s", "5 min", "3 h", "7 d" — the axis and tooltip's time grammar. */
export function spanWord(ms: number): string {
  if (ms < 60_000) return `${Math.max(1, Math.round(ms / 1000))} s`;
  if (ms < 3_600_000) return `${Math.round(ms / 60_000)} min`;
  if (ms < 86_400_000) return `${Math.round(ms / 3_600_000)} h`;
  return `${Math.round(ms / 86_400_000)} d`;
}

/** How far back an open-ended window reaches, in words: minutes under the hour, hours under two days
 *  ("24 h" reads better than "1 d"), days beyond. */
export function agoWord(ms: number): string {
  if (ms < 3_600_000 || ms >= 48 * 3_600_000) return spanWord(ms);
  return `${Math.round(ms / 3_600_000)} h`;
}

/** The x-axis labels a chart carries for its resolved window: how far back (or the start's stamp) at the left
 *  edge, the end (or "now") at the right, and the midpoint once the span is long enough to read it. */
export function windowLabels(from: number, to: number, now: number): ChartLabel[] {
  const span = to - from;
  if (!(span > 0)) return [];
  const endsNow = to >= now;
  const marks: ChartLabel[] = [
    { text: endsNow ? `${agoWord(span)} ago` : stampOf(from, new Date(now).getFullYear()), at: 0 },
    { text: endsNow ? "now" : stampOf(to, new Date(now).getFullYear()), at: 1 },
  ];
  if (span >= 6 * 3_600_000) {
    const middle = endsNow ? agoWord(span / 2) : stampOf(from + span / 2, new Date(now).getFullYear());
    marks.splice(1, 0, { text: middle, at: 0.5 });
  }
  return marks;
}

/** Vertical position of a value: 3px headroom at the top, the baseline at the bottom edge. */

/**
 * Monotone cubic interpolation (Fritsch-Carlson): one cubic Bézier per neighbouring pair, with
 * tangents chosen so a monotone series stays monotone and a local extreme flattens out instead of
 * bulging past it. The curve never leaves the data's y range, so it cannot dip under the x axis.
 */
export function monotonePath(points: Point[]): string {
  const n = points.length;
  if (n === 0) return "";
  const first = points[0] as Point;
  if (n === 1) return `M ${first.x.toFixed(1)} ${first.y.toFixed(1)}`;
  const last = points[n - 1] as Point;
  if (n === 2)
    return `M ${first.x.toFixed(1)} ${first.y.toFixed(1)} L ${last.x.toFixed(1)} ${last.y.toFixed(1)}`;

  const slopes: number[] = [];
  for (let i = 0; i < n - 1; i += 1) {
    const a = points[i] as Point;
    const b = points[i + 1] as Point;
    slopes.push(b.x === a.x ? 0 : (b.y - a.y) / (b.x - a.x));
  }
  // the tangent at each point: the one-sided slope at the ends, zero at a turn, and the
  // weighted harmonic mean of the two slopes elsewhere (the Fritsch-Carlson weighting)
  const tangents: number[] = [slopes[0] as number];
  for (let i = 1; i < n - 1; i += 1) {
    const before = slopes[i - 1] as number;
    const after = slopes[i] as number;
    if (before * after <= 0) {
      tangents.push(0);
      continue;
    }
    const w1 =
      2 * ((points[i + 1] as Point).x - (points[i] as Point).x) +
      ((points[i] as Point).x - (points[i - 1] as Point).x);
    const w2 =
      (points[i + 1] as Point).x -
      (points[i] as Point).x +
      2 * ((points[i] as Point).x - (points[i - 1] as Point).x);
    tangents.push((w1 + w2) / (w1 / before + w2 / after));
  }
  tangents.push(slopes[n - 2] as number);

  let d = `M ${first.x.toFixed(1)} ${first.y.toFixed(1)}`;
  for (let i = 0; i < n - 1; i += 1) {
    const a = points[i] as Point;
    const b = points[i + 1] as Point;
    const h = b.x - a.x;
    const c1y = a.y + ((tangents[i] as number) * h) / 3;
    const c2y = b.y - ((tangents[i + 1] as number) * h) / 3;
    d += ` C ${(a.x + h / 3).toFixed(1)} ${c1y.toFixed(1)}, ${(b.x - h / 3).toFixed(1)} ${c2y.toFixed(1)}, ${b.x.toFixed(1)} ${b.y.toFixed(1)}`;
  }
  return d;
}

export type ChartLabel = { text: string; at: number };

export type Frame = {
  width: number;
  height: number;
  /** x-axis labels pinned to fractions of the width (0 = left edge, 1 = right edge) */
  xLabels?: ChartLabel[];
  /** y-axis top label (the max); hidden when null */
  yLabel?: string | null;
};

export type ChartOptions = Frame & {
  values: number[];
  color: string;
  /** unique id for the gradient definition (two charts on a page need different ids) */
  gradientId: string;
};

export type StackedOptions = Frame & {
  /** bottom-to-top series; a band counts as 0 wherever it has no value */
  series: { values: number[]; color: string }[];
};

export type LayeredOptions = Frame & {
  /** independent series, each drawn on its own values over one shared scale */
  series: { values: number[]; color: string }[];
  /** the picked point's index, drawn as a dashed reference line; null or out of range draws none */
  mark?: number | null;
};

/** Vertical position of a value: 3px headroom at the top, the baseline at the bottom edge. */
export function scaleY(value: number, max: number, height: number): number {
  return height - (value / max) * (height - 4);
}

/** The tallest value across every series, at least 1, so an all-zero window still draws a scale. */
export function layeredMax(series: number[][]): number {
  let max = 1;
  for (const values of series) {
    for (const value of values) {
      if (value > max) max = value;
    }
  }
  return max;
}

/** "17 Oct" out of "2026-10-17", by hand, so the label stays locale-stable; anything that does not
 *  spell a date reads as nothing. */
export function dayWords(date: string | undefined): string {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(date ?? "");
  if (match === null) return "";
  return `${match[3]} ${MONTHS[Number(match[2]) - 1] ?? ""}`.trim();
}

/** The x labels a per-day chart carries: the first, middle and last day, pinned to the edges and the middle. */
export function dayLabels(dates: string[]): ChartLabel[] {
  if (dates.length === 0) return [];
  const first = { text: dayWords(dates[0]), at: 0 };
  const last = { text: dayWords(dates.at(-1)), at: 1 };
  if (dates.length === 1) return [first];
  if (dates.length === 2) return [first, last];
  const middle = dates[Math.floor((dates.length - 1) / 2)];
  return [first, { text: dayWords(middle), at: 0.5 }, last];
}

/** A path that walks `points` backwards, continuing the current subpath (its leading M becomes an L). */
export function reversedContinuation(points: Point[]): string {
  const path = monotonePath([...points].reverse());
  return path === "" ? "" : `L${path.slice(1)}`;
}

/** Index of the bucket a pointer x falls in, or null when outside. */
export function nearestIndex(values: number[], x: number, width: number): number | null {
  if (values.length === 0 || width <= 0) return null;
  const step = width / values.length;
  const index = Math.floor(x / step);
  if (index < 0 || index >= values.length) return null;
  return index;
}
