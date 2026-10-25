// SettingsTab.tsx — the Settings page: one section per settings group, in a section row shaped like
// Radar's view row. This browser's groups come first — Appearance (System, Dark, Light, and the
// Catppuccin flavour), Notifications (the two switches and what wakes the device), Devices (the
// paired ones, with remove), This hub (name, address, version) and Phone (the trust guide in a
// sheet) — then Sign-in, Radar's own settings (budgets, notifications, history retention) and
// Linked channels. Every class comes from the ui components or the tokens.

import { Button } from "@huddle/ui/button.tsx";
import { List, ListItem } from "@huddle/ui/list.tsx";
import { Sheet } from "@huddle/ui/sheet.tsx";
import { Switch } from "@huddle/ui/switch.tsx";
import {
  applyFlavour,
  type Flavour,
  readFlavour,
  readScheme,
  type SchemePref,
  writeFlavour,
} from "@huddle/ui/theme.ts";
import type { JSX } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type { ApiError } from "../api.ts";
import { Icon } from "../icons.tsx";
import { creationFromJSON, credentialToJSON } from "../passkey.ts";
import { SettingsView } from "../radar/app/views/Settings.tsx";
import { RadarProvider, radarController } from "../radar/mount.tsx";
import { ageText } from "../registry.ts";
import { Channels } from "./Channels.tsx";
import { errText, useHuddle } from "./context.tsx";
import { useLayout } from "./layout.ts";
import { useStepUp } from "./Needs.tsx";
import { NotificationsCard } from "./Notifications.tsx";
import { pickScheme, SCHEMES } from "./scheme.ts";
import { AuthenticatorCard } from "./Totp.tsx";

/** The sections the Settings page serves, in row order; each later surface adds its entry. */
const SECTIONS = [
  { id: "appearance", label: "Appearance" },
  { id: "notifications", label: "Notifications" },
  { id: "devices", label: "Devices" },
  { id: "hub", label: "This hub" },
  { id: "phone", label: "Phone" },
  { id: "signin", label: "Sign-in" },
  { id: "radar", label: "Radar" },
  { id: "channels", label: "Linked channels" },
] as const;

/** A section's heading, the same small caps on every section. */
const HEAD = "mb-1.5 text-xs font-semibold tracking-[0.06em] uppercase text-[var(--ui-muted)]";

/** Muted body copy under a heading. */
const NOTE = "m-0 text-sm text-[var(--ui-muted)]";

/** An error said inline. */
const ERR = "m-0 text-sm text-[var(--ui-fail-ink)]";

/** One section of the Settings page. */
export type SettingsSection = (typeof SECTIONS)[number]["id"];

/** The section the address names; a missing or unknown one reads as the first. */
export function settingsSectionOf(named: string | undefined): SettingsSection {
  return SECTIONS.some((entry) => entry.id === named) ? (named as SettingsSection) : "appearance";
}

/** The section's own settings: radar's budgets, notifications and history retention, loaded the
 *  first time the section shows (the controller runs already — main starts it on this address). */
