// Pair.tsx — the screen a `huddle pair` link opens at /pair: the token rides the fragment the
// browser never sends to the server, so the whole ceremony runs before any credential exists.
// The three stages are the approved design's: the name the device offers, the code both sides
// compare, and the passkey the browser creates once the owner says the codes match.

import type { JSX } from "preact";
import { useState } from "preact/hooks";
import { Icon, Logo } from "../icons.tsx";
import { creationFromJSON, credentialToJSON, deviceNameFromUA } from "../passkey.ts";
import { errText, useHuddle } from "./context.tsx";

/** The screen's stages: the name, then the code and its passkey, then the mismatch's dead end. */
type Stage = "name" | "code" | "cancelled";

/** What a begin answers: the creation options, the code both sides compare and the link's life. */
type BeginAnswer = { options?: unknown; code?: string; expires_in?: number };

/** A link's remaining life as the design shows it: minutes and seconds. */
function expiresLabel(expiresIn: number): string {
  const m = Math.max(0, Math.floor(expiresIn / 60));
  const s = Math.max(0, expiresIn % 60);
  return `Expires in ${m}:${String(s).padStart(2, "0")}`;
}

/** The one check icon the design puts before every promise the pairing makes. */
function Tick(): JSX.Element {
  return (
    <span class="mt-0.5 shrink-0 text-success" aria-hidden="true">
      <Icon name="check" />
    </span>
  );
}

export function Pair({ onPaired }: { onPaired?: () => void } = {}): JSX.Element {
  const { api } = useHuddle();
  const [token] = useState(() => location.hash.replace(/^#/, ""));
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
      // the cookie is in: the clean address, the shell over the Now tab, and the gate dropped
      history.replaceState(null, "", "/");
      location.hash = "#/now";
      onPaired?.();
    } catch (e) {
      setError(errText(e));
    } finally {
      setBusy(false);
    }
  };

  if (!token) {
    return (
      <main
        data-pair=""
        class="mx-auto flex min-h-dvh w-full max-w-md flex-col items-center justify-center gap-4 px-6 text-center"
      >
        <Logo />
        <h1 class="text-2xl font-semibold">This link is incomplete. Run `huddle pair` again.</h1>
      </main>
    );
  }

  if (stage === "cancelled") {
    return (
      <main
        data-pair=""
        class="mx-auto flex min-h-dvh w-full max-w-md flex-col items-center justify-center gap-4 px-6 text-center"
      >
        <Logo />
        <h1 class="text-2xl font-semibold">Pairing cancelled. Run `huddle pair` again on your computer.</h1>
      </main>
    );
  }

  const head = (
    <>
      <div class="flex items-center gap-2.5">
        <span class="flex size-10 items-center justify-center rounded-xl bg-base-200 text-primary">
          <Logo class="size-6" />
        </span>
        <span class="text-lg font-semibold">Huddle</span>
      </div>
      <h1 class="text-[28px] leading-tight font-semibold">Pair this device as “{name}”?</h1>
    </>
  );

  if (stage === "code") {
    return (
      <main
        data-pair=""
        data-pair-stage="code"
        class="mx-auto flex min-h-dvh w-full max-w-md flex-col gap-6 px-6 pt-14 pb-10"
      >
        {head}
        <div class="flex flex-col items-center gap-2 rounded-2xl border hairline bg-base-100 px-5 py-5">
          <span class="text-xs tracking-widest uppercase muted">Pairing code</span>
          <span class="font-mono text-[34px] font-medium tracking-[0.12em]">{code}</span>
          <span class="text-xs muted tnum">{expiresLabel(expiresIn)}</span>
        </div>
        <p class="m-0 text-sm muted">Check this matches the code in your terminal</p>
        <div class="mt-auto flex flex-col gap-2">
          {error ? (
            <p role="alert" class="m-0 text-sm text-error">
              {error}
            </p>
          ) : null}
          {retryable ? (
            <button
              type="button"
              class="btn btn-ghost"
              disabled={busy}
              onClick={() => void createAndFinish()}
            >
              Retry
            </button>
          ) : null}
          <button
            type="button"
            class="btn btn-primary min-h-13"
            disabled={busy}
            onClick={() => void createAndFinish()}
          >
            It matches
          </button>
          <button
            type="button"
            class="min-h-11 bg-transparent text-[15px] font-medium muted outline-none focus-visible:ring-2 focus-visible:ring-primary"
            onClick={() => setStage("cancelled")}
          >
            The code does not match
          </button>
        </div>
      </main>
    );
  }

  return (
    <main
      data-pair=""
      data-pair-stage="name"
      class="mx-auto flex min-h-dvh w-full max-w-md flex-col gap-6 px-6 pt-14 pb-10"
    >
      {head}
      <p class="m-0 text-[15px] muted">
        You ran <code class="font-mono text-base-content">huddle pair</code> on your computer. Check the code
        matches what your terminal shows.
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
        <label class="flex flex-col gap-1 text-xs muted">
          Device name
          <input class="input min-h-11 w-full" value={name} onInput={(e) => setName(e.currentTarget.value)} />
        </label>
        {error ? (
          <p role="alert" class="m-0 text-sm text-error">
            {error}
          </p>
        ) : null}
        <button
          type="button"
          class="btn btn-primary min-h-13"
          disabled={busy || name.trim().length === 0}
          onClick={() => void begin()}
        >
          Continue
        </button>
      </div>
    </main>
  );
}
