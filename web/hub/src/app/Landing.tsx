// Landing.tsx — the page a browser with no session lands on: three clear paths beside each
// other, sign-in the default. The sign-in card leads with what this home and this browser can
// do — the biometric button when this browser holds a passkey, the authenticator code field
// otherwise, the first-time card when nothing is enrolled yet — with the one-time-link commands
// one disclosure away and the server's reason line only when a cookie was sent and refused. The
// start-a-session card and the four-step tour fill the rest of the width, and a theme toggle
// rides the head.

import { Dialog } from "@huddle/ui/dialog.tsx";
import { Panel } from "@huddle/ui/page.tsx";
import type { ComponentChildren, JSX } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import { ConnectCard } from "./ConnectCard.tsx";
import { CodeField } from "./CodeField.tsx";
import { errText, useHuddle } from "./context.tsx";
import { Icon, Logo } from "../icons.tsx";
import { assertionFromJSON, credentialToJSON, markPasskeyHere, passkeyHere } from "../passkey.ts";
import { bioName } from "./Totp.tsx";
import { currentTheme, setTheme, type ThemePref } from "./theme.ts";

/** The tour's four steps: the tab it shows, the picture it carries, the one sentence it says. */
const TOUR: readonly (readonly [key: string, label: string, sentence: string])[] = [
  ["now", "Now", "Every session at work, the ones that need you first."],
  ["channels", "Channels", "One channel per piece of shared work, its plan and its knowledge."],
  ["radar", "Radar", "Requests, tokens and cost across every model, with budgets."],
  ["kanban", "Kanban", "The board the sessions claim cards from and move."],
];

/** A read-only command with a copy button at the end. */
function CmdField({ id, label, cmd }: { id: string; label: string; cmd: string }): JSX.Element {
  return (
    <div class="flex flex-col gap-1.5">
      <span class="text-sm font-medium" id={`${id}-l`}>
        {label}
      </span>
      {/* biome-ignore lint/a11y/useAriaPropsSupportedByRole: the cmd field's labelled box */}
      <div
        class="flex h-10 items-center gap-2.5 rounded-lg border border-base-content/15 bg-base-200 pr-1 pl-3"
        aria-labelledby={`${id}-l`}
      >
        <span class="muted">
          <Icon name="terminal" />
        </span>
        <code class="min-w-0 flex-1 truncate text-sm">{cmd}</code>
        <CopyButton text={cmd} done="" ariaLabel={`Copy ${cmd}`} />
      </div>
    </div>
  );
}

/** A copy button that flashes a check and a word for a moment when the copy landed. */
function CopyButton({
  text,
  done,
  ariaLabel,
  class: c = "btn btn-ghost btn-square btn-sm muted",
  children,
}: {
  text: string;
  done: string;
  ariaLabel?: string | undefined;
  class?: string | undefined;
  children?: ComponentChildren;
}): JSX.Element {
  const [hit, setHit] = useState(false);
  useEffect(() => {
    if (!hit) return;
    const t = setTimeout(() => setHit(false), 1600);
    return () => clearTimeout(t);
  }, [hit]);
  const copy = (): void => {
    navigator.clipboard?.writeText(text).then(
      () => setHit(true),
      () => {},
    );
  };
  return (
    <button type="button" class={c} aria-label={ariaLabel} onClick={copy}>
      {hit ? (
        <>
          <Icon name="check" />
          {done}
        </>
      ) : (
        children
      )}
    </button>
  );
}

/** The light/dark switch the landing's head carries: one click flips the resolved variant. */
function ThemeToggle(): JSX.Element {
  const [pref, setPref] = useState<ThemePref>(() => currentTheme(localStorage));
  const dark =
    pref.mode === "dark" ||
    (pref.mode === "system" && window.matchMedia("(prefers-color-scheme: dark)").matches);
  return (
    <button
      type="button"
      class="btn btn-ghost btn-square btn-sm"
      data-theme-toggle=""
      aria-label={dark ? "Switch to the light theme" : "Switch to the dark theme"}
      onClick={() => {
        const next: ThemePref = { family: pref.family, mode: dark ? "light" : "dark" };
        setTheme(localStorage, next, window.matchMedia("(prefers-color-scheme: dark)").matches);
        setPref(next);
      }}
    >
      <Icon name={dark ? "sun" : "moon"} class="size-4.5" />
    </button>
  );
}

