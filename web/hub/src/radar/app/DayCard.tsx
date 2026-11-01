/**
 * The Overview's day card: the most sessions alive at once today and the cards the boards moved
 * to done, both over the day of the hub's zone, which the title names. The hub counts them from
 * its session registry and the boards' LOG; the card reads /api/radar/api/day on mount and once
 * a minute, and shows nothing while the route cannot answer.
 */

import type { JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import { decode } from "../../api/decode.ts";
import { DayAnswer } from "../../api/schema/radar-side.ts";
import { displayZone, fmtNum } from "../fmt.ts";

const DAY_PATH = "/api/radar/api/day";
const REFRESH_MS = 60_000;

/** Reads the day; null when the route cannot answer (a standalone radar, a network error, an
 *  answer that is no day report). */
export async function fetchDay(outer: typeof fetch = fetch): Promise<DayAnswer | null> {
  try {
    const response = await outer(DAY_PATH, { headers: { Accept: "application/json" } });
    return response.ok ? decode(DayAnswer, await response.json(), DAY_PATH) : null;
  } catch {
    return null;
  }
}

function Figure({ name, word, value }: { name: string; word: string; value: number }): JSX.Element {
  return (
    <div class="min-w-0">
      <div class="text-xs text-[var(--ui-muted)]">{word}</div>
      <div class="num mt-0.5 text-xl font-semibold md:text-[26px]" data-day={name}>
        {fmtNum(value)}
      </div>
    </div>
  );
}

export function DayCard({
  read = fetchDay,
}: {
  /** The day's read, injectable so tests answer it without a server. */
  read?: () => Promise<DayAnswer | null>;
}): JSX.Element | null {
  const [day, setDay] = useState<DayAnswer | null>(null);

  useEffect(() => {
    let live = true;
    const load = (): void => {
      void read().then((answer) => {
        if (live && answer !== null) setDay(answer);
      });
    };
    load();
    const timer = setInterval(load, REFRESH_MS);
    return () => {
      live = false;
      clearInterval(timer);
    };
  }, [read]);

  if (day === null) return null;
  return (
    <section class="ui-card p-4 md:p-5" aria-labelledby="ovw-day" data-day-card="">
      <h2 id="ovw-day" class="mb-2 text-[15px] font-semibold">
        Today, {displayZone()}
      </h2>
      <div class="grid grid-cols-2 gap-3">
        <Figure name="peak-sessions" word="Peak sessions at once" value={day.peakSessions} />
        <Figure name="cards-done" word="Cards done" value={day.cardsDone} />
      </div>
    </section>
  );
}
