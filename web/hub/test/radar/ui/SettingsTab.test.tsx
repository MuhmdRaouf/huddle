// SettingsTab.test.tsx — the Settings tab's section row and the Radar settings its Spending group
// serves: the address's group is current, Radar's own address opens that group, an unknown
// one reads as the first, and the group loads radar's settings the first time it shows.

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
  it("serves Appearance as the first group, Radar under Spending, and reads any other address as the first", () => {
    expect(settingsSectionOf(undefined)).toBe("appearance");
    expect(settingsSectionOf("radar")).toBe("spending");
    expect(settingsSectionOf("somewhere-else")).toBe("appearance");
  });

  it("renders the section row with Spending current, and the link moves between groups", () => {
    const { container } = renderIn(<SettingsTab section="spending" />);
    const row = container.querySelector('[aria-label="Settings sections"]');
    expect(row?.querySelector('a[data-section="spending"]')?.getAttribute("aria-current")).toBe("page");
    fireEvent.click(row?.querySelector('a[data-section="security"]') as HTMLElement);
    expect(location.hash).toBe("#/settings/security");
  });

  it("loads radar's settings the first time the group shows, and draws its panes", async () => {
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
    const { container } = renderIn(<SettingsTab section="spending" />, makeCtx(makeState()));
    await settleRadar();
    const requested = world.calls.filter((path) => path === "/api/settings").length;
    expect(requested).toBeGreaterThan(0);
    expect(container.textContent).toContain("Budgets");
  });
});