/** The tour's strip: one thumbnail and one sentence per tab, the lightbox a click away. */
function TourStrip({ onOpen }: { onOpen: (at: number) => void }): JSX.Element {
  return (
    <section aria-labelledby="tour-h" data-tour="" class="flex flex-col gap-4">
      <h2 id="tour-h" class="text-lg font-semibold tracking-tight">
        See how it works
      </h2>
      <ol class="m-0 grid list-none grid-cols-2 gap-4 p-0 lg:grid-cols-4">
        {TOUR.map(([key, label, sentence], i) => (
          <li key={key} class="flex flex-col gap-2">
            <button
              type="button"
              class="overflow-hidden rounded-xl border hairline bg-base-100 outline-none focus-visible:ring-2 focus-visible:ring-primary"
              aria-label={`${label}: open the tour step`}
              onClick={() => onOpen(i)}
            >
              <img
                src={`/static/tour/${key}.jpg`}
                alt={`${label} tab`}
                width="960"
                height="500"
                loading="lazy"
                class="block w-full"
              />
            </button>
            <p class="m-0 text-[13px] font-semibold">{label}</p>
            <p class="m-0 text-[13px] muted">{sentence}</p>
          </li>
        ))}
      </ol>
    </section>
  );
}

/** The tour's lightbox: the step's picture large, the arrows stepping through, Escape closing. */
function TourLightbox({
  at,
  onClose,
  onStep,
}: {
  at: number;
  onClose: () => void;
  onStep: (to: number) => void;
}): JSX.Element {
  const step = TOUR[at] ?? TOUR[0];
  if (!step) return <></>;
  const [key, label, sentence] = step;
  return (
    <Dialog open onClose={onClose} title={`${label} (${at + 1} of ${TOUR.length})`} labeledBy="tour-box-h">
      <div class="flex flex-col gap-3" data-tour-box="">
        <img
          src={`/static/tour/${key}.jpg`}
          alt={`${label} tab`}
          width="1440"
          height="750"
          class="block w-full rounded-lg border hairline"
        />
        <p class="m-0 text-sm muted">{sentence}</p>
        <div class="flex items-center justify-between">
          <button
            type="button"
            class="btn btn-ghost btn-sm"
            onClick={() => onStep((at - 1 + TOUR.length) % TOUR.length)}
          >
            <Icon name="left" class="size-4" />
            Previous
          </button>
          <button type="button" class="btn btn-primary btn-sm" onClick={() => onStep((at + 1) % TOUR.length)}>
            Next
            <Icon name="right" class="size-4" />
          </button>
        </div>
      </div>
    </Dialog>
  );
}

/** The recovery-code spelling: one word, its own field, Enter or the button submitting it. */
function RecoveryField({
  code,
  busy,
  onCode,
  onInput,
}: {
  code: string;
  busy: boolean;
  onCode: (code: string) => void;
  onInput: (code: string) => void;
}): JSX.Element {
  return (
    <>
      <label class="text-sm font-medium" htmlFor="so-recovery">
        Recovery code
      </label>
      <input
        id="so-recovery"
        class="input h-12 w-full font-mono"
        placeholder="xxxx-xxxx"
        autocomplete="off"
        spellcheck={false}
        value={code}
        disabled={busy}
        onInput={(e) => onInput(e.currentTarget.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && code.trim() !== "") onCode(code.trim());
        }}
      />
      <button
        type="button"
        class="btn btn-primary h-12 w-full text-sm"
        disabled={busy || code.trim() === ""}
        onClick={() => onCode(code.trim())}
      >
        Sign in
      </button>
    </>
  );
}

/** The card a hub with nothing enrolled yet shows: the setup-link commands as the way in. */
function FirstTimeCard(): JSX.Element {
  return (
    <div class="flex flex-col gap-3" data-first-time="">
      <p class="m-0 text-sm">
        First time here? Run <code>/huddle:setup</code> or <code>huddle open</code> for your setup link, then
        open the link it prints.
      </p>
      <CmdField id="so-c" label="In a Claude session" cmd="/huddle:setup" />
      <CmdField id="so-t" label="Or in a terminal" cmd="huddle open" />
      <CopyButton
        text="/huddle:setup"
        done="Copied: paste it into a Claude session"
        class="btn btn-primary h-10 w-full text-sm"
      >
        <Icon name="copy" />
        Copy /huddle:setup
      </CopyButton>
    </div>
  );
}

/** The sign-in card: the biometric button when this browser holds a passkey, the code field
 *  while an enrolment stands, the passkey button while a passkey exists, the reason line only
 *  when the server sent one. */
