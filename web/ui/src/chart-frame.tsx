// chart-frame.tsx — the frame every chart sits in: a title, a headline value in the mono face and a
// muted note above the chart, which is one image to a screen reader, named by what it shows.

import type { ComponentChildren, JSX } from "preact";
import { useId } from "preact/hooks";

/** What a chart frame shows around its chart. */
export type ChartFrameProps = {
  title: string;
  /** The headline number, already formatted. */
  value?: string | undefined;
  /** The range or unit, muted. */
  meta?: string | undefined;
  /** What the chart shows, in one plain sentence, for screen readers. */
  label: string;
  children: ComponentChildren;
};

/** A figure: the caption over the chart. */
export function ChartFrame({ title, value, meta, label, children }: ChartFrameProps): JSX.Element {
  const id = useId();
  return (
    <figure class="ui-chart" aria-labelledby={id}>
      <figcaption id={id} class="ui-chart-head">
        <span class="ui-chart-title">{title}</span>
        {value !== undefined ? <span class="ui-chart-value ui-mono">{` ${value}`}</span> : null}
        {meta !== undefined ? <span class="ui-chart-meta">{` ${meta}`}</span> : null}
      </figcaption>
      <div class="ui-chart-body" role="img" aria-label={label}>
        {children}
      </div>
    </figure>
  );
}
