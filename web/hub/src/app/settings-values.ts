// settings-values.ts — the current value each Settings row names: a formatter per group's source,
// and one hook that reads each source once on mount through the hub's api client (the notify
// choice on a paired device alone). A read that fails leaves its value out, so the row shows
// nothing rather than a stale or wrong answer.

import { readScheme, type SchemePref } from "@huddle/ui/theme.ts";
import { readTheme, THEMES } from "@huddle/ui/themes.ts";
import { useEffect, useState } from "preact/hooks";
import { fmtUsd } from "../radar/fmt.ts";
import { useHuddle } from "./context.tsx";
import { CHOICES, readNotifyChoice } from "./Notifications.tsx";
import { SCHEMES } from "./scheme.ts";
import type { SettingsSection } from "./SettingsTab.tsx";

/** The value each group's row shows, by group id; a group whose read has not landed is left out
 *  and its row shows nothing. */
export type SettingsValues = Partial<Record<SettingsSection, string>>;

/** The theme's label beside the scheme's: "Huddle · System". */
export function appearanceValue(theme: string, scheme: SchemePref): string {
  const themeLabel = THEMES.find((t) => t.id === theme)?.label ?? theme;
  const schemeLabel = SCHEMES.find(([id]) => id === scheme)?.[1] ?? scheme;
  return `${themeLabel} · ${schemeLabel}`;
}

/** The label of this device's choice of what wakes it: the pane's own words. */
export function notifyValue(v: unknown): string {
  return CHOICES.find((c) => c.value === readNotifyChoice(v))?.label ?? "";
}

/** The paired devices' count: "1 device", "3 devices". */
export function devicesValue(count: number): string {
  return `${count} ${count === 1 ? "device" : "devices"}`;
}

/** One line of radar's budget answer, as the wire holds it. */
type SpendLine = { scope: unknown; period: unknown; spentUsd: unknown; limitUsd: unknown };

/** The day against the per-day budget on everything: "$1.20 of $40.00 today". No such budget in
 *  the answer — or one it does not price — and the row names nothing. */
export function spendingValue(r: unknown): string {
  const spend = (r as { spend?: unknown } | null)?.spend;
  if (!Array.isArray(spend)) return "";
  for (const s of spend as SpendLine[]) {
    if (s.scope !== "total" || s.period !== "day") continue;
    if (typeof s.spentUsd !== "number" || typeof s.limitUsd !== "number") return "";
    return `${fmtUsd(s.spentUsd)} of ${fmtUsd(s.limitUsd)} today`;
  }
  return "";
}

/** Whether the hub's tips reach this browser. */
export function tipsValue(on: boolean): string {
  return on ? "On" : "Off";
}

/** The hub's time zone and where the hub got it, as the settings route holds them. */
export type HubZone = { zone: string; origin: string };

export function zoneOf(r: unknown): HubZone {
  const o = (r ?? {}) as Record<string, unknown>;
  return {
    zone: typeof o.zone === "string" ? o.zone : "",
    origin: typeof o.zone_origin === "string" ? o.zone_origin : "",
  };
}

/** The hub card's line under the name: "huddle 0.4.0 · signed in on iPhone"; a part with nothing
 *  to say stays out. */
export function hubSubtitle(version: string, device: string): string {
  const left = version ? `huddle ${version}` : "";
  const right = device ? `signed in on ${device}` : "";
  return [left, right].filter(Boolean).join(" · ");
}

/** The hub card tile's letter: the hub name's first, uppercased, or H for huddle. */
export function hubInitial(name: string): string {
  return name.trim().charAt(0).toUpperCase() || "H";
}

/** Reads every source once on mount and keeps the values it got. Appearance comes from this
 *  browser alone, so it is there before the first paint. */
export function useSettingsValues(): SettingsValues {
  const { api, who } = useHuddle();
  const [values, setValues] = useState<SettingsValues>(() => ({
    appearance: appearanceValue(readTheme(localStorage), readScheme(localStorage)),
  }));
  useEffect(() => {
    let alive = true;
    const put = (section: SettingsSection, value: string): void => {
      if (alive) setValues((v) => ({ ...v, [section]: value }));
    };
    // a refused or failed read leaves its value out; the row stays empty
    api
      .api("/api/devices")
      .then((r) => {
        const devices = (r as { devices?: unknown }).devices;
        if (Array.isArray(devices)) put("security", devicesValue(devices.length));
      })
      .catch(() => {});
    api
      .api("/api/insights/settings")
      .then((r) => {
        const tips = (r as { tips_for_me?: unknown }).tips_for_me;
        put("tips", tipsValue(tips === true));
      })
      .catch(() => {});
    api
      .api("/api/settings")
      .then((r) => {
        const { zone } = zoneOf(r);
        if (zone) put("hub", zone);
      })
      .catch(() => {});
    // the notify choice is a paired device's own; the route refuses every other browser, and a
    // refused read would put a 403 in the console
    if (who.kind === "device") {
      api
        .api("/api/push/notify")
        .then((r) => put("notifications", notifyValue(r)))
        .catch(() => {});
    }
    api
      .api("/api/radar/api/budget-status")
      .then((r) => {
        const value = spendingValue(r);
        if (value !== "") put("spending", value);
      })
      .catch(() => {});
    return () => {
      alive = false;
    };
  }, [api, who.kind]);
  return values;
}
