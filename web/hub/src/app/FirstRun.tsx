// FirstRun.tsx — the checklist a freshly signed-in browser carries on Now, as one compact list:
// four rows that tick themselves from server state, each open row one 44 px link straight to the
// place that does it, and the passkey row an owner's link session is offered (minted and
// dismissed the same way). Dismissal holds per browser; the panel hides once every item stands
// ticked.

import type { JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import { errText, useHuddle } from "./context.tsx";
import { Icon } from "../icons.tsx";
import { readPref, writePref } from "../storage.ts";

/** One checklist item: its label, whether it stands done, and where its button goes. */
type Item = { key: string; label: string; done: boolean; href: string };

/** The passkey row: the moved banner, drawn inside the checklist like the items under it —
 *  one-line label, the why as the muted second line, the actions right-aligned. */
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
    <div class="flex flex-col gap-1 border-b border-[var(--ui-line)] pb-2" data-banner="passkey">
      <div class="flex min-h-[var(--ui-tap)] items-center gap-2.5">
        <span class="shrink-0 text-[var(--ui-accent)]">
          <Icon name="key" />
        </span>
        <div class="min-w-0 flex-1">
          <p class="m-0 text-sm">Save a passkey</p>
          <p class="m-0 text-xs text-[var(--ui-muted)]">Touch ID signs you in next time.</p>
        </div>
        <button
          type="button"
          class="ui-btn ui-btn-primary shrink-0"
          aria-label="Save passkey"
          disabled={busy}
          onClick={onSave}
        >
          Save
        </button>
        <button
          type="button"
          class="ui-btn ui-btn-ghost ui-btn-icon shrink-0"
          aria-label="Dismiss"
          onClick={onClose}
        >
          <Icon name="x" />
        </button>
      </div>
      {error ? (
        <p role="alert" class="m-0 pl-7 text-sm text-[var(--ui-fail-ink)]">
          {error}
        </p>
      ) : null}
    </div>
  );
}

/** An item's mark: a green tick when it stands done, an empty ring while it waits. */
function ItemMark({ done }: { done: boolean }): JSX.Element {
  return (
    <span
      class={`inline-flex size-5 shrink-0 items-center justify-center rounded-full border ${
        done
          ? "border-[var(--ui-ok)] bg-[var(--ui-ok-soft)] text-[var(--ui-ok-ink)]"
          : "border-[var(--ui-line-strong)] text-[var(--ui-muted)]"
      }`}
      aria-hidden="true"
    >
      {done ? <Icon name="check" class="size-3.5" /> : null}
    </span>
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
      class="mx-3 mt-3 flex flex-col gap-1 rounded-[var(--ui-radius-lg)] border border-[var(--ui-line)] bg-[var(--ui-surface)] px-3.5 py-2"
    >
      <div class="flex items-center justify-between gap-3">
        <h2
          id="firstrun-h"
          class="m-0 text-[13px] font-semibold tracking-[0.06em] uppercase text-[var(--ui-muted)]"
        >
          Get set up
        </h2>
        <button
          type="button"
          class="ui-btn ui-btn-ghost -mr-2"
          onClick={() => {
            writePref(localStorage, "first-run", true);
            setDismissed(true);
          }}
        >
          Hide checklist
        </button>
      </div>
      {passkeyRow}
      <ul class="m-0 flex list-none flex-col p-0">
        {items.map((item) => (
          <li key={item.key} data-item={item.key} data-done={item.done ? "true" : "false"}>
            {item.done ? (
              <div class="flex min-h-[var(--ui-tap)] items-center gap-2.5">
                <ItemMark done />
                <span class="min-w-0 flex-1 truncate text-sm text-[var(--ui-muted)] line-through">
                  {item.label}
                </span>
                <span class="sr-only">done</span>
              </div>
            ) : (
              <a
                class="-mx-2 flex min-h-[var(--ui-tap)] items-center gap-2.5 rounded-[var(--ui-radius)] px-2 text-sm hover:bg-[var(--ui-surface-2)]"
                href={item.href}
              >
                <ItemMark done={false} />
                <span class="min-w-0 flex-1 truncate">{item.label}</span>
                <span class="sr-only">to do</span>
                <Icon name="right" class="size-4 shrink-0 text-[var(--ui-muted)]" />
              </a>
            )}
          </li>
        ))}
      </ul>
    </section>
  );
}
