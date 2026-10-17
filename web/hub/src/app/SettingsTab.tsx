// SettingsTab.tsx — the Settings tab: one section per settings surface, as underline tabs like
// the Radar tab's sub-tabs. Radar's own settings (budgets, notifications, history retention)
// live here; the surfaces later cards add (theme, machines, allow lists, the hub address) join
// as their own sections.

import type { JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import type { ApiError } from "../api.ts";
import { Icon } from "../icons.tsx";
import { SettingsView } from "../radar/app/views/Settings.tsx";
import { RadarProvider, radarController } from "../radar/mount.tsx";
import { ageText } from "../registry.ts";
import { Channels } from "./Channels.tsx";
import { errText, useHuddle } from "./context.tsx";
import { useStepUp } from "./Needs.tsx";
import { NotificationsCard } from "./Notifications.tsx";

/** The sections the Settings tab serves, in row order; each later surface adds its entry. */
const SECTIONS = [
  { id: "radar", label: "Radar" },
  { id: "devices", label: "Devices" },
  { id: "channels", label: "Linked channels" },
  { id: "notifications", label: "Notifications" },
] as const;

/** One section of the Settings tab. */
export type SettingsSection = (typeof SECTIONS)[number]["id"];

/** The section the address names; a missing or unknown one reads as the first. */
export function settingsSectionOf(named: string | undefined): SettingsSection {
  return SECTIONS.some((entry) => entry.id === named) ? (named as SettingsSection) : "radar";
}

/** The section's own settings: radar's budgets, notifications and history retention, loaded the
 *  first time the section shows (the controller runs already — main starts it on this address). */
function RadarSettings(): JSX.Element {
  useEffect(() => {
    // the settings data loads here rather than on the controller's boot: the Radar tab's views
    // never ask for it, and this section may be the first radar address the reader opens
    void radarController()?.loadTab("settings");
  }, []);
  return (
    <RadarProvider>
      <SettingsView />
    </RadarProvider>
  );
}

/** One paired device as the server lists it. */
type DeviceRow = { name: string; added: string; seen: string };

/** Settings › Devices (SPEC 7.3): every paired device with its ages, the removal that asks for
 *  the passkey when the server says step-up, and the `huddle pair` line that pairs the next
 *  device. A removed device's browser is signed out server-side by the removal itself. */
export function Devices(): JSX.Element {
  const { api, toast, copy, now } = useHuddle();
  const { ask, sheet } = useStepUp();
  const [devices, setDevices] = useState<DeviceRow[] | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    let alive = true;
    api
      .api("/api/devices")
      .then((r) => {
        if (alive) setDevices((r as { devices?: DeviceRow[] }).devices ?? []);
      })
      .catch((e: unknown) => {
        if (alive) setError(errText(e));
      });
    return () => {
      alive = false;
    };
  }, [api]);

  const remove = async (name: string): Promise<void> => {
    // the removal is a dangerous act (SPEC 7.4): a 401 runs the passkey sheet once and deletes again
    const drop = async (): Promise<void> => {
      setDevices((d) => (d ?? []).filter((x) => x.name !== name));
    };
    const path = `/api/devices/${encodeURIComponent(name)}`;
    try {
      await api.api(path, { method: "DELETE" });
      await drop();
    } catch (e) {
      const err = e as ApiError;
      if (err.status !== 401 || err.message !== "step-up required") {
        toast(errText(e), { bad: true });
        return;
      }
      if (!(await ask({ action: "Remove device", session: name }))) return;
      try {
        await api.api(path, { method: "DELETE" });
        await drop();
      } catch (e2) {
        toast(errText(e2), { bad: true });
      }
    }
  };

  return (
    <section aria-labelledby="devices-h" data-devices="" class="flex flex-col gap-1">
      {sheet}
      <h2 id="devices-h" class="mb-1.5 text-xs font-semibold tracking-[0.06em] uppercase muted">
        Devices{devices && devices.length > 0 ? ` · ${devices.length}` : ""}
      </h2>
      {error ? (
        <p role="alert" class="m-0 text-sm text-error">
          {error}
        </p>
      ) : null}
      {devices === null ? (
        <p class="m-0 text-sm muted">Loading…</p>
      ) : devices.length === 0 ? (
        <p class="m-0 text-sm muted">No devices paired yet.</p>
      ) : (
        <ul class="m-0 flex list-none flex-col p-0">
          {devices.map((d) => (
            <li
              key={d.name}
              data-device={d.name}
              class="flex min-h-12 items-center justify-between gap-3 border-b hairline py-1"
            >
              <div class="flex min-w-0 flex-col">
                <span class="truncate text-[15px]">{d.name}</span>
                <span class="text-xs muted tnum">
                  added {ageText(d.added, now)} · last seen {ageText(d.seen, now)}
                </span>
              </div>
              <button
                type="button"
                class="btn btn-ghost btn-sm text-error"
                onClick={() => void remove(d.name)}
              >
                Remove
              </button>
            </li>
          ))}
        </ul>
      )}
      <div class="mt-4 flex flex-col gap-1.5">
        <h3 class="m-0 text-[15px] font-semibold">Pair a new device</h3>
        <p class="m-0 text-sm muted">
          Run this on the computer running huddle, then open the link on the device:
        </p>
        <div class="flex h-10 items-center gap-2.5 rounded-lg border border-base-content/15 bg-base-200 pr-1 pl-3">
          <span class="muted">
            <Icon name="terminal" />
          </span>
          <code class="min-w-0 flex-1 truncate text-sm">huddle pair</code>
          <button
            type="button"
            class="btn btn-ghost btn-square btn-sm muted"
            aria-label="Copy huddle pair"
            onClick={() => copy("huddle pair")}
          >
            <Icon name="copy" />
          </button>
        </div>
      </div>
    </section>
  );
}

/** The Settings tab: the section row, then the named section's panes. */
export function SettingsTab({ section }: { section: SettingsSection }): JSX.Element {
  return (
    <div class="flex min-h-0 flex-1 flex-col">
      <nav
        aria-label="Settings sections"
        class="flex min-h-[52px] shrink-0 items-center gap-0.5 border-b hairline px-5"
      >
        {SECTIONS.map((entry) => (
          <a
            key={entry.id}
            href={`#/settings/${entry.id}`}
            data-section={entry.id}
            aria-current={entry.id === section ? "page" : undefined}
            class={`flex h-full shrink-0 items-center border-b-2 px-3.5 text-sm outline-none focus-visible:ring-2 focus-visible:ring-primary ${
              entry.id === section
                ? "border-primary font-semibold text-base-content"
                : "border-transparent text-base-content/70 hover:text-base-content"
            }`}
          >
            {entry.label}
          </a>
        ))}
      </nav>
      <div class="min-h-0 flex-1 overflow-y-auto">
        <div class="mx-auto w-full max-w-3xl px-4 py-6 sm:px-6">
          {section === "devices" ? (
            <Devices />
          ) : section === "channels" ? (
            <Channels />
          ) : section === "notifications" ? (
            <NotificationsCard />
          ) : (
            <RadarSettings />
          )}
        </div>
      </div>
    </div>
  );
}
