/**
 * The self-check Settings › Hub and Radar shows: today's (UTC) Radar totals against the same day
 * re-summed from the Claude Code transcripts. The badge is green while the worst counter drifts 1%
 * or less, yellow above, grey when the transcripts hold no call today; the sentence under it names
 * both sides' numbers. Read on mount and once a minute, the server's own cache window.
 */

import type { JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import type { StatusTone } from "../palette.ts";
import type { SelfCheck, SelfCheckCounts } from "../shared/api.ts";
import { Badge } from "./kit.tsx";

/** The drift a badge still reads as a match, in percent. */
export const DRIFT_OK_PCT = 1;

/** The hub's address of radar's self-check route. */
export const SELFCHECK_PATH = "/api/radar/api/selfcheck";
const REFRESH_MS = 60_000;

const COUNTERS = ["requests", "input", "output", "cacheRead", "cacheWrite"] as const;

/** One side's counters, checked at the edge. */
function isCounts(value: unknown): value is SelfCheckCounts {
  if (typeof value !== "object" || value === null) return false;
  const side = value as Record<string, unknown>;
  return COUNTERS.every((key) => typeof side[key] === "number");
}

/** The self-check an answer holds, or null when it is not one. */
export function selfCheckOf(answer: unknown): SelfCheck | null {
  if (typeof answer !== "object" || answer === null) return null;
  const a = answer as Record<string, unknown>;
  const drift = a.driftPct === null || typeof a.driftPct === "number";
  return typeof a.day === "string" && drift && isCounts(a.radar) && isCounts(a.transcripts)
    ? (answer as SelfCheck)
    : null;
}

/** Reads the self-check; null when the route cannot answer (no history, a network error). */
export async function fetchSelfCheck(outer: typeof fetch = fetch): Promise<SelfCheck | null> {
  try {
    const response = await outer(SELFCHECK_PATH, { headers: { Accept: "application/json" } });
    return response.ok ? selfCheckOf(await response.json()) : null;
  } catch {
    return null;
  }
}

const count = (value: number): string => value.toLocaleString("en-US");

/** One side's numbers in words, for the sentence. */
function countsText(counts: SelfCheckCounts): string {
  return (
    `${count(counts.requests)} requests, ${count(counts.input)} in, ${count(counts.output)} out, ` +
    `${count(counts.cacheRead)} cache read, ${count(counts.cacheWrite)} cache write`
  );
}

/** The badge's tone and word for one answer. */
export function selfCheckLook(check: SelfCheck): { tone: StatusTone; text: string } {
  if (check.driftPct === null) return { tone: "idle", text: "No transcripts today" };
  if (check.driftPct <= DRIFT_OK_PCT) return { tone: "ok", text: "Matches transcripts" };
  return { tone: "warn", text: `${check.driftPct.toFixed(1)}% off the transcripts` };
}

export function SelfCheckBadge({
  read = fetchSelfCheck,
}: {
  /** The self-check read, injectable so tests answer it without a server. */
  read?: () => Promise<SelfCheck | null>;
}): JSX.Element | null {
  const [check, setCheck] = useState<SelfCheck | null>(null);

  useEffect(() => {
    let live = true;
    const load = (): void => {
      void read().then((answer) => {
        if (live && answer !== null) setCheck(answer);
      });
    };
    load();
    const timer = setInterval(load, REFRESH_MS);
    return () => {
      live = false;
      clearInterval(timer);
    };
  }, [read]);

  if (check === null) return null;
  const { tone, text } = selfCheckLook(check);
  const sentence =
    `Self-check for ${check.day} (UTC). Radar: ${countsText(check.radar)}. ` +
    `Transcripts: ${countsText(check.transcripts)}.`;
  return (
    <div data-selfcheck="" data-tone={tone} class="flex flex-col items-start gap-2">
      <Badge tone={tone} text={text} />
      <p class="m-0 text-sm text-[var(--ui-text)]">{sentence}</p>
    </div>
  );
}
