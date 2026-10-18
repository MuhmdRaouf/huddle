// FirstRun.tsx — the checklist a freshly signed-in browser carries on Now: four items that tick
// themselves from server state, each with its button going straight to the place that does it,
// and the passkey row an owner's link session is offered (the banner the sign-in card used to
// carry, minted and dismissed the same way). Dismissal holds per browser; the panel hides once
// every item stands ticked.

import type { JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import { errText, useHuddle } from "./context.tsx";
import { Icon } from "../icons.tsx";
import { readPref, writePref } from "../storage.ts";

/** One checklist item: its label, whether it stands done, and where its button goes. */
type Item = { key: string; label: string; done: boolean; href: string };

/** The passkey row: the moved banner, drawn inside the checklist. */
function PasskeyRow({
  busy,
  error,
  onSave,
  onClose,
}: {
  busy: boolean;
  error: string;
  onSave: () => void;
  onClose: () => void;
}): JSX.Element {
  return (
    <div class="flex flex-col gap-1.5 border-b hairline pb-3" data-banner="passkey">
      <div class="flex items-center gap-3">
        <span class="shrink-0 text-primary">
          <Icon name="key" />
        </span>
        <p class="m-0 min-w-0 flex-1 text-sm muted">
          Save a passkey for this browser — next time Touch ID signs you in.
        </p>
        <button type="button" class="btn btn-primary btn-sm" disabled={busy} onClick={onSave}>
          Save passkey
        </button>
        <button
          type="button"
          class="btn btn-ghost btn-square btn-sm muted"
          aria-label="Dismiss"
          onClick={onClose}
        >
          <Icon name="x" />
        </button>
      </div>
      {error ? (
        <p role="alert" class="m-0 pl-9 text-sm text-error">
          {error}
        </p>
      ) : null}
    </div>
  );
}

/** The first-run checklist on Now: the four items, the passkey row above them. `onPairToken`
 *  opens the pairing screen a minted token pays for (the shell owns that screen). */
export function FirstRunChecklist({
  onPairToken,
}: {
  onPairToken: (token: string) => void;
}): JSX.Element | null {
  const { api, who, state, registry } = useHuddle();
  const [budgets, setBudgets] = useState<boolean | null>(null);
  const [dismissed, setDismissed] = useState(() => readPref(localStorage, "first-run", false));
  const [bannerOff, setBannerOff] = useState(() => readPref(localStorage, "passkey-banner", false));
  const [minting, setMinting] = useState(false);
  const [mintError, setMintError] = useState("");

  useEffect(() => {
    api
      .api("/api/radar/api/budgets")
      .then((r) => {
        const list = (r as { budgets?: unknown }).budgets;
        setBudgets(Array.isArray(list) && list.length > 0);
      })
      .catch(() => setBudgets(false));
  }, [api]);

  const items: Item[] = [
    {
      key: "totp",
      label: "Set up your authenticator app",
      done: who.totp === true,
      href: "#/settings/signin",
    },
    {
      key: "session",
      label: "Connect a session",
      done: (registry.getState().sessions ?? []).length > 0,
      href: "#/channels",
    },
    {
      key: "channel",
      label: "Create a channel",
      done: (state.channels ?? []).length > 0,
      href: "#/channels",
    },
    { key: "budget", label: "Set a budget", done: budgets === true, href: "#/settings/radar" },
  ];
  if (dismissed || items.every((i) => i.done)) return null;

  /** Mints one pairing link without a terminal and opens the ceremony it pays for. */
  const savePasskey = async (): Promise<void> => {
    setMinting(true);
    setMintError("");
    try {
      const r = (await api.api("/api/pair/mint", { body: { action: "mint" } })) as { token?: string };
      if (!r.token) throw new Error("the server sent no pairing link");
      onPairToken(r.token);
    } catch (e) {
      setMintError(errText(e));
    } finally {
      setMinting(false);
    }
  };

  // the passkey row asks only a browser whose credential is the owner's link session: a paired
  // device already has its passkey, and a member may not mint one
  const passkeyRow =
    who.kind === "owner" && !who.devicePasskey && !bannerOff ? (
      <PasskeyRow
        busy={minting}
        error={mintError}
        onSave={() => void savePasskey()}
        onClose={() => {
          writePref(localStorage, "passkey-banner", true);
          setBannerOff(true);
        }}
      />
    ) : null;

  return (
    <section
      aria-labelledby="firstrun-h"
      data-first-run=""
      class="mx-3 mt-3 flex flex-col gap-3 rounded-[14px] border hairline bg-base-100 p-3.5"
    >
      <div class="flex items-center justify-between gap-3">
        <h2 id="firstrun-h" class="m-0 text-[13px] font-semibold tracking-[0.06em] uppercase muted">
          Get set up
        </h2>
        <button
          type="button"
          class="btn btn-ghost btn-sm muted"
          onClick={() => {
            writePref(localStorage, "first-run", true);
            setDismissed(true);
          }}
        >
          Hide checklist
        </button>
      </div>
      {passkeyRow}
      <ul class="m-0 flex list-none flex-col gap-1 p-0">
        {items.map((item) => (
          <li
            key={item.key}
            class="flex min-h-10 items-center gap-2.5"
            data-item={item.key}
            data-done={item.done ? "true" : "false"}
          >
            <span
              class={`inline-flex size-5 shrink-0 items-center justify-center rounded-full border ${
                item.done ? "border-success bg-success/15 text-success" : "hairline text-base-content/40"
              }`}
              aria-hidden="true"
            >
              {item.done ? <Icon name="check" class="size-3.5" /> : null}
            </span>
            <span class={`min-w-0 flex-1 truncate text-sm ${item.done ? "muted line-through" : ""}`}>
              {item.label}
            </span>
            <span class="sr-only">{item.done ? "done" : "to do"}</span>
            {item.done ? null : (
              <a class="btn btn-ghost btn-sm shrink-0" href={item.href}>
                Go
                <span class="sr-only"> to {item.label}</span>
              </a>
            )}
          </li>
        ))}
      </ul>
    </section>
  );
}
