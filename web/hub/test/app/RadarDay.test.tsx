// RadarDay.test.tsx — the Radar card on the Overview: today's cost and requests from the usage
// report's last day, the loudest alert, the day route's two counts when it answers and nothing
// of them on a 404 or a 503, no card at all when the usage report cannot be read, and the
// once-a-minute re-read.
import { act, cleanup, screen } from "@testing-library/preact";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { Api, ApiError } from "../../src/api.ts";
import { RadarDayCard, readRadarDay, worstAlert } from "../../src/app/RadarDay.tsx";
import type { Alert } from "../../src/radar/shared/api.ts";
import { flush, makeCtx, makeState, renderIn } from "../helpers.tsx";

const refusal = (status: number): ApiError => Object.assign(new Error("refused"), { status }) as ApiError;

const alert = (over: Partial<Alert>): Alert => ({
  id: "a",
  kind: "stuck",
  sessionId: "s-1",
  agentId: null,
  project: "huddle",
  since: 0,
  detail: "quiet for 12 min",
  costUsd: null,
  severity: "warn",
  ...over,
});

const usage = {
  days: [
    { date: "2026-10-24", requests: 9, costUsd: 1.5 },
    { date: "2026-10-25", requests: 3, costUsd: 0.0073 },
  ],
};

/** An API whose answers are a function of the path; a thrown Error is a refusal. */
function apiOf(answer: (path: string) => unknown): Api & { paths: string[] } {
  const paths: string[] = [];
  return {
    paths,
    api: (path) => {
      paths.push(path);
      const r = answer(path);
      return r instanceof Error ? Promise.reject(r) : Promise.resolve(r);
    },
    op: () => Promise.resolve({}),
    channelPath: (ch, p) => `/api/c/${ch}${p}`,
    channelHref: (ch, p) => `#/channels/${ch}${p}`,
  };
}

/** The three routes answering: the usage report, the alerts, and the day route (or its refusal). */
const routes =
  (day: unknown, alerts: Alert[] = []) =>
  (path: string): unknown => {
    if (path.startsWith("/api/radar/api/usage")) return usage;
    if (path === "/api/radar/api/alerts") return { alerts };
    if (path === "/api/radar/api/day") return day;
    return refusal(404);
  };

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

describe("the Radar card", () => {
  it("reads today as the usage report's last day, with the day route's counts", async () => {
    const data = await readRadarDay(apiOf(routes({ peakSessions: 4, cardsDone: 7 })));
    expect(data).toEqual({
      costUsd: 0.0073,
      requests: 3,
      worst: null,
      day: { peakSessions: 4, cardsDone: 7 },
    });
  });

  it("hides the day's counts when the day route answers 404 or 503, or a shape it cannot read", async () => {
    for (const day of [refusal(404), refusal(503), { peakSessions: "4" }]) {
      const data = await readRadarDay(apiOf(routes(day)));
      expect(data?.day).toBeNull();
      expect(data?.costUsd).toBe(0.0073);
    }
  });

  it("answers nothing when the usage report cannot be read", async () => {
    expect(await readRadarDay(apiOf(() => refusal(503)))).toBeNull();
    expect(await readRadarDay(apiOf(() => ({})))).toBeNull();
  });

  it("names the loudest alert: an error before any warning", () => {
    const warn = alert({ id: "w" });
    const err = alert({ id: "e", severity: "err" });
    expect(worstAlert([warn, err])).toBe(err);
    expect(worstAlert([warn])).toBe(warn);
    expect(worstAlert([])).toBeNull();
  });

  it("shows the cost, the requests, the counts and the alert, and re-reads once a minute", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    const api = apiOf(
      routes({ peakSessions: 4, cardsDone: 7 }, [
        alert({ kind: "budget", detail: "day budget at 92%", severity: "err" }),
      ]),
    );
    renderIn(<RadarDayCard />, makeCtx(makeState(), { api }));
    await act(async () => {
      await flush();
    });
    const card = screen.getByRole("region", { name: "Today in Radar" });
    expect(card.querySelector("[data-radar-cost]")?.textContent).toBe("$0.0073");
    expect(card.querySelector("[data-radar-requests]")?.textContent).toBe("3");
    expect(card.querySelector("[data-radar-peak]")?.textContent).toBe("4");
    expect(card.querySelector("[data-radar-cards]")?.textContent).toBe("7");
    expect(screen.getByText("day budget at 92%")).toBeDefined();
    expect(card.querySelector("[data-radar-alert]")?.getAttribute("href")).toBe("#/radar/alerts");
    const reads = api.paths.length;
    await act(async () => {
      vi.advanceTimersByTime(60_000);
      await flush();
    });
    expect(api.paths.length).toBe(reads + 3);
  });

  it("says no alerts and leaves the counts out when the day route is missing", async () => {
    renderIn(<RadarDayCard />, makeCtx(makeState(), { api: apiOf(routes(refusal(404))) }));
    await act(async () => {
      await flush();
    });
    expect(screen.getByText("No alerts")).toBeDefined();
    expect(document.querySelector("[data-radar-peak]")).toBeNull();
    expect(document.querySelector("[data-radar-cards]")).toBeNull();
  });

  it("draws nothing while Radar has no usage to show", async () => {
    const view = renderIn(<RadarDayCard />, makeCtx(makeState(), { api: apiOf(() => refusal(503)) }));
    await act(async () => {
      await flush();
    });
    expect(view.container.firstElementChild).toBeNull();
  });
});