function SignInCard(): JSX.Element {
  const { api } = useHuddle();
  const [passkeys, setPasskeys] = useState(false);
  const [totp, setTotp] = useState(false);
  const [reason, setReason] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [recovery, setRecovery] = useState(false);
  const [recoveryCode, setRecoveryCode] = useState("");
  const [here, setHere] = useState(false);
  const [autofill, setAutofill] = useState(false);

  useEffect(() => {
    api
      .api("/api/login/available")
      .then((r) => {
        const a = r as { passkeys?: unknown; totp?: unknown; reason?: unknown };
        if (a.passkeys === true) setPasskeys(true);
        if (a.totp === true) setTotp(true);
        if (typeof a.reason === "string" && a.reason) setReason(a.reason);
      })
      .catch(() => {});
    setHere(passkeyHere());
  }, []);

  /** One code, six digits or one recovery word: the finish sets the cookie, the reload lands the
   *  shell, and the marker tells it the biometrics offer may run. */
  const signInWithCode = async (code: string): Promise<void> => {
    setBusy(true);
    setError("");
    try {
      await api.api("/api/login/totp", { body: { code } });
      try {
        localStorage.setItem("huddle:totp-signed-in", "1");
      } catch {
        // site data is blocked: the offer simply waits for its thirty-day turn
      }
      location.reload();
    } catch (e) {
      setError(errText(e));
    } finally {
      setBusy(false);
    }
  };

  /** One touch: begin, the browser's own credential, finish, and the reload that lands the shell. */
  const signInWithPasskey = async (): Promise<void> => {
    setBusy(true);
    setError("");
    try {
      const r = (await api.api("/api/login/begin", { body: {} })) as { options?: unknown };
      let cred: PublicKeyCredential;
      try {
        cred = (await navigator.credentials.get({
          publicKey: assertionFromJSON(r.options).publicKey,
        })) as PublicKeyCredential;
      } catch (e) {
        if ((e as { name?: string }).name === "NotAllowedError") {
          setError("Sign-in cancelled. Try again.");
        } else {
          setError("No passkey on this device. Save one below, or use a sign-in link.");
        }
        return;
      }
      try {
        localStorage.setItem("huddle:passkey-here", "1");
      } catch {
        // site data is blocked: the next visit asks the code again
      }
      await api.api("/api/login/finish", { body: { response: credentialToJSON(cred) } });
      location.reload();
    } catch (e) {
      setError(errText(e));
    } finally {
      setBusy(false);
    }
  };

  // the passkey offer the code field itself carries where the browser supports it: the autofill
  // attribute says so as soon as the field stands, the ceremony itself waits for its focus
  useEffect(() => {
    const pk = window.PublicKeyCredential;
    if (!passkeys || typeof pk?.isConditionalMediationAvailable !== "function") return;
    pk.isConditionalMediationAvailable()
      .then((ok) => setAutofill(ok))
      .catch(() => {});
  }, [passkeys]);

  const bio = here && passkeys;
  const firstTime = !totp && !passkeys;

  // one begin parked as a conditional get, armed the first time the code field takes focus and
  // only in a browser that already saved its passkey here: a first visit stays quiet until its
  // owner asks, and the field's own autofill is the offer
  const armed = useRef(false);
  const armConditional = (): void => {
    if (armed.current || !bio) return;
    armed.current = true;
    api
      .api("/api/login/begin", { body: {} })
      .then((r) =>
        navigator.credentials
          .get({
            mediation: "conditional",
            publicKey: assertionFromJSON((r as { options?: unknown }).options).publicKey,
          })
          .then((cred) => {
            markPasskeyHere();
            return api.api("/api/login/finish", {
              body: { response: credentialToJSON(cred as PublicKeyCredential) },
            });
          })
          .then(() => location.reload())
          .catch(() => {}),
      )
      .catch(() => {});
  };

  /** Enter outside any control runs the sign-in this screen leads with. */
  const onKey = (e: KeyboardEvent): void => {
    if (e.key !== "Enter" || e.metaKey || e.ctrlKey || e.altKey) return;
    const t = e.target instanceof HTMLElement ? e.target : null;
    if (t?.closest("button, input, textarea, select, a, [contenteditable]")) return;
    if (bio || (!totp && passkeys)) {
      e.preventDefault();
      void signInWithPasskey();
    }
  };

  return (
    <div data-landing-signin="" onKeyDown={onKey}>
      <Panel title="Sign in" label="Sign in">
        <div class="flex flex-col gap-5">
          {reason ? (
            <div class="alert alert-warning alert-soft text-sm" role="status" data-reason="">
              <span class="shrink-0">
                <Icon name="logout" />
              </span>
              <span>{reason}</span>
            </div>
          ) : null}
          {bio ? (
            <button
              type="button"
              class="btn btn-primary h-12 w-full text-sm"
              disabled={busy}
              data-bio-signin=""
              onClick={() => void signInWithPasskey()}
            >
              <Icon name="key" />
              Sign in with {bioName(navigator.userAgent)}
            </button>
          ) : null}
          {totp ? (
            <div class="flex flex-col gap-2.5" data-signin-code="" onFocusIn={armConditional}>
              {recovery ? (
                <>
                  <RecoveryField
                    code={recoveryCode}
                    busy={busy}
                    onCode={(c) => void signInWithCode(c)}
                    onInput={setRecoveryCode}
                  />
                  <button
                    type="button"
                    class="btn btn-ghost btn-sm self-start"
                    onClick={() => {
                      setRecovery(false);
                      setError("");
                    }}
                  >
                    Use the app's code instead
                  </button>
                </>
              ) : (
                <>
                  <span class="text-sm font-medium">
                    {bio ? "Or enter your authenticator code" : "Enter the code from your authenticator app"}
                  </span>
                  <CodeField
                    onCode={(code) => void signInWithCode(code)}
                    busy={busy}
                    error={error}
                    autoFocus={!bio}
                    autocomplete={autofill ? "one-time-code webauthn" : "one-time-code"}
                  />
                  <button
                    type="button"
                    class="btn btn-ghost btn-sm self-start"
                    onClick={() => {
                      setRecovery(true);
                      setError("");
                    }}
                  >
                    Use a recovery code instead
                  </button>
                </>
              )}
            </div>
          ) : null}
          {!bio && passkeys ? (
            <button
              type="button"
              class={`${totp ? "btn" : "btn btn-primary"} h-12 w-full text-sm`}
              disabled={busy}
              onClick={() => void signInWithPasskey()}
            >
              <Icon name="key" />
              Sign in with a passkey
            </button>
          ) : null}
          {passkeys && !totp && error ? (
            <p role="alert" class="m-0 text-sm text-error">
              {error}
            </p>
          ) : null}
          {firstTime ? (
            <FirstTimeCard />
          ) : (
            <details class="collapse collapse-arrow bg-base-200 text-sm" data-other-ways="">
              <summary class="collapse-title font-medium muted">Other ways to sign in</summary>
              <div class="collapse-content flex flex-col gap-3">
                <p class="m-0 muted">
                  Run <code>/huddle:setup</code> in any session of this huddle — or <code>huddle open</code>{" "}
                  in a terminal — for a new sign-in link, then open it here.
                </p>
                <CmdField id="so-c" label="In a Claude session" cmd="/huddle:setup" />
                <CmdField id="so-t" label="Or in a terminal" cmd="huddle open" />
                <CopyButton
                  text="/huddle:setup"
                  done="Copied: paste it into a Claude session"
                  class="btn h-10 w-full text-sm"
                >
                  <Icon name="copy" />
                  Copy /huddle:setup
                </CopyButton>
              </div>
            </details>
          )}
        </div>
      </Panel>
    </div>
  );
}

