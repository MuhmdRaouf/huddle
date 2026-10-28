// Pair.tsx — the screen a `huddle pair` link opens at /pair: the token rides the fragment the
// browser never sends to the server, so the whole ceremony runs before any credential exists.
// The three stages are the approved design's: the name the device offers, the code both sides
// compare, and the passkey the browser creates once the owner says the codes match.

import { Button } from "@huddle/ui/button.tsx";
import { Field } from "@huddle/ui/field.tsx";
import type { JSX } from "preact";
import { useState } from "preact/hooks";
import { Icon, Logo } from "../icons.tsx";
import { creationFromJSON, credentialToJSON, deviceNameFromUA } from "../passkey.ts";
import { errText, useHuddle } from "./context.tsx";

/** The screen's stages: the name, then the code and its passkey, then the mismatch's dead end. */
type Stage = "name" | "code" | "cancelled";

/** What a begin answers: the creation options, the code both sides compare and the link's life. */
type BeginAnswer = { options?: unknown; code?: string; expires_in?: number };

/** The one column every stage stands in: a phone's width, its actions pinned to its foot. */
const COLUMN = "mx-auto flex w-full max-w-md flex-1 flex-col gap-6 pt-10";

/** A link's remaining life as the design shows it: minutes and seconds. */
function expiresLabel(expiresIn: number): string {
  const m = Math.max(0, Math.floor(expiresIn / 60));
  const s = Math.max(0, expiresIn % 60);
  return `Expires in ${m}:${String(s).padStart(2, "0")}`;
}

/** The one check icon the design puts before every promise the pairing makes. */
function Tick(): JSX.Element {
  return (
    <span class="mt-0.5 shrink-0 text-[var(--ui-ok-ink)]" aria-hidden="true">
      <Icon name="check" />
    </span>
  );
}

/** A stage the ceremony cannot go on from: the logo and the one line that says what to run. */
function DeadEnd({ text }: { text: string }): JSX.Element {
  return (
    <main data-pair="" class="ui-page flex flex-col">
      <div class="mx-auto flex w-full max-w-md flex-1 flex-col items-center justify-center gap-4 text-center">
        <Logo />
        <h1 class="m-0 text-[length:var(--ui-text-xl)] leading-tight font-semibold">{text}</h1>
      </div>
    </main>
  );
}

