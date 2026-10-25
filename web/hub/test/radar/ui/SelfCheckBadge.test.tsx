// SelfCheckBadge.test.tsx — the Radar header's self-check badge: its tone by drift, its hover with both
// sides' numbers, and its silence when the route cannot answer.

import { render, waitFor } from "@testing-library/preact";
import { describe, expect, it } from "vite-plus/test";
import { fetchSelfCheck, SelfCheckBadge } from "../../../src/radar/app/SelfCheckBadge.tsx";
import type { SelfCheck } from "../../../src/radar/shared/api.ts";

const counts = { requests: 3, input: 2400, output: 600, cacheRead: 5000, cacheWrite: 2000 };

function check(driftPct: number | null, over: Partial<SelfCheck> = {}): SelfCheck {
  return { day: "2026-10-25", radar: counts, transcripts: counts, driftPct, ...over };
}

async function badgeOf(answer: SelfCheck | null) {
  const { container } = render(<SelfCheckBadge read={() => Promise.resolve(answer)} />);
  await waitFor(() => expect(container.querySelector("[data-selfcheck]")).not.toBeNull());
  return container.querySelector("[data-selfcheck]") as HTMLElement;
}

describe("SelfCheckBadge", () => {
  it("is green at 1% drift or less and names both sides on hover", async () => {
    const badge = await badgeOf(check(1));
    expect(badge.dataset.tone).toBe("ok");
    expect(badge.textContent).toBe("Matches transcripts");
    expect(badge.title).toBe(
      "Self-check for 2026-10-25 (UTC). " +
        "Radar: 3 requests, 2,400 in, 600 out, 5,000 cache read, 2,000 cache write. " +
        "Transcripts: 3 requests, 2,400 in, 600 out, 5,000 cache read, 2,000 cache write.",
    );
  });

  it("is yellow above 1% and says by how much", async () => {
    const badge = await badgeOf(check(12.5, { radar: { ...counts, requests: 4 } }));
    expect(badge.dataset.tone).toBe("warn");
    expect(badge.textContent).toBe("12.5% off the transcripts");
    expect(badge.title).toContain("Radar: 4 requests");
  });

  it("is grey when no transcript has a call today", async () => {
    const badge = await badgeOf(check(null));
    expect(badge.dataset.tone).toBe("idle");
    expect(badge.textContent).toBe("No transcripts today");
  });

  it("shows nothing while the route cannot answer", async () => {
    const { container } = render(<SelfCheckBadge read={() => Promise.resolve(null)} />);
    await Promise.resolve();
    expect(container.querySelector("[data-selfcheck]")).toBeNull();
  });

  it("reads null from a refused or failed request", async () => {
    expect(await fetchSelfCheck(() => Promise.resolve(new Response("{}", { status: 503 })))).toBeNull();
    expect(await fetchSelfCheck(() => Promise.reject(new Error("offline")))).toBeNull();
    const body = check(0);
    const asked: string[] = [];
    const read = await fetchSelfCheck((input) => {
      asked.push(String(input));
      return Promise.resolve(new Response(JSON.stringify(body)));
    });
    expect(read).toEqual(body);
    expect(asked).toEqual(["/api/radar/api/selfcheck"]);
  });
});
