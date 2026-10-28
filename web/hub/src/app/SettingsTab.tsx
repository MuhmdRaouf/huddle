// SettingsTab.tsx — the Settings page: six groups, on the phone a hub card over rows of three
// cards whose rows name each group's current value and open one page per group, on the tablet and
// the desktop a sidebar beside the page; each page draws its group's members in order. Appearance
// (System, Dark, Light, and the Catppuccin flavour); Notifications (the two switches and what
// wakes the device); Security (the paired devices oldest first with the browser asking named
// current, the phone's trust guide and the pair command, the sign-in methods in sheets, the two
// sign-outs); Spending (Radar's budgets, notifications and history retention); Tips (the
// two Insights switches); Hub (name, address and version, the hub's time zone, the request
// bodies capture, and Radar's self-check against the transcripts). A channel's linked sessions live in that channel's own
// Settings view. Every class comes from the ui components or the tokens.

import { Button } from "@huddle/ui/button.tsx";
import { Field } from "@huddle/ui/field.tsx";
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
import { useCallback, useEffect, useRef, useState } from "preact/hooks";
import type { ApiError } from "../api.ts";
import { Icon, type IconName } from "../icons.tsx";
import { creationFromJSON, credentialToJSON } from "../passkey.ts";
import { SELFCHECK_PATH, SelfCheckBadge, selfCheckOf } from "../radar/app/SelfCheckBadge.tsx";
import { SettingsView } from "../radar/app/views/Settings.tsx";
import { RadarProvider, radarController } from "../radar/mount.tsx";
import { ageText } from "../registry.ts";
import { CaptureSetting } from "./CaptureSetting.tsx";
import { errText, useHuddle } from "./context.tsx";
import { InsightsSettings } from "./InsightsSettings.tsx";
import { useLayout } from "./layout.ts";
import { useStepUp } from "./Needs.tsx";
import { NotificationsCard } from "./Notifications.tsx";
import { pickScheme, SCHEMES } from "./scheme.ts";
import { SignOut } from "./SignOut.tsx";
import { hubInitial, hubSubtitle, useSettingsValues, zoneOf, type HubZone } from "./settings-values.ts";
import { AuthenticatorCard } from "./Totp.tsx";

/** A section's heading, the same small caps on every section. */
const HEAD = "mb-1.5 text-xs font-semibold tracking-[0.06em] uppercase text-[var(--ui-muted)]";

/** Muted body copy under a heading. */
const NOTE = "m-0 text-sm text-[var(--ui-muted)]";

/** An error said inline. */
const ERR = "m-0 text-sm text-[var(--ui-fail-ink)]";

/** Settings › Spending › Radar: radar's budgets, notifications and history retention, loaded
 *  the first time the group shows (the controller runs already — main starts it on this group). */
function RadarSettings(): JSX.Element {
  useEffect(() => {
    // the settings data loads here rather than on the controller's boot: Radar's own views
    // never ask for it, and this group may be the first radar address the reader opens
    void radarController()?.loadTab("settings");
  }, []);
  return (
    <RadarProvider>
      <SettingsView />
    </RadarProvider>
  );
}

/** Settings › Hub › Self-check: whether Radar's totals for today match the transcripts,
 *  said as a badge and a sentence, read through the hub's own client. */
function SelfCheck(): JSX.Element {
  const { api } = useHuddle();
  const read = useCallback(() => api.api(SELFCHECK_PATH).then(selfCheckOf, () => null), [api]);
  return (
    <section aria-labelledby="selfcheck-h" class="flex flex-col gap-3">
      <h2 id="selfcheck-h" class={HEAD}>
        Self-check
      </h2>
      <p class={NOTE}>
        Radar's totals for today against the same day re-summed from the Claude Code transcripts.
      </p>
      <SelfCheckBadge read={read} />
    </section>
  );
}

/** One paired device as the server lists it; `current` marks the browser asking. */
type DeviceRow = { name: string; added: string; seen: string; current: boolean };

/** How fresh one device's check-in is: live within two minutes, active within the hour, else the
 *  last use. */
function freshness(seen: string, now: number): string {
  const ago = now - Date.parse(seen);
  if (ago < 2 * 60_000) return "active now";
  if (ago < 3_600_000) return `active ${ageText(seen, now)} ago`;
  return `last used ${ageText(seen, now)}`;
}

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

