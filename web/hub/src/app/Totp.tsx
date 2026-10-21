// Totp.tsx — the authenticator's three faces: the set-up both the first-run page and Settings
// open (the QR and the secret, the confirming code, the recovery codes shown this one time),
// the full page a link sign-in lands on when the hub holds no enrolment yet, and the sheet that
// offers a code-signed-in browser a passkey of its own. The QR rows the server sends become an
// SVG here: no script, no canvas, nothing but rects.

import { Dialog } from "@huddle/ui/dialog.tsx";
import type { JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import type { ApiError } from "../api.ts";
import { Icon, Logo } from "../icons.tsx";
import { creationFromJSON, credentialToJSON } from "../passkey.ts";
import { errText, useHuddle } from "./context.tsx";
import { CodeField } from "./CodeField.tsx";
import { useStepUp } from "./Needs.tsx";

/** What a begin answers: the otpauth URI, the secret for manual entry, the QR rows. */
type BeginAnswer = { uri?: unknown; secret?: unknown; qr?: unknown };

/** The QR rows as strings of 1 (dark) and 0 (light), or nothing when the answer was short. */
function qrRows(v: unknown): string[] {
  return Array.isArray(v) ? v.filter((row): row is string => typeof row === "string") : [];
}

/** The secret in groups of four, the way an app's manual entry reads it. */
function grouped(secret: string): string {
  return (secret.match(/.{1,4}/g) ?? [secret]).join(" ");
}

/** The QR as one SVG: one rect per horizontal run of dark modules, on the white the scan needs. */
function QRCode({ rows, label }: { rows: string[]; label: string }): JSX.Element | null {
  if (rows.length === 0 || rows.some((row) => row.length !== rows.length)) return null;
  const n = rows.length;
  const runs: { x: number; y: number; w: number }[] = [];
  rows.forEach((row, y) => {
    let x = 0;
    while (x < n) {
      if (row[x] !== "1") {
        x++;
        continue;
      }
      let end = x;
      while (end < n && row[end] === "1") end++;
      runs.push({ x, y, w: end - x });
      x = end;
    }
  });
  return (
    <div class="inline-flex rounded-xl bg-white p-3" data-qr="">
      <svg
        viewBox={`0 0 ${n} ${n}`}
        shapeRendering="crispEdges"
        role="img"
        aria-label={label}
        class="w-full max-w-[216px]"
      >
        <rect width={n} height={n} fill="#fff" />
        {runs.map((r) => (
          <rect key={`${r.x}-${r.y}`} x={r.x} y={r.y} width={r.w} height={1} fill="#0f172a" />
        ))}
      </svg>
    </div>
  );
}

/** The set-up's two stages: the QR and the code, then the recovery codes. `onDone` runs when the
 * owner saved them (or the caller's own way out ran). A begin that asks for a step-up (a
 * replacement) runs the passkey sheet once and begins again. */
export function TotpSetup({ onDone }: { onDone: () => void }): JSX.Element {
  const { api, copy, toast } = useHuddle();
  const { ask, sheet } = useStepUp();
  const [begin, setBegin] = useState<BeginAnswer | null>(null);
  const [recovery, setRecovery] = useState<string[] | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const beginEnrol = async (): Promise<void> => {
    setBusy(true);
    setError("");
    try {
      const r = (await api.api("/api/totp/begin", { body: {} })) as BeginAnswer;
      setBegin(r);
    } catch (e) {
      const err = e as ApiError;
      if (err.status === 401 && err.message === "step-up required") {
        if (!(await ask({ action: "Replace the authenticator" }))) return;
        try {
          const r = (await api.api("/api/totp/begin", { body: {} })) as BeginAnswer;
          setBegin(r);
        } catch (e2) {
          setError(errText(e2));
        }
        return;
      }
      setError(errText(e));
    } finally {
      setBusy(false);
    }
  };

  useEffect(() => {
    void beginEnrol();
  }, []);

  const finish = async (code: string): Promise<void> => {
    setBusy(true);
    setError("");
    try {
      const r = (await api.api("/api/totp/finish", { body: { code } })) as { recovery?: unknown };
      const codes = Array.isArray(r.recovery)
        ? r.recovery.filter((c): c is string => typeof c === "string")
        : [];
      setRecovery(codes);
    } catch (e) {
      setError(errText(e));
    } finally {
      setBusy(false);
    }
  };

  if (recovery !== null) {
    const all = recovery.join("\n");
    const download = (): void => {
      try {
        const blob = new Blob([`Huddle recovery codes\n\n${all}\n`], { type: "text/plain" });
        const a = document.createElement("a");
        a.href = URL.createObjectURL(blob);
        a.download = "huddle-recovery-codes.txt";
        a.click();
        URL.revokeObjectURL(a.href);
      } catch {
        toast("Could not build the file. Copy the codes instead.", { bad: true });
      }
    };
    return (
      <div class="flex flex-col gap-4" data-totp-stage="recovery">
        <div>
          <h2 class="text-lg font-semibold">Save your recovery codes</h2>
          <p class="m-0 mt-1 text-sm muted">
            Each code works once when your authenticator is not at hand. They show this one time.
          </p>
        </div>
        <ul
          data-recovery=""
          class="m-0 grid list-none grid-cols-2 gap-1.5 rounded-box bg-base-200 p-4 font-mono text-sm sm:grid-cols-2"
        >
          {recovery.map((code) => (
            <li key={code} class="tnum">
              {code}
            </li>
          ))}
        </ul>
        <div class="flex flex-wrap gap-2">
          <button type="button" class="btn" onClick={() => copy(all, "Recovery codes copied")}>
            <Icon name="copy" />
            Copy
          </button>
          <button type="button" class="btn" onClick={download}>
            <Icon name="download" />
            Download .txt
          </button>
          <button type="button" class="btn btn-primary ml-auto w-full sm:w-auto" onClick={onDone}>
            I saved them, continue
          </button>
        </div>
      </div>
    );
  }

  const secret = typeof begin?.secret === "string" ? begin.secret : "";
  return (
    <div class="flex flex-col gap-4" data-totp-stage="code">
      {sheet}
      <div class="flex flex-col items-start gap-4 sm:flex-row sm:items-center">
        {begin ? <QRCode rows={qrRows(begin.qr)} label="Authenticator QR code" /> : null}
        <div class="flex min-w-0 flex-col gap-2">
          <p class="m-0 text-sm muted">
            Scan the code with your authenticator app (Google Authenticator, 1Password, Authy), or type the
            secret by hand.
          </p>
          {secret ? (
            <div class="flex items-center gap-2">
              <code
                data-secret=""
                class="min-w-0 flex-1 truncate rounded-lg bg-base-200 px-3 py-2 font-mono text-sm"
              >
                {grouped(secret)}
              </code>
              <button
                type="button"
                class="btn btn-ghost btn-square btn-sm muted"
                aria-label="Copy the secret"
                onClick={() => copy(secret, "Secret copied")}
              >
                <Icon name="copy" />
              </button>
            </div>
          ) : null}
        </div>
      </div>
      <div>
        <p class="mb-1.5 text-sm font-medium">Enter the 6-digit code the app shows</p>
        <CodeField onCode={(code) => void finish(code)} busy={busy} error={error} />
      </div>
    </div>
  );
}

/** The first-run page a link sign-in lands on when the hub holds no enrolment: the set-up
 * before the shell, with the one skip the card allows. */
export function TotpFirstRun({ onDone, onLater }: { onDone: () => void; onLater: () => void }): JSX.Element {
  return (
    <main class="grid min-h-dvh place-items-center bg-base-200 p-4">
      <div class="flex w-full max-w-lg flex-col gap-8" data-totp-first-run="">
        <div class="flex flex-col items-center gap-3 text-center">
          <Logo />
          <h1 class="text-3xl font-bold tracking-tight">Set up your authenticator app</h1>
          <p class="text-[15px] muted">
            This is how you sign in to Huddle from any browser: a code from your app, no terminal and no link.
            It takes a minute.
          </p>
        </div>
        <div class="rounded-box bg-base-100 p-6">
          <TotpSetup onDone={onDone} />
        </div>
        <button type="button" class="btn btn-ghost btn-sm mx-auto" data-totp-later="" onClick={onLater}>
          Later — sign in with a link instead
        </button>
      </div>
    </main>
  );
}

/** The Settings › Sign-in panel's authenticator half: what is enrolled, the set-up when nothing
 * is, and the removal that ends every session a code minted. */
export function AuthenticatorCard({
  enrolled,
  onChanged,
}: {
  enrolled: boolean;
  onChanged: () => void;
}): JSX.Element {
  const { api, toast } = useHuddle();
  const { ask, sheet } = useStepUp();
  const [setting, setSetting] = useState(false);
  const [busy, setBusy] = useState(false);

  const remove = async (): Promise<void> => {
    // the removal is a dangerous act: a 401 runs the passkey sheet once and removes again
    const drop = async (): Promise<void> => {
      await api.api("/api/totp", { method: "DELETE" });
      toast("Authenticator sign-in removed; its sessions ended");
      onChanged();
    };
    setBusy(true);
    try {
      try {
        await drop();
      } catch (e) {
        const err = e as ApiError;
        if (err.status !== 401 || err.message !== "step-up required") {
          toast(errText(e), { bad: true });
          return;
        }
        if (!(await ask({ action: "Remove the authenticator" }))) return;
        try {
          await drop();
        } catch (e2) {
          toast(errText(e2), { bad: true });
        }
      }
    } finally {
      setBusy(false);
    }
  };

  if (setting) {
    return (
      <div class="flex flex-col gap-3">
        {sheet}
        <TotpSetup
          onDone={() => {
            setSetting(false);
            onChanged();
          }}
        />
      </div>
    );
  }
  return (
    <div class="flex items-center justify-between gap-3 border-b hairline py-3" data-totp-row="">
      {sheet}
      <div class="flex min-w-0 flex-col">
        <span class="text-[15px]">Authenticator app</span>
        <span class="text-xs muted">
          {enrolled
            ? "A code from your app signs any browser in."
            : "Not set up. A code from your app would sign any browser in."}
        </span>
      </div>
      {enrolled ? (
        <button
          type="button"
          class="btn btn-ghost btn-sm text-error"
          disabled={busy}
          onClick={() => void remove()}
        >
          Remove
        </button>
      ) : (
        <button type="button" class="btn btn-primary btn-sm" disabled={busy} onClick={() => setSetting(true)}>
          Set up
        </button>
      )}
    </div>
  );
}

/** The lock each platform names, in its own word: the name the biometric button carries. */
export function bioName(ua: string): string {
  if (ua.includes("iPhone") || ua.includes("iPad")) return "Face ID";
  if (ua.includes("Mac")) return "Touch ID";
  if (ua.includes("Android")) return "your fingerprint";
  return "this device's lock";
}

/** The lock each platform names, in its own word. */
export function bioLabel(ua: string): { title: string } {
  if (ua.includes("iPhone") || ua.includes("iPad")) return { title: "Use Face ID next time?" };
  if (ua.includes("Mac")) return { title: "Use Touch ID next time?" };
  if (ua.includes("Android")) return { title: "Use your fingerprint next time?" };
  return { title: "Use this device's lock next time?" };
}

/** The sheet a code sign-in earns once: turn this browser's own lock into a passkey, or answer
 * not now and be asked again in thirty days. The code keeps working either way. */
export function PasskeyOffer({
  open,
  onDone,
  onClose,
}: {
  open: boolean;
  onDone: () => void;
  onClose: () => void;
}): JSX.Element {
  const { api } = useHuddle();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const word = bioLabel(navigator.userAgent);

  const turnOn = async (): Promise<void> => {
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
      onDone();
    } catch (e) {
      setError(errText(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onClose={onClose} title={word.title} labeledBy="passkey-offer-h">
      <div class="flex flex-col gap-4" data-passkey-offer="">
        <p class="m-0 text-sm muted">
          Sign in next time with this device's lock instead of a code. The code keeps working as the fallback.
        </p>
        {error ? (
          <p role="alert" class="m-0 text-sm text-error">
            {error}
          </p>
        ) : null}
        <div class="flex justify-end gap-2">
          <button type="button" class="btn btn-ghost" disabled={busy} onClick={onClose}>
            Not now
          </button>
          <button type="button" class="btn btn-primary" disabled={busy} onClick={() => void turnOn()}>
            Turn on
          </button>
        </div>
      </div>
    </Dialog>
  );
}