export function Pair({
  token: given,
  onPaired,
}: { token?: string | undefined; onPaired?: () => void } = {}): JSX.Element {
  const { api } = useHuddle();
  // the link's token rides the fragment at /pair; the banner's minted token arrives as a prop
  const [hashToken] = useState(() => location.hash.replace(/^#/, ""));
  const token = given ?? hashToken;
  const [name, setName] = useState(() => deviceNameFromUA(navigator.userAgent));
  const [stage, setStage] = useState<Stage>("name");
  const [code, setCode] = useState("");
  const [expiresIn, setExpiresIn] = useState(0);
  const [options, setOptions] = useState<unknown>(null);
  const [error, setError] = useState("");
  const [retryable, setRetryable] = useState(false);
  const [busy, setBusy] = useState(false);

  const begin = async (): Promise<void> => {
    setBusy(true);
    setError("");
    try {
      const r = (await api.api("/api/pair/begin", { body: { token, name } })) as BeginAnswer;
      setCode(r.code ?? "");
      setExpiresIn(r.expires_in ?? 0);
      setOptions(r.options ?? null);
      setStage("code");
    } catch (e) {
      setError(errText(e));
    } finally {
      setBusy(false);
    }
  };

  const createAndFinish = async (): Promise<void> => {
    setBusy(true);
    setError("");
    setRetryable(false);
    try {
      let cred: PublicKeyCredential;
      try {
        cred = (await navigator.credentials.create({
          publicKey: creationFromJSON(options).publicKey,
        })) as PublicKeyCredential;
      } catch (e) {
        // the owner backed out of the browser's own prompt: nothing reached the server, so the
        // ceremony can run again as it stands
        if ((e as { name?: string }).name === "NotAllowedError") {
          setError("The passkey was not created. Try again.");
          setRetryable(true);
          return;
        }
        throw e;
      }
      await api.api("/api/pair/finish", { body: { token, response: credentialToJSON(cred) } });
      // the cookie is in: a full reload onto the Now tab, the way the signed-out passkey lands
      // the shell — the app this page runs in opened its streams before the cookie existed (the
      // TLS hub's only sign-in is this ceremony), and no stream reconnects on a hash change
      history.replaceState(null, "", "/");
      location.hash = "#/sessions";
      location.reload();
      onPaired?.();
    } catch (e) {
      setError(errText(e));
    } finally {
      setBusy(false);
    }
  };

  if (!token) {
    return <DeadEnd text="This link is incomplete. Run `huddle pair` again." />;
  }

  if (stage === "cancelled") {
    return <DeadEnd text="Pairing cancelled. Run `huddle pair` again on your computer." />;
  }

  const head = (
    <>
      <div class="flex items-center gap-2.5">
        <span class="flex size-10 items-center justify-center rounded-[var(--ui-radius)] bg-[var(--ui-surface-2)] text-[var(--ui-accent)]">
          <Logo class="size-6" />
        </span>
        <span class="text-[length:var(--ui-text-lg)] font-semibold">Huddle</span>
      </div>
      <h1 class="m-0 text-[length:var(--ui-text-xl)] leading-tight font-semibold">
        Pair this device as “{name}”?
      </h1>
    </>
  );

  if (stage === "code") {
    return (
      <main data-pair="" data-pair-stage="code" class="ui-page flex flex-col">
        <div class={COLUMN}>
          {head}
          <div class="ui-card flex flex-col items-center gap-2 p-5">
            <span class="text-xs tracking-widest uppercase text-[var(--ui-muted)]">Pairing code</span>
            {/* the pairing code keeps its 34 px: 2.2667 rem at the 15 px root, the size the
             *  terminal's own code prints at, so the two read as one */}
            <span class="ui-mono text-[length:2.2667rem] font-medium tracking-[0.12em]">{code}</span>
            <span class="ui-mono text-xs text-[var(--ui-muted)]">{expiresLabel(expiresIn)}</span>
          </div>
          <p class="m-0 text-sm text-[var(--ui-muted)]">Check this matches the code in your terminal</p>
          <div class="mt-auto flex flex-col gap-2">
            {error ? (
              <p role="alert" class="m-0 text-sm text-[var(--ui-fail-ink)]">
                {error}
              </p>
            ) : null}
            {retryable ? (
              <Button
                variant="secondary"
                class="w-full"
                disabled={busy}
                onClick={() => void createAndFinish()}
              >
                Retry
              </Button>
            ) : null}
            <Button size="lg" class="w-full" disabled={busy} onClick={() => void createAndFinish()}>
              It matches
            </Button>
            <Button variant="ghost" class="w-full" onClick={() => setStage("cancelled")}>
              The code does not match
            </Button>
          </div>
        </div>
      </main>
    );
  }

  return (
    <main data-pair="" data-pair-stage="name" class="ui-page flex flex-col">
      <div class={COLUMN}>
        {head}
        <p class="m-0 text-[length:var(--ui-text-md)] text-[var(--ui-muted)]">
          You ran <code class="ui-mono text-[var(--ui-text)]">huddle pair</code> on your computer. Check the
          code matches what your terminal shows.
        </p>
        <ul class="m-0 flex list-none flex-col gap-3 p-0 text-sm">
          <li class="flex gap-2.5">
            <Tick />
            This device gets a passkey. No password, nothing to type.
          </li>
          <li class="flex gap-2.5">
            <Tick />
            It can watch, reply and allow commands with a passkey check.
          </li>
          <li class="flex gap-2.5">
            <Tick />
            Remove it any time from Settings on any paired device.
          </li>
        </ul>
        <div class="mt-auto flex flex-col gap-2">
          <Field label="Device name" value={name} onInput={setName} />
          {error ? (
            <p role="alert" class="m-0 text-sm text-[var(--ui-fail-ink)]">
              {error}
            </p>
          ) : null}
          <Button
            size="lg"
            class="w-full"
            disabled={busy || name.trim().length === 0}
            onClick={() => void begin()}
          >
            Continue
          </Button>
        </div>
      </div>
    </main>
  );
}