/** The name and version /health answers, checked at the edge; the machine's name stands in for a
 *  hub without one of its own. */
function hubFactsOf(r: unknown): HubFacts {
  const h = (r ?? {}) as { name?: unknown; machine?: unknown; version?: unknown };
  const text = (v: unknown): string => (typeof v === "string" ? v : "");
  return { name: text(h.name) || text(h.machine), version: text(h.version) };
}

/** Settings › Hub › This hub: its name (the hub's own, else the machine's), the address this page
 *  reached it at, and the version it runs. */
export function ThisHub(): JSX.Element {
  const { api } = useHuddle();
  const [facts, setFacts] = useState<HubFacts | null>(null);
  useEffect(() => {
    let alive = true;
    api
      .api("/health")
      .then((r) => {
        if (alive) setFacts(hubFactsOf(r));
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

/** The IANA zones this browser knows, UTC first; empty where the browser cannot list them. */
export function browserZones(): string[] {
  const list = (Intl as { supportedValuesOf?: (key: "timeZone") => string[] }).supportedValuesOf;
  if (typeof list !== "function") return [];
  return ["UTC", ...list("timeZone").filter((z) => z !== "UTC")];
}

/** Settings › Hub › Time zone: the one zone the hub names the owner's day in, seeded from
 *  the first signed-in browser and said so until the owner picks one. The picker lists the zones
 *  this browser knows; a browser that lists none gets a typed field with UTC filled in. */
export function TimeZone({ zones = browserZones() }: { zones?: string[] }): JSX.Element {
  const { api, toast } = useHuddle();
  const [held, setHeld] = useState<HubZone | null>(null);
  const [typed, setTyped] = useState("UTC");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let alive = true;
    api
      .api("/api/settings")
      .then((r) => {
        if (!alive) return;
        const zone = zoneOf(r);
        setHeld(zone);
        if (zone.zone) setTyped(zone.zone);
      })
      .catch((e: unknown) => {
        if (alive) toast(errText(e), { bad: true });
      });
    return () => {
      alive = false;
    };
  }, [api]);

  const pick = async (zone: string): Promise<void> => {
    setBusy(true);
    try {
      setHeld(zoneOf(await api.api("/api/settings", { method: "PUT", body: { zone } })));
    } catch (e) {
      toast(errText(e), { bad: true });
    } finally {
      setBusy(false);
    }
  };

  const said =
    held === null
      ? "Time zone: Loading…"
      : !held.zone
        ? "Time zone: not set yet"
        : held.origin === "first-sign-in"
          ? `Time zone: ${held.zone} (from your first sign-in)`
          : `Time zone: ${held.zone}`;
  // a held zone the browser does not list still shows as the picked option
  const options = held?.zone && !zones.includes(held.zone) ? [held.zone, ...zones] : zones;

  return (
    <section aria-labelledby="zone-h" data-hub-zone="" class="flex flex-col gap-3">
      <h2 id="zone-h" class={HEAD}>
        {said}
      </h2>
      <p class={NOTE}>The hub names the day in this zone. Times are stored in UTC either way.</p>
      {zones.length > 0 ? (
        <label class="flex flex-col gap-1.5 text-sm font-semibold">
          Time zone
          <select
            class="ui-input"
            disabled={held === null || busy}
            value={held?.zone ?? ""}
            onChange={(e) => void pick(e.currentTarget.value)}
          >
            {held !== null && !held.zone ? <option value="">Pick a zone</option> : null}
            {options.map((z) => (
              <option key={z} value={z}>
                {z}
              </option>
            ))}
          </select>
        </label>
      ) : (
        <div class="flex flex-col gap-2">
          <Field
            label="Time zone"
            value={typed}
            onInput={setTyped}
            mono
            hint="An IANA name such as Europe/Istanbul, or UTC."
          />
          <Button
            class="w-fit"
            disabled={busy || typed.trim() === ""}
            onClick={() => void pick(typed.trim())}
          >
            Save
          </Button>
        </div>
      )}
    </section>
  );
}

/** Settings › Security › Add a device: the phone joins through the trust guide — "Add a phone"
 *  starts it (10 minutes, loopback-bound QR) and the sheet shows its QR and the fingerprint to
 *  compare with the phone's page — and a machine joins through `huddle pair --satellite`, run on
 *  this hub, one tap to copy. */
export function Phone(): JSX.Element {
  const { api, toast, copy } = useHuddle();
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

  const cmd = "huddle pair --satellite";

  return (
    <section aria-labelledby="phone-h" class="flex flex-col gap-1">
      <h2 id="phone-h" class={HEAD}>
        Add a device
      </h2>
      <List label="Add a device">
        <ListItem title="Add a phone" onClick={() => void start()} />
        <ListItem
          title="Pair a machine"
          meta={<code class="ui-mono">{cmd}</code>}
          trailing={
            <Button variant="ghost" label="Copy the pair command" onClick={() => copy(cmd)}>
              Copy
            </Button>
          }
        />
      </List>
      <Sheet open={open} onClose={() => setOpen(false)} title="Add a phone">
        {guide ? (
          <div class="flex flex-col items-start gap-4">
            <img
              data-phone-qr=""
              src="/api/phone/qr.svg"
              alt="QR code: the phone's trust guide"
              width={160}
              height={160}
              class="size-40 shrink-0 rounded-lg bg-[var(--ui-qr-bg)] p-2"
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

/** Settings › Security › Devices: every paired device, oldest first, the browser asking named
 *  current, the rest by how long since their last check-in; the removal asks for the passkey when
 *  the server says step-up, and the removed device's browser is signed out server-side by the
 *  removal itself. */
export function Devices(): JSX.Element {
  const { api, toast, now } = useHuddle();
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
    // the removal is a dangerous act: a 401 runs the passkey sheet once and deletes again
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

  const ordered = [...(devices ?? [])].sort((a, b) => Date.parse(a.added) - Date.parse(b.added));

  return (
    <section aria-labelledby="devices-h" data-devices="" class="flex flex-col gap-1">
      {sheet}
      <h2 id="devices-h" class={HEAD}>
        Devices{ordered.length > 0 ? ` · ${ordered.length}` : ""}
      </h2>
      {error ? (
        <p role="alert" class={ERR}>
          {error}
        </p>
      ) : null}
      {devices === null ? (
        <p class={NOTE}>Loading…</p>
      ) : ordered.length === 0 ? (
        <p class={NOTE}>No devices paired yet.</p>
      ) : (
        <List label="Paired devices">
          {ordered.map((d) => (
            <ListItem
              key={d.name}
              title={d.name}
              meta={
                <span class="tabular-nums">
                  {d.current ? "This device · active now" : freshness(d.seen, now)}
                </span>
              }
              trailing={
                <span data-device={d.name}>
                  {d.current ? (
                    <span data-this-device="" class="text-xs text-[var(--ui-muted)]">
                      This device
                    </span>
                  ) : (
                    <Button
                      variant="ghost"
                      class="text-[var(--ui-fail-ink)]"
                      onClick={() => void remove(d.name)}
                    >
                      Remove
                    </Button>
                  )}
                </span>
              }
            />
          ))}
        </List>
      )}
      <p class={NOTE}>A device signs out on its own after 7 days unused.</p>
    </section>
  );
}

/** Settings › Security › Sign-in methods: two rows — this browser's passkey and the authenticator
 *  code (one enrolment per hub) — each opening its pane in a sheet; the passkey pane offers to save
 *  this browser one when its session rides a code, and the authenticator pane holds the enrolment's
 *  set-up and removal. */
export function SignInMethods(): JSX.Element {
  const { api, toast } = useHuddle();
  const [facts, setFacts] = useState<{
    enrolled: boolean;
    kind: string;
    passkey: boolean;
    device: string;
  } | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  /** The error the save itself said, inside the passkey pane. */
  const [saveError, setSaveError] = useState("");
  const [pane, setPane] = useState<"passkey" | "authenticator" | null>(null);

  const alive = useRef(true);
  const load = (): void => {
    api
      .api("/api/whoami")
      .then((r) => {
        if (!alive.current) return;
        const w = r as { totp?: unknown; kind?: unknown; device_passkey?: unknown; device?: unknown };
        setFacts({
          enrolled: w.totp === true,
          kind: typeof w.kind === "string" ? w.kind : "",
          passkey: w.device_passkey === true,
          device: typeof w.device === "string" ? w.device : "",
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
    setSaveError("");
    try {
      const r = (await api.api("/api/passkey/begin", { body: {} })) as { options?: unknown };
      let cred: PublicKeyCredential;
      try {
        cred = (await navigator.credentials.create({
          publicKey: creationFromJSON(r.options).publicKey,
        })) as PublicKeyCredential;
      } catch {
        setSaveError("The passkey was not created. Try again.");
        return;
      }
      await api.api("/api/passkey/finish", { body: { response: credentialToJSON(cred) } });
      toast("Passkey saved for this browser");
      setPane(null);
      load();
    } catch (e) {
      setSaveError(errText(e));
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
        error ? null : (
          <p class={NOTE}>Loading…</p>
        )
      ) : (
        <>
          <List label="Sign-in methods">
            <ListItem
              title="Passkey"
              meta={facts.passkey ? `On · ${facts.device}` : "Off"}
              onClick={() => setPane("passkey")}
            />
            <ListItem
              title="Authenticator app"
              meta={facts.enrolled ? "On" : "Off"}
              onClick={() => setPane("authenticator")}
            />
          </List>
          <p class={NOTE}>Removing the authenticator asks for your passkey first.</p>
          <Sheet open={pane === "passkey"} onClose={() => setPane(null)} title="Passkey">
            <div data-passkey-row="" class="flex flex-col gap-4">
              <p class={NOTE}>
                {facts.kind === "device"
                  ? facts.passkey
                    ? "This browser signs in with its passkey."
                    : "Not saved. This device's lock would sign in without a code."
                  : "A passkey is saved per browser: sign in with a code first, then accept the offer."}
              </p>
              {saveError ? (
                <p role="alert" class={ERR}>
                  {saveError}
                </p>
              ) : null}
              {facts.kind === "device" && !facts.passkey ? (
                <Button disabled={busy} onClick={() => void savePasskey()}>
                  Save passkey
                </Button>
              ) : null}
            </div>
          </Sheet>
          <Sheet open={pane === "authenticator"} onClose={() => setPane(null)} title="Authenticator app">
            <AuthenticatorCard enrolled={facts.enrolled} onChanged={load} />
          </Sheet>
        </>
      )}
    </section>
  );
}

/** One group of the Settings page. */
export type SettingsSection = "appearance" | "notifications" | "security" | "spending" | "tips" | "hub";

/** One setting a group page draws; its id is an address of its own that opens its group. */
type SettingsMember = { id: string; Pane: () => JSX.Element };

/** One group: its address, its name, its glyph and its members in page order. */
type SettingsGroup = {
  id: SettingsSection;
  label: string;
  icon: IconName;
  members: readonly SettingsMember[];
};

/** The six groups in order. The phone's list, the sidebar and the pages all read this table. */
const SETTINGS_GROUPS: readonly SettingsGroup[] = [
  {
    id: "appearance",
    label: "Appearance",
    icon: "sun",
    members: [{ id: "appearance", Pane: Appearance }],
  },
  {
    id: "notifications",
    label: "Notifications",
    icon: "bell",
    members: [{ id: "notifications", Pane: NotificationsCard }],
  },
  {
    id: "security",
    label: "Security",
    icon: "key",
    members: [
      { id: "devices", Pane: Devices },
      { id: "phone", Pane: Phone },
      { id: "signin", Pane: SignInMethods },
      { id: "signout", Pane: SignOut },
    ],
  },
  {
    id: "spending",
    label: "Spending",
    icon: "activity",
    members: [{ id: "radar", Pane: RadarSettings }],
  },
  {
    id: "tips",
    label: "Tips",
    icon: "sparkle",
    members: [{ id: "insights", Pane: InsightsSettings }],
  },
  {
    id: "hub",
    label: "Hub",
    icon: "monitor",
    members: [
      { id: "hub", Pane: ThisHub },
      { id: "zone", Pane: () => <TimeZone /> },
      { id: "capture", Pane: CaptureSetting },
      { id: "selfcheck", Pane: SelfCheck },
    ],
  },
];

/** One card of the phone's list: its heading and the groups whose rows it holds. */
type SettingsCard = { heading: string; groups: readonly SettingsSection[] };

/** The phone list's three cards, in order. */
const SETTINGS_CARDS: readonly SettingsCard[] = [
  { heading: "Appearance and notifications", groups: ["appearance", "notifications"] },
  { heading: "Security and spending", groups: ["security", "spending"] },
  { heading: "Tips and hub", groups: ["tips", "hub"] },
];

/** The group the address names, or the group holding the member it names; a missing or unknown
 *  one reads as the first. */
export function settingsSectionOf(named: string | undefined): SettingsSection {
  const group = SETTINGS_GROUPS.find(
    (g) => g.id === named || g.members.some((member) => member.id === named),
  );
  return group?.id ?? "appearance";
}

/** The phone's header over a group page: the back link to the list above the group's name. */
export function SettingsHead({ section }: { section: SettingsSection }): JSX.Element {
  return (
    <div class="flex min-w-0 flex-col">
      <a
        href="#/settings"
        aria-label="Back to Settings"
        class="-ml-2 flex min-h-[var(--ui-tap)] min-w-[var(--ui-tap)] items-center gap-1 self-start rounded-[var(--ui-radius)] pr-2 text-sm font-semibold text-[var(--ui-accent)] outline-none focus-visible:ring-2 focus-visible:ring-[var(--ui-accent)]"
      >
        <Icon name="left" class="size-5" />
        Settings
      </a>
      <h1 class="truncate text-[length:var(--ui-text-xl)] font-semibold tracking-tight">
        {SETTINGS_GROUPS.find((g) => g.id === section)?.label}
      </h1>
    </div>
  );
}

/** The phone's Settings page: the hub card, then three cards of rows, one row per group with the
 *  group's current value, each row opening its group's page; the footnote says what is kept
 *  where. */
function GroupList(): JSX.Element {
  const { api } = useHuddle();
  // the hub card's facts: /health answering is the card's Healthy, whoami names this device
  const [facts, setFacts] = useState<HubFacts | null>(null);
  const [device, setDevice] = useState("");
  useEffect(() => {
    let alive = true;
    api
      .api("/health")
      .then((r) => {
        if (alive) setFacts(hubFactsOf(r));
      })
      .catch(() => {});
    api
      .api("/api/whoami")
      .then((r) => {
        const d = (r as { device?: unknown }).device;
        if (alive && typeof d === "string") setDevice(d);
      })
      .catch(() => {});
    return () => {
      alive = false;
    };
  }, [api]);
  const values = useSettingsValues();
  return (
    <div class="min-h-0 flex-1 overflow-y-auto">
      <div class="flex flex-col px-4 pb-4 pt-4">
        <a
          href="#/settings/hub"
          data-hub-card=""
          class="flex min-h-13 items-center gap-3 rounded-[var(--ui-radius-lg)] bg-[var(--ui-surface)] p-4 shadow-[var(--ui-shadow-1)] outline-none focus-visible:ring-2 focus-visible:ring-[var(--ui-accent)]"
        >
          <span
            data-hub-initial=""
            class="flex size-11 shrink-0 items-center justify-center rounded-[var(--ui-radius-sm)] bg-[var(--ui-accent-soft)] text-[length:var(--ui-text-lg)] font-bold text-[var(--ui-accent)]"
          >
            {hubInitial(facts?.name ?? "")}
          </span>
          <span class="min-w-0 flex-1">
            <span
              data-hub-name=""
              class="block truncate text-[length:var(--ui-text-md)] font-semibold text-[var(--ui-text)]"
            >
              {facts?.name ?? ""}
            </span>
            <span
              data-hub-subtitle=""
              class="block truncate text-[length:var(--ui-text-sm)] text-[var(--ui-muted)]"
            >
              {hubSubtitle(facts?.version ?? "", device)}
            </span>
          </span>
          {facts !== null ? (
            <span
              data-hub-healthy=""
              class="flex shrink-0 items-center gap-1.5 text-[length:var(--ui-text-sm)] text-[var(--ui-ok-ink)]"
            >
              <span class="size-2 rounded-full bg-[var(--ui-ok)]" aria-hidden="true" />
              Healthy
            </span>
          ) : null}
        </a>
        <nav aria-label="Settings sections" data-settings="" class="flex flex-col">
          {SETTINGS_CARDS.map((card) => (
            <section key={card.heading}>
              <h2
                data-card-heading=""
                class="m-0 mb-2 ms-1 mt-6 text-[length:var(--ui-text-sm)] font-semibold text-[var(--ui-muted)]"
              >
                {card.heading}
              </h2>
              <div class="overflow-hidden rounded-[var(--ui-radius)] bg-[var(--ui-surface)] shadow-[var(--ui-shadow-1)]">
                <ul class="m-0 list-none p-0">
                  {SETTINGS_GROUPS.filter((g) => card.groups.includes(g.id)).map((group, i) => (
                    <li key={group.id}>
                      {i > 0 ? <div class="ms-14 h-px bg-[var(--ui-line)]" /> : null}
                      <a
                        href={`#/settings/${group.id}`}
                        data-section={group.id}
                        class="flex min-h-13 items-center gap-3 px-4 outline-none hover:bg-[var(--ui-surface-2)] focus-visible:ring-2 focus-visible:ring-[var(--ui-accent)]"
                      >
                        <span class="flex size-7 shrink-0 items-center justify-center rounded-[var(--ui-radius-sm)] bg-[var(--ui-accent-soft)] text-[var(--ui-accent)]">
                          <Icon name={group.icon} class="size-4" />
                        </span>
                        <span
                          data-label=""
                          class="min-w-0 flex-1 truncate text-[length:var(--ui-text-md)] font-medium text-[var(--ui-text)]"
                        >
                          {group.label}
                        </span>
                        <span
                          data-value={group.id}
                          class="shrink-0 text-[length:var(--ui-text-md)] text-[var(--ui-muted)]"
                        >
                          {values[group.id] ?? ""}
                        </span>
                        <Icon name="right" class="size-3.5 shrink-0 text-[var(--ui-muted)]" />
                      </a>
                    </li>
                  ))}
                </ul>
              </div>
            </section>
          ))}
        </nav>
        <p data-footnote="" class="m-0 mt-2 text-[length:var(--ui-text-xs)] text-[var(--ui-muted)]">
          Theme and appearance are kept in this browser. Everything else is kept on the hub.
        </p>
      </div>
    </div>
  );
}

/** One group's page: its members in order. */
function GroupPage({ section }: { section: SettingsSection }): JSX.Element {
  return (
    <div class="min-h-0 flex-1 overflow-y-auto">
      <div class="mx-auto w-full max-w-3xl px-4 py-6 sm:px-6" data-settings="" data-group={section}>
        <div class="flex flex-col gap-8">
          {SETTINGS_GROUPS.find((g) => g.id === section)?.members.map(({ id, Pane }) => (
            <div key={id} data-setting={id}>
              <Pane />
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}

/** The Settings page. On the phone an address with no group is the grouped list and a group's
 *  address is its page alone (the shell's header carries the back link); the tablet and the
 *  desktop keep the groups as a sidebar beside the named group's page, the first when none is
 *  named. */
export function SettingsTab({ section }: { section: SettingsSection | null }): JSX.Element {
  const layout = useLayout();
  if (layout === "phone") return section === null ? <GroupList /> : <GroupPage section={section} />;
  const open = section ?? "appearance";
  return (
    <div class="flex min-h-0 flex-1">
      <nav
        aria-label="Settings sections"
        class="flex w-56 shrink-0 flex-col gap-0.5 overflow-y-auto border-r border-[var(--ui-line)] p-3"
      >
        {SETTINGS_GROUPS.map((group) => (
          <a
            key={group.id}
            href={`#/settings/${group.id}`}
            data-section={group.id}
            aria-current={group.id === open ? "page" : undefined}
            class={`flex min-h-[var(--ui-tap)] items-center gap-2.5 rounded-[var(--ui-radius)] px-3 text-sm outline-none focus-visible:ring-2 focus-visible:ring-[var(--ui-accent)] ${
              group.id === open
                ? "bg-[var(--ui-surface-2)] font-semibold text-[var(--ui-text)]"
                : "text-[var(--ui-muted)] hover:bg-[var(--ui-surface-2)] hover:text-[var(--ui-text)]"
            }`}
          >
            <Icon name={group.icon} class="size-4 shrink-0" />
            {group.label}
          </a>
        ))}
      </nav>
      <div class="flex min-h-0 min-w-0 flex-1 flex-col">
        <GroupPage section={open} />
      </div>
    </div>
  );
}
