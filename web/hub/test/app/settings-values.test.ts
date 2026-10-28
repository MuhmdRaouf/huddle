// settings-values.test.ts — the one-line values Settings' rows show: the formatters (theme and
// scheme, the paired devices' count, this device's notify choice, the day against the per-day
// budget on everything, the Insights switch's On and Off, the hub's zone, the hub card's line and
// initial) and the hook that reads each source once through the panes' client and leaves a value
// out when its read fails.

import { waitFor } from "@testing-library/preact";
import { createElement } from "preact";
import type { JSX } from "preact";
import { afterEach, describe, expect, it } from "vite-plus/test";
import type { FetchFn } from "../../src/api.ts";
import {
  appearanceValue,
  devicesValue,
  hubInitial,
  hubSubtitle,
  notifyValue,
  spendingValue,
  tipsValue,
  useSettingsValues,
  zoneOf,
} from "../../src/app/settings-values.ts";
import { flush, NOW, stubMedia } from "../helpers.tsx";
import { renderHuddle } from "../render.tsx";

describe("the row values' formatters", () => {
  it("TestAppearanceNamesTheThemeAndTheScheme", () => {
    expect(appearanceValue("huddle", "system")).toBe("Huddle · System");
    expect(appearanceValue("huddle", "light")).toBe("Huddle · Light");
    expect(appearanceValue("huddle", "dark")).toBe("Huddle · Dark");
  });

  it("TestDevicesCountsOneDeviceAndMany", () => {
    expect(devicesValue(1)).toBe("1 device");
    expect(devicesValue(3)).toBe("3 devices");
    expect(devicesValue(0)).toBe("0 devices");
  });

  it("TestNotificationsNamesTheDeviceChoice", () => {
    expect(notifyValue({ notify: "needs" })).toBe("Needs you only");
    expect(notifyValue({ notify: "all" })).toBe("Needs you and alerts");
    expect(notifyValue({ notify: "off" })).toBe("Off");
    expect(notifyValue({})).toBe("Needs you only");
    expect(notifyValue(null)).toBe("Needs you only");
  });

  it("TestSpendingNamesTheDayAgainstThePerDayBudgetOnEverything", () => {
    expect(spendingValue({ spend: [{ scope: "total", period: "day", spentUsd: 1.2, limitUsd: 40 }] })).toBe(
      "$1.20 of $40.00 today",
    );
  });

  it("TestSpendingNamesNothingWithoutAPerDayBudgetOnEverything", () => {
    expect(spendingValue({ spend: [] })).toBe("");
    expect(spendingValue({ spend: [{ scope: "provider", period: "day", spentUsd: 1, limitUsd: 5 }] })).toBe(
      "",
    );
    expect(spendingValue({ spend: [{ scope: "total", period: "month", spentUsd: 1, limitUsd: 5 }] })).toBe(
      "",
    );
    expect(spendingValue({})).toBe("");
    expect(spendingValue(null)).toBe("");
  });

  it("TestTipsSaysOnOrOff", () => {
    expect(tipsValue(true)).toBe("On");
    expect(tipsValue(false)).toBe("Off");
  });

  it("TestZoneIsWhatTheHubAnswers", () => {
    expect(zoneOf({ zone: "Europe/Istanbul", zone_origin: "first-sign-in" }).zone).toBe("Europe/Istanbul");
    expect(zoneOf({ zone: "", zone_origin: "" }).zone).toBe("");
    expect(zoneOf({}).zone).toBe("");
    expect(zoneOf(undefined).zone).toBe("");
  });

  it("TestTheHubCardLineNamesTheVersionAndTheDevice", () => {
    expect(hubSubtitle("0.4.0", "iPhone 17 Pro")).toBe("huddle 0.4.0 · signed in on iPhone 17 Pro");
    expect(hubSubtitle("0.4.0", "")).toBe("huddle 0.4.0");
    expect(hubSubtitle("", "iPhone 17 Pro")).toBe("signed in on iPhone 17 Pro");
    expect(hubSubtitle("", "")).toBe("");
  });

  it("TestTheHubCardTileTakesTheNameSFirstLetter", () => {
    expect(hubInitial("studio")).toBe("S");
    expect(hubInitial("huddle")).toBe("H");
    expect(hubInitial("")).toBe("H");
  });
});

