// SettingsTab.test.tsx — the Settings tab's section row and the Radar section it serves: the
// address's section is current, an unknown one reads as the first, and the section loads radar's
// settings the first time it shows.

import { fireEvent } from "@testing-library/preact";
import { afterEach, describe, expect, it } from "vite-plus/test";
import { SettingsTab, settingsSectionOf } from "../../../src/app/SettingsTab.tsx";
import { ensureRadar, stopRadar } from "../../../src/radar/mount.tsx";
import { makeCtx, makeState, renderIn } from "../../helpers.tsx";
import { ok, radarIo, settleRadar } from "./io.ts";

afterEach(() => {
  stopRadar();
  document.body.textContent = "";
});

describe("the Settings tab", () => {
  it("serves Sign-in as the first section and reads any other address as it", () => {
    expect(settingsSectionOf(undefined)).toBe("signin");
    expect(settingsSectionOf("radar")).toBe("radar");
    expect(settingsSectionOf("somewhere-else")).toBe("signin");
  });

  it("renders the section row with Radar current, and the link moves between sections", () => {
    const { container } = renderIn(<SettingsTab section="radar" />);
    const row = container.querySelector('[aria-label="Settings sections"]');
    expect(row?.querySelector('a[data-section="radar"]')?.getAttribute("aria-current")).toBe("page");
    fireEvent.click(row?.querySelector('a[data-section="radar"]') as HTMLElement);
    expect(location.hash).toBe("#/settings/radar");
  });

  it("loads radar's settings the first time the section shows, and draws its panes", async () => {
    const world = radarIo({
      "/api/settings": ok({
        settings: { notifications: true, historyRetentionDays: 21, budgetDefaults: null },
      }),
      "/api/budgets": ok({
        budgets: [{ id: "b1", scope: "provider:zai", period: "month", limitUsd: 10, action: "warn" }],
        providers: ["zai", "kimi"],
      }),
    });
    ensureRadar(world.io);
    const { container } = renderIn(<SettingsTab section="radar" />, makeCtx(makeState()));
    await settleRadar();
    const requested = world.calls.filter((path) => path === "/api/settings").length;
    expect(requested).toBeGreaterThan(0);
    expect(container.textContent).toContain("Budgets");
  });
});
