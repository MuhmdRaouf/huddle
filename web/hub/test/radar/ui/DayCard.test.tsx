// DayCard.test.tsx — the Overview's day card: the peak sessions and cards done the day route
// answers, nothing while it cannot answer, and the one path it reads.

import { waitFor } from "@testing-library/preact";
import { describe, expect, it } from "vite-plus/test";
import { DayCard, type DayReport, fetchDay } from "../../../src/radar/app/DayCard.tsx";
import { legacyClasses, makeCtx, makeState, renderIn } from "../../helpers.tsx";

async function card(answer: DayReport | null) {
  const view = renderIn(<DayCard read={() => Promise.resolve(answer)} />, makeCtx(makeState()));
  if (answer !== null)
    await waitFor(() => expect(view.container.querySelector("[data-day-card]")).not.toBeNull());
  return view.container;
}

describe("DayCard", () => {
  it("shows the peak sessions and the cards done", async () => {
    const root = await card({ peakSessions: 3, cardsDone: 2 });
    expect(root.querySelector("[data-day='peak-sessions']")?.textContent).toBe("3");
    expect(root.querySelector("[data-day='cards-done']")?.textContent).toBe("2");
    expect(root.querySelector("[data-day-card]")?.textContent).toContain("Today, UTC");
  });

  it("paints on the token card surface", async () => {
    const root = await card({ peakSessions: 3, cardsDone: 2 });
    expect(root.querySelector("[data-day-card]")?.classList.contains("ui-card")).toBe(true);
    expect(legacyClasses(root)).toEqual([]);
  });

  it("shows nothing while the route cannot answer", async () => {
    const root = await card(null);
    await Promise.resolve();
    expect(root.querySelector("[data-day-card]")).toBeNull();
  });

  it("reads the day route and answers null when it fails", async () => {
    const asked: string[] = [];
    const read = await fetchDay((input) => {
      asked.push(typeof input === "string" ? input : input instanceof URL ? input.href : input.url);
      return Promise.resolve(new Response(JSON.stringify({ peakSessions: 4, cardsDone: 1 })));
    });
    expect(read).toEqual({ peakSessions: 4, cardsDone: 1 });
    expect(asked).toEqual(["/api/radar/api/day"]);
    expect(await fetchDay(() => Promise.resolve(new Response("{}", { status: 503 })))).toBeNull();
    expect(await fetchDay(() => Promise.reject(new Error("offline")))).toBeNull();
  });
});
