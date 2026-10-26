// RadarDay.tsx — the Radar card on the Overview: today's estimated cost and requests from Radar's
// usage report, the loudest open alert, and the day's peak sessions and cards done when Radar's
// day route answers. Read on mount and once a minute through the hub's /api/radar prefix. A usage
// report that cannot be read hides the card; a day route that answers 404 or 503 (an older
// Radar, or none running) hides its two numbers and nothing else.
import { Badge } from "@huddle/ui/badge.tsx";
import { Card } from "@huddle/ui/card.tsx";
import type { JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import type { Api } from "../api.ts";
import { fmtNum, fmtUsd } from "../radar/fmt.ts";
import { alertLabel } from "../radar/palette.ts";
import type { Alert } from "../radar/shared/api.ts";
import type { UsageReport } from "../radar/shared/usage.ts";
import { useHuddle } from "./context.tsx";

const USAGE_PATH = "/api/radar/api/usage?days=7";
const ALERTS_PATH = "/api/radar/api/alerts";
const DAY_PATH = "/api/radar/api/day";
const REFRESH_MS = 60_000;

/** The day's two counts as Radar's day route answers them. */
type DayCounts = { peakSessions: number; cardsDone: number };

/** What the card shows: today's money and requests, the loudest alert, and the day's counts
 *  (null when the day route cannot answer). */
export type RadarDayData = {
  costUsd: number;
  requests: number;
  worst: Alert | null;
  day: DayCounts | null;
};

/** One read through the API client; any refusal or network error reads as null. */
async function readOr<T>(api: Api, path: string): Promise<T | null> {
  try {
    return (await api.api(path)) as T;
  } catch {
    return null;
  }
}

/** The loudest alert: the first error, else the first warning, else none. */
export function worstAlert(alerts: readonly Alert[]): Alert | null {
  return alerts.find((a) => a.severity === "err") ?? alerts[0] ?? null;
}

const isCount = (v: unknown): v is number => typeof v === "number" && Number.isFinite(v);

/** Reads the three routes at once. Today is the usage report's last day; a report without days
 *  answers null, and so does the whole read. */
export async function readRadarDay(api: Api): Promise<RadarDayData | null> {
  const [usage, alerts, day] = await Promise.all([
    readOr<UsageReport>(api, USAGE_PATH),
    readOr<{ alerts?: Alert[] }>(api, ALERTS_PATH),
    readOr<Partial<DayCounts>>(api, DAY_PATH),
  ]);
  const today = Array.isArray(usage?.days) ? usage.days.at(-1) : undefined;
  if (!today) return null;
  return {
    costUsd: today.costUsd,
    requests: today.requests,
    worst: worstAlert(Array.isArray(alerts?.alerts) ? alerts.alerts : []),
    day:
      day && isCount(day.peakSessions) && isCount(day.cardsDone)
        ? { peakSessions: day.peakSessions, cardsDone: day.cardsDone }
        : null,
  };
}

/** One figure: its word over its number. `data` names the attribute the e2e suite reads. */
function Figure({
  word,
  value,
  data,
}: {
  word: string;
  value: string;
  data: Record<string, string>;
}): JSX.Element {
  return (
    <div class="flex min-w-0 flex-col gap-0.5">
      <span class="text-xs muted">{word}</span>
      <span class="truncate font-mono text-xl font-semibold tnum" {...data}>
        {value}
      </span>
    </div>
  );
}

/** The card itself, over data already read. */
export function RadarDayView({ data }: { data: RadarDayData }): JSX.Element {
  const { worst, day } = data;
  return (
    <Card label="Today in Radar">
      <div class="flex flex-col gap-3">
        <div class="grid grid-cols-2 gap-3 sm:grid-cols-4">
          <Figure
            word="Cost today"
            value={fmtUsd(data.costUsd)}
            data={{ "data-radar-cost": String(data.costUsd) }}
          />
          <Figure word="Requests" value={fmtNum(data.requests)} data={{ "data-radar-requests": "" }} />
          {day ? (
            <>
              <Figure
                word="Peak sessions"
                value={fmtNum(day.peakSessions)}
                data={{ "data-radar-peak": "" }}
              />
              <Figure word="Cards done" value={fmtNum(day.cardsDone)} data={{ "data-radar-cards": "" }} />
            </>
          ) : null}
        </div>
        <a
          href={worst ? "#/radar/alerts" : "#/radar/overview"}
          class="flex min-h-[44px] min-w-0 items-center gap-2 text-sm text-base-content"
          data-radar-alert={worst ? worst.severity : "none"}
        >
          {worst ? (
            <>
              <Badge tone={worst.severity === "err" ? "fail" : "warn"}>{alertLabel(worst.kind)}</Badge>
              <span class="min-w-0 truncate">{worst.detail}</span>
            </>
          ) : (
            <>
              <Badge tone="ok">No alerts</Badge>
              <span class="min-w-0 truncate muted">Open Radar</span>
            </>
          )}
        </a>
      </div>
    </Card>
  );
}

/** The card as the Overview mounts it: read on mount and once a minute; nothing while there is
 *  nothing to show. */
export function RadarDayCard(): JSX.Element | null {
  const { api } = useHuddle();
  const [data, setData] = useState<RadarDayData | null>(null);
  useEffect(() => {
    let alive = true;
    const read = (): void => {
      void readRadarDay(api).then((d) => {
        if (alive) setData(d);
      });
    };
    read();
    const timer = window.setInterval(read, REFRESH_MS);
    return () => {
      alive = false;
      window.clearInterval(timer);
    };
  }, [api]);
  return data ? <RadarDayView data={data} /> : null;
}
