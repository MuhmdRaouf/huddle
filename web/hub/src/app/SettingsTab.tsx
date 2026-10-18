// SettingsTab.tsx — the Settings tab: one section per settings surface, as underline tabs like
// the Radar tab's sub-tabs. Radar's own settings (budgets, notifications, history retention)
// live here; the surfaces later cards add (theme, machines, allow lists, the hub address) join
// as their own sections.

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
import { AuthenticatorCard } from "./Totp.tsx";

/** The sections the Settings tab serves, in row order; each later surface adds its entry. */
const SECTIONS = [
  { id: "signin", label: "Sign-in" },
  { id: "radar", label: "Radar" },
  { id: "devices", label: "Devices" },
  { id: "channels", label: "Linked channels" },
  { id: "notifications", label: "Notifications" },
] as const;

/** One section of the Settings tab. */
export type SettingsSection = (typeof SECTIONS)[number]["id"];

/** The section the address names; a missing or unknown one reads as the first. */
export function settingsSectionOf(named: string | undefined): SettingsSection {
  return SECTIONS.some((entry) => entry.id === named) ? (named as SettingsSection) : "signin";
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

/** The phone guide the server's "Add a phone" started: where it answers and the root's
 *  fingerprint, which the phone's page shows too. */
type PhoneGuide = { url: string; fingerprint: string; expires_in: number };

/** The "Add a phone" card: starts the guide the server serves (10 minutes, loopback-bound QR)
 *  and shows its QR and the fingerprint to compare with the phone's page. */
function AddAPhone(): JSX.Element {
  const { api, toast } = useHuddle();
  const [guide, setGuide] = useState<PhoneGuide | null>(null);
  const [busy, setBusy] = useState(false);

  const start = async (): Promise<void> => {
    setBusy(true);
    try {
      const r = await api.api("/api/phone", { body: { action: "start" } });
      setGuide(r as PhoneGuide);
    } catch (e) {
      toast(errText(e), { bad: true });
    } finally {
      setBusy(false);
    }
  };

  return (
    <div class="mt-6 flex flex-col gap-1.5">
      <h3 class="m-0 text-[15px] font-semibold">Add a phone</h3>
      <p class="m-0 text-sm muted">
        A phone trusts the hub's root once: it scans a QR this computer serves for 10 minutes, and the page it
        opens walks the taps.
      </p>
      {guide ? (
        <div class="flex flex-wrap items-start gap-4">
          <img
            data-phone-qr=""
            src="/api/phone/qr.svg"
            alt="QR code: the phone's trust guide"
            width={160}
            height={160}
            class="size-40 shrink-0 rounded-lg bg-white p-2"
          />
          <div class="flex min-w-0 flex-col gap-1.5">
            <code class="break-all font-mono text-xs">{guide.fingerprint}</code>
            <span class="text-xs muted">
              Scan with the phone's camera. The page it opens shows the same fingerprint — compare before
              installing. The guide stops itself after 10 minutes.
            </span>
          </div>
        </div>
      ) : (
        <button
          type="button"
          class="btn btn-sm btn-primary w-fit"
          disabled={busy}
          onClick={() => void start()}
        >
          {busy ? "Starting…" : "Add a phone"}
        </button>
      )}
    </div>
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
      <AddAPhone />
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
      <h2 id="signin-h" class="mb-1.5 text-xs font-semibold tracking-[0.06em] uppercase muted">
        Sign-in methods
      </h2>
      {error ? (
        <p role="alert" class="m-0 text-sm text-error">
          {error}
        </p>
      ) : null}
      {facts === null ? (
        <p class="m-0 text-sm muted">Loading…</p>
      ) : (
        <>
          <AuthenticatorCard enrolled={facts.enrolled} onChanged={load} />
          <div class="flex items-center justify-between gap-3 border-b hairline py-3" data-passkey-row="">
            <div class="flex min-w-0 flex-col">
              <span class="text-[15px]">Passkey for this browser</span>
              <span class="text-xs muted">
                {facts.kind === "device"
                  ? facts.passkey
                    ? "This browser signs in with its passkey."
                    : "Not saved. This device's lock would sign in without a code."
                  : "A passkey is saved per browser: sign in with a code first, then accept the offer."}
              </span>
            </div>
            {facts.kind === "device" && !facts.passkey ? (
              <button
                type="button"
                class="btn btn-primary btn-sm"
                disabled={busy}
                onClick={() => void savePasskey()}
              >
                Save passkey
              </button>
            ) : null}
          </div>
          <p class="m-0 pt-2 text-xs muted">
            Every device session ends after 7 days unused; one code or touch starts a fresh one. Manage the
            devices themselves under Devices.
          </p>
        </>
      )}
    </section>
  );
}

/** The Settings tab: the section row, then the named section's panes. The row is the Radar
 *  views' own shape: underlined tabs at the desk, pills that scroll inside the row on the
 *  narrower layouts, so the five sections never push the page sideways. */
export function SettingsTab({ section }: { section: SettingsSection }): JSX.Element {
  const layout = useLayout();
  return (
    <div class="flex min-h-0 flex-1 flex-col">
      <nav
        aria-label="Settings sections"
        class={
          layout === "desktop"
            ? "flex min-h-[52px] shrink-0 items-center gap-0.5 border-b hairline px-5"
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
                ? `flex h-full shrink-0 items-center border-b-2 px-3.5 text-sm outline-none focus-visible:ring-2 focus-visible:ring-primary ${
                    entry.id === section
                      ? "border-primary font-semibold text-base-content"
                      : "border-transparent text-base-content/70 hover:text-base-content"
                  }`
                : `flex min-h-11 shrink-0 items-center rounded-full px-3.5 text-sm outline-none focus-visible:ring-2 focus-visible:ring-primary ${
                    entry.id === section
                      ? "bg-primary font-semibold text-base-300"
                      : "border hairline text-base-content/80 hover:bg-base-100"
                  }`
            }
          >
            {entry.label}
          </a>
        ))}
      </nav>
      <div class="min-h-0 flex-1 overflow-y-auto">
        <div class="mx-auto w-full max-w-3xl px-4 py-6 sm:px-6">
          {section === "signin" ? (
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
