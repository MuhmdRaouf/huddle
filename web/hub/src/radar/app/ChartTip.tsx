/**
 * The hover tooltip every chart shares: a crosshair under the pointer and a box with the date (or
 * the bucket's time) and one line per series. The CSS pins both to the viewport, so the pointer's
 * client coordinates place them.
 */

import { useState } from "preact/hooks";
import { nearestIndex } from "../chart.ts";

/** Where the tooltip sits: the bucket's index and the pointer's box. */
export type ChartTip = { index: number; x: number; top: number; height: number };

/** One series' row in the box: its colour, its name and the value already formatted. */
export type ChartTipLine = { color: string; label: string; value: string };

/** Where the box sits: right of the pointer, or left of it when it would run off the window. */
export function tipLeft(x: number, width: number): number {
  const left = x + 14 + width > window.innerWidth ? x - 14 - width : x + 14;
  return Math.max(8, left);
}

/** The tooltip box and its crosshair, placed by the pointer's viewport coordinates. */
export function ChartTipBox({ tip, head, lines }: { tip: ChartTip; head: string; lines: ChartTipLine[] }) {
  return (
    <>
      <div
        class="chart-tip"
        style={{ display: "block", left: `${tipLeft(tip.x, 190)}px`, top: `${tip.top + 8}px` }}
      >
        <div class="tip-head">{head}</div>
        {lines.map((line) => (
          <div key={`${line.label}`} class="tip-line">
            <span class="swatch" style={{ background: line.color }} />
            <span>{line.label}</span>
            <span class="tip-value">{line.value}</span>
          </div>
        ))}
      </div>
      <div
        class="chart-cross"
        style={{ display: "block", left: `${tip.x}px`, top: `${tip.top}px`, height: `${tip.height}px` }}
      />
    </>
  );
}

/** Tracks the pointer over a chart: the nearest bucket's tip, null once the pointer leaves. */
export function useChartTip(values: number[]) {
  const [tip, setTip] = useState<ChartTip | null>(null);
  const move = (event: PointerEvent): void => {
    const svg = (event.currentTarget as HTMLElement).querySelector("svg");
    if (svg === null) return;
    const rect = svg.getBoundingClientRect();
    if (rect.width <= 0) return;
    const index = nearestIndex(values, event.clientX - rect.left, rect.width);
    setTip(index === null ? null : { index, x: event.clientX, top: rect.top, height: rect.height });
  };
  const leave = (): void => setTip(null);
  return { tip, move, leave };
}
