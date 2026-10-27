// settings-values.ts — the current value each Settings row names: a formatter per group's source,
// and one hook that reads each source once on mount through the hub's api client. A read that
// fails leaves its value out, so the row shows nothing rather than a stale or wrong answer.
// Notifications and Spending name no value yet; their cards add them.

import { readScheme, type SchemePref } from "@huddle/ui/theme.ts";
import { readTheme, THEMES } from "@huddle/ui/themes.ts";
import { useEffect, useState } from "preact/hooks";
import { useHuddle } from "./context.tsx";
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

/** The paired devices' count: "1 device", "3 devices". */
export function devicesValue(count: number): string {
  return `${count} ${count === 1 ? "device" : "devices"}`;
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
  const { api } = useHuddle();
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
    return () => {
      alive = false;
    };
  }, [api]);
  return values;
}