/** The values the hook holds now, painted as one marked row per entry. */
function ValuesProbe(): JSX.Element {
  const values = useSettingsValues();
  return createElement(
    "dl",
    null,
    Object.entries(values).map(([section, value]) =>
      createElement("div", { key: section, "data-value": section }, value),
    ),
  );
}

/** Answers each path by name; a path given no answer fails the way the network would. */
function mountValues(answers: Record<string, unknown>): Array<{ url: string }> {
  stubMedia();
  const calls: Array<{ url: string }> = [];
  const fetchFn: FetchFn = (url) => {
    calls.push({ url });
    const body = answers[url];
    if (body === undefined) return Promise.reject(new Error(`no answer staged for ${url}`));
    return Promise.resolve(
      new Response(JSON.stringify(body), {
        status: 200,
        headers: { "content-type": "application/json" },
      }),
    );
  };
  renderHuddle(createElement(ValuesProbe, null), {}, NOW, fetchFn);
  return calls;
}

afterEach(() => {
  localStorage.clear();
  document.body.textContent = "";
});

describe("the values hook", () => {
  it("TestReadsEachSourceOnceAndShowsEveryValue", async () => {
    const calls = mountValues({
      "/api/devices": { devices: [{ name: "iPhone", added: "", seen: "" }] },
      "/api/insights/settings": { tips_for_me: true },
      "/api/settings": { zone: "Africa/Cairo", zone_origin: "owner" },
      "/api/push/notify": { notify: "needs" },
      "/api/radar/api/budget-status": {
        spend: [{ scope: "total", period: "day", spentUsd: 1.2, limitUsd: 40 }],
      },
    });
    await waitFor(() => {
      expect(document.querySelector('[data-value="security"]')?.textContent).toBe("1 device");
      expect(document.querySelector('[data-value="tips"]')?.textContent).toBe("On");
      expect(document.querySelector('[data-value="hub"]')?.textContent).toBe("Africa/Cairo");
      expect(document.querySelector('[data-value="notifications"]')?.textContent).toBe("Needs you only");
      expect(document.querySelector('[data-value="spending"]')?.textContent).toBe("$1.20 of $40.00 today");
    });
    expect(document.querySelector('[data-value="appearance"]')?.textContent).toBe("Huddle · System");
    expect(calls.filter((c) => c.url === "/api/devices")).toHaveLength(1);
    expect(calls.filter((c) => c.url === "/api/settings")).toHaveLength(1);
    expect(calls.filter((c) => c.url === "/api/push/notify")).toHaveLength(1);
    expect(calls.filter((c) => c.url === "/api/radar/api/budget-status")).toHaveLength(1);
  });

  it("TestTheKeptSchemeIsNamed", async () => {
    localStorage.setItem("huddle:scheme", "dark");
    mountValues({});
    await waitFor(() =>
      expect(document.querySelector('[data-value="appearance"]')?.textContent).toBe("Huddle · Dark"),
    );
  });

  it("TestTipsReadsOffWhileTheFieldIsAbsent", async () => {
    // the pane reads an absent field as off, so the row names the same
    mountValues({ "/api/insights/settings": {} });
    await waitFor(() => expect(document.querySelector('[data-value="tips"]')?.textContent).toBe("Off"));
  });

  it("TestTheNotificationsRowNamesTheChoiceTheHubKeeps", async () => {
    mountValues({ "/api/push/notify": { notify: "off" } });
    await waitFor(() =>
      expect(document.querySelector('[data-value="notifications"]')?.textContent).toBe("Off"),
    );
  });

  it("TestAFailedReadLeavesItsValueOut", async () => {
    const calls = mountValues({});
    await flush();
    expect(calls.map((c) => c.url).sort()).toEqual([
      "/api/devices",
      "/api/insights/settings",
      "/api/push/notify",
      "/api/radar/api/budget-status",
      "/api/settings",
    ]);
    expect(document.querySelector('[data-value="appearance"]')?.textContent).toBe("Huddle · System");
    expect(document.querySelector('[data-value="security"]')).toBeNull();
    expect(document.querySelector('[data-value="notifications"]')).toBeNull();
    expect(document.querySelector('[data-value="spending"]')).toBeNull();
    expect(document.querySelector('[data-value="tips"]')).toBeNull();
    expect(document.querySelector('[data-value="hub"]')).toBeNull();
  });
});