function RadarSettings(): JSX.Element {
  useEffect(() => {
    // the settings data loads here rather than on the controller's boot: Radar's own views
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

/** The phone guide the server's "Add a phone" started: where it answers and the root's
 *  fingerprint, which the phone's page shows too. */
type PhoneGuide = { url: string; fingerprint: string; expires_in: number };

/** Settings › Appearance: System follows this device's own setting; Dark and Light hold one
 *  scheme in this browser whatever the device says. The Catppuccin switch paints either scheme in
 *  Catppuccin's colours (latte when light, mocha when dark), also kept in this browser. */
export function Appearance(): JSX.Element {
  const [pref, setPref] = useState<SchemePref>(() => readScheme(localStorage));
  const [flavour, setFlavour] = useState<Flavour>(() => readFlavour(localStorage));
  const catppuccin = flavour === "catppuccin";
  return (
    <section aria-labelledby="appearance-h" class="flex flex-col gap-3">
      <h2 id="appearance-h" class={HEAD}>
        Appearance
      </h2>
      <p class={NOTE}>
        Dark and light follow this device until you pick one here. The choice stays in this browser.
      </p>
      <div role="group" aria-label="Scheme" class="flex flex-wrap gap-2">
        {SCHEMES.map(([id, label, icon]) => (
          <Button
            key={id}
            variant="secondary"
            icon={icon}
            pressed={pref === id}
            onClick={() => {
              pickScheme(localStorage, id);
              setPref(id);
            }}
          >
            {label}
          </Button>
        ))}
      </div>
      <label class="flex min-h-[var(--ui-tap)] items-center gap-3 text-sm">
        <Switch
          labelledBy="flavour-l"
          checked={catppuccin}
          onChange={(on) => {
            const next: Flavour = on ? "catppuccin" : "plain";
            writeFlavour(localStorage, next);
            applyFlavour(document.documentElement, next);
            setFlavour(next);
          }}
        />
        <span id="flavour-l">Catppuccin colours</span>
      </label>
    </section>
  );
}

/** What /health says about the hub this page talks to. */
type HubFacts = { name: string; version: string };

/** Settings › This hub: its name (the hub's own, else the machine's), the address this page
 *  reached it at, and the version it runs. */
export function ThisHub(): JSX.Element {
  const { api } = useHuddle();
  const [facts, setFacts] = useState<HubFacts | null>(null);
  useEffect(() => {
    let alive = true;
    api
      .api("/health")
      .then((r) => {
        const h = r as { name?: unknown; machine?: unknown; version?: unknown };
        const text = (v: unknown): string => (typeof v === "string" ? v : "");
        if (alive) setFacts({ name: text(h.name) || text(h.machine), version: text(h.version) });
      })
      .catch(() => {
        if (alive) setFacts({ name: "", version: "" });
      });
    return () => {
      alive = false;
    };
  }, [api]);
  const unknown = facts === null ? "Loading…" : "Unknown";
  return (
    <section aria-labelledby="hub-h" data-hub-facts="" class="flex flex-col gap-1">
      <h2 id="hub-h" class={HEAD}>
        This hub
      </h2>
      <List label="This hub">
        <ListItem title="Name" meta={facts?.name || unknown} />
        <ListItem title="Address" meta={<span class="ui-mono break-all">{location.origin}</span>} />
        <ListItem title="Version" meta={<span class="ui-mono">{facts?.version || unknown}</span>} />
      </List>
    </section>
  );
}

/** Settings › Phone: "Add a phone" opens a sheet that starts the guide the server serves
 *  (10 minutes, loopback-bound QR) and shows its QR and the fingerprint to compare with the
 *  phone's page. */
export function Phone(): JSX.Element {
  const { api, toast } = useHuddle();
  const [open, setOpen] = useState(false);
  const [guide, setGuide] = useState<PhoneGuide | null>(null);

  const start = async (): Promise<void> => {
    setOpen(true);
    if (guide) return;
    try {
      const r = await api.api("/api/phone", { body: { action: "start" } });
      setGuide(r as PhoneGuide);
    } catch (e) {
      setOpen(false);
      toast(errText(e), { bad: true });
    }
  };

  return (
    <section aria-labelledby="phone-h" class="flex flex-col gap-3">
      <h2 id="phone-h" class={HEAD}>
        Phone
      </h2>
      <p class={NOTE}>
        A phone trusts the hub's root once: it scans a QR this computer serves for 10 minutes, and the page it
        opens walks the taps.
      </p>
      <Button class="w-fit" onClick={() => void start()}>
        Add a phone
      </Button>
      <Sheet open={open} onClose={() => setOpen(false)} title="Add a phone">
        {guide ? (
          <div class="flex flex-col items-start gap-4">
            <img
              data-phone-qr=""
              src="/api/phone/qr.svg"
              alt="QR code: the phone's trust guide"
              width={160}
              height={160}
              class="size-40 shrink-0 rounded-lg bg-white p-2"
            />
            <code class="ui-mono break-all text-xs">{guide.fingerprint}</code>
            <p class={NOTE}>
              Scan with the phone's camera. The page it opens shows the same fingerprint — compare before
              installing. The guide stops itself after 10 minutes.
            </p>
          </div>
        ) : (
          <p class={NOTE}>Starting…</p>
        )}
      </Sheet>
    </section>
  );
}

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
      <h2 id="devices-h" class={HEAD}>
        Devices{devices && devices.length > 0 ? ` · ${devices.length}` : ""}
      </h2>
      {error ? (
        <p role="alert" class={ERR}>
          {error}
        </p>
      ) : null}
      {devices === null ? (
        <p class={NOTE}>Loading…</p>
      ) : devices.length === 0 ? (
        <p class={NOTE}>No devices paired yet.</p>
      ) : (
        <List label="Paired devices">
          {devices.map((d) => (
            <ListItem
              key={d.name}
              title={d.name}
              meta={
                <span class="tabular-nums">
                  added {ageText(d.added, now)} · last seen {ageText(d.seen, now)}
                </span>
              }
              trailing={
                <span data-device={d.name}>
                  <Button
                    variant="ghost"
                    class="text-[var(--ui-fail-ink)]"
                    onClick={() => void remove(d.name)}
                  >
                    Remove
                  </Button>
                </span>
              }
            />
          ))}
        </List>
      )}
      <div class="mt-4 flex flex-col gap-1.5">
        <h3 class="m-0 text-[15px] font-semibold">Pair a new device</h3>
        <p class={NOTE}>Run this on the computer running huddle, then open the link on the device:</p>
        <div class="flex min-h-[var(--ui-tap)] items-center gap-2.5 rounded-[var(--ui-radius)] border border-[var(--ui-line)] bg-[var(--ui-surface-2)] pl-3">
          <span class="text-[var(--ui-muted)]">
            <Icon name="terminal" />
          </span>
          <code class="min-w-0 flex-1 truncate text-sm">huddle pair</code>
          <Button variant="ghost" label="Copy huddle pair" onClick={() => copy("huddle pair")}>
            <Icon name="copy" />
          </Button>
        </div>
      </div>
    </section>
  );
}

/** Settings › Sign-in: the two ways a browser signs itself in — the authenticator code (the
 *  primary, one enrolment per hub) and this browser's own passkey — with the enrolment's
 *  set-up, its removal, and the passkey a code session can save without a link. */
export function SignInMethods(): JSX.Element {
  const { api, toast } = useHuddle();
  const [facts, setFacts] = useState<{ enrolled: boolean; kind: string; passkey: boolean } | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const alive = useRef(true);
  const load = (): void => {
    api
      .api("/api/whoami")
      .then((r) => {
        if (!alive.current) return;
        const w = r as { totp?: unknown; kind?: unknown; device_passkey?: unknown };
        setFacts({
          enrolled: w.totp === true,
          kind: typeof w.kind === "string" ? w.kind : "",
          passkey: w.device_passkey === true,
        });
      })
      .catch((e: unknown) => {
        if (alive.current) setError(errText(e));
      });
  };

  useEffect(() => {
    alive.current = true;
    load();
    return () => {
      alive.current = false;
    };
  }, []);

  /** Saves this browser a passkey of its own, on the device row its session belongs to. */
  const savePasskey = async (): Promise<void> => {
    setBusy(true);
    setError("");
    try {
      const r = (await api.api("/api/passkey/begin", { body: {} })) as { options?: unknown };
      let cred: PublicKeyCredential;
      try {
        cred = (await navigator.credentials.create({
          publicKey: creationFromJSON(r.options).publicKey,
        })) as PublicKeyCredential;
      } catch {
        setError("The passkey was not created. Try again.");
        return;
      }
      await api.api("/api/passkey/finish", { body: { response: credentialToJSON(cred) } });
      toast("Passkey saved for this browser");
      load();
    } catch (e) {
      setError(errText(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <section aria-labelledby="signin-h" data-signin="" class="flex flex-col gap-1">
      <h2 id="signin-h" class={HEAD}>
        Sign-in methods
      </h2>
      {error ? (
        <p role="alert" class={ERR}>
          {error}
        </p>
      ) : null}
      {facts === null ? (
        <p class={NOTE}>Loading…</p>
      ) : (
        <>
          <AuthenticatorCard enrolled={facts.enrolled} onChanged={load} />
          <div
            class="flex items-center justify-between gap-3 border-b border-[var(--ui-line)] py-3"
            data-passkey-row=""
          >
            <div class="flex min-w-0 flex-col">
              <span class="text-[15px]">Passkey for this browser</span>
              <span class="text-xs text-[var(--ui-muted)]">
                {facts.kind === "device"
                  ? facts.passkey
                    ? "This browser signs in with its passkey."
                    : "Not saved. This device's lock would sign in without a code."
                  : "A passkey is saved per browser: sign in with a code first, then accept the offer."}
              </span>
            </div>
            {facts.kind === "device" && !facts.passkey ? (
              <Button disabled={busy} onClick={() => void savePasskey()}>
                Save passkey
              </Button>
            ) : null}
          </div>
          <p class="m-0 pt-2 text-xs text-[var(--ui-muted)]">
            Every device session ends after 7 days unused; one code or touch starts a fresh one. Manage the
            devices themselves under Devices.
          </p>
        </>
      )}
    </section>
  );
}

/** The Settings page: the section row, then the named section's panes. The row is the Radar
 *  views' own shape: underlined links at the desk, pills that scroll inside the row on the
 *  narrower layouts, so the eight sections never push the page sideways. */
export function SettingsTab({ section }: { section: SettingsSection }): JSX.Element {
  const layout = useLayout();
  return (
    <div class="flex min-h-0 flex-1 flex-col">
      <nav
        aria-label="Settings sections"
        class={
          layout === "desktop"
            ? "flex min-h-[52px] shrink-0 items-center gap-0.5 border-b border-[var(--ui-line)] px-5"
            : "flex shrink-0 items-center gap-1.5 overflow-x-auto px-4 py-2"
        }
      >
        {SECTIONS.map((entry) => (
          <a
            key={entry.id}
            href={`#/settings/${entry.id}`}
            data-section={entry.id}
            aria-current={entry.id === section ? "page" : undefined}
            class={
              layout === "desktop"
                ? `flex min-h-[var(--ui-tap)] self-stretch shrink-0 items-center border-b-2 px-3.5 text-sm outline-none focus-visible:ring-2 focus-visible:ring-[var(--ui-accent)] ${
                    entry.id === section
                      ? "border-[var(--ui-accent)] font-semibold text-[var(--ui-text)]"
                      : "border-transparent text-[var(--ui-muted)] hover:text-[var(--ui-text)]"
                  }`
                : `flex min-h-[var(--ui-tap)] shrink-0 items-center rounded-full px-3.5 text-sm outline-none focus-visible:ring-2 focus-visible:ring-[var(--ui-accent)] ${
                    entry.id === section
                      ? "bg-[var(--ui-accent)] font-semibold text-[var(--ui-on-accent)]"
                      : "border border-[var(--ui-line-strong)] text-[var(--ui-text)] hover:bg-[var(--ui-surface-2)]"
                  }`
            }
          >
            {entry.label}
          </a>
        ))}
      </nav>
      <div class="min-h-0 flex-1 overflow-y-auto">
        <div class="mx-auto w-full max-w-3xl px-4 py-6 sm:px-6" data-settings="">
          {section === "appearance" ? (
            <Appearance />
          ) : section === "hub" ? (
            <ThisHub />
          ) : section === "phone" ? (
            <Phone />
          ) : section === "signin" ? (
            <SignInMethods />
          ) : section === "devices" ? (
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