/** The landing page: sign-in (the default focus), start a session, and the tour — one screen,
 *  light and dark, stacking sign-in first on a phone. */
export function Landing(): JSX.Element {
  const [ver, setVer] = useState("");
  const [place, setPlace] = useState("");
  const [shot, setShot] = useState(-1);

  useEffect(() => {
    document.title = "Huddle";
    fetch("/health")
      .then((r) => r.json())
      .then((h: { version?: unknown; machine?: unknown; name?: unknown }) => {
        if (typeof h.version === "string" && h.version) setVer(`Huddle v${h.version}`);
        const where = [h.name, h.machine].filter((p): p is string => typeof p === "string" && p !== "");
        setPlace(where.join(" · "));
      })
      .catch(() => {});
  }, []);

  return (
    <main class="min-h-dvh bg-base-200 px-4 py-6 sm:px-6 sm:py-10" data-landing="">
      <div class="mx-auto flex w-full max-w-[960px] flex-col gap-8">
        <header class="flex items-start justify-between gap-4" data-landing-head="">
          <div class="flex min-w-0 items-center gap-4">
            <Logo class="size-12 shrink-0" />
            <div class="min-w-0">
              <h1 class="text-2xl font-bold tracking-tight">Huddle</h1>
              <p class="m-0 truncate text-sm muted">{place || "Sign in to see your channels and sessions"}</p>
            </div>
          </div>
          <ThemeToggle />
        </header>

        <div class="grid items-start gap-6 lg:grid-cols-2">
          <SignInCard />
          <ConnectCard ch="" invite={false} />
        </div>

        <TourStrip onOpen={setShot} />
        {shot >= 0 ? <TourLightbox at={shot} onClose={() => setShot(-1)} onStep={setShot} /> : null}
        <p class="muted text-center text-xs">{ver || "Huddle"}</p>
      </div>
    </main>
  );
}
