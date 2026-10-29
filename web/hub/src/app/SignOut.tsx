// SignOut.tsx — the Settings › Security › Sign out section: "Sign out of this browser" ends this
// browser's session after one confirm; "Sign out everywhere" ends every device's session and asks
// for the passkey before it posts. A refusal toasts and the page stays.

import { Button } from "@huddle/ui/button.tsx";
import { Dialog } from "@huddle/ui/dialog.tsx";
import type { JSX } from "preact";
import { useState } from "preact/hooks";
import { errText, useHuddle } from "./context.tsx";
import { useStepUp } from "./Needs.tsx";

/** The card the two sign-out buttons sit on. */
const CARD =
  "flex flex-col overflow-hidden rounded-[var(--ui-radius)] bg-[var(--ui-surface)] shadow-[var(--ui-shadow-1)]";

/** Muted body copy under a heading. */
const NOTE = "m-0 text-sm text-[var(--ui-muted)]";

/** Which sign-out a confirm is standing for. */
type Confirm = "here" | "everywhere" | null;

/** The Sign out section: two acts, each behind its own confirm, the everywhere one through the
 *  passkey step-up before its call goes out. */
export function SignOut(): JSX.Element {
  const { api, toast } = useHuddle();
  const { ask, sheet } = useStepUp();
  const [confirm, setConfirm] = useState<Confirm>(null);
  const [busy, setBusy] = useState(false);

  /** Signs this browser out; the server clears the session cookie. */
  const signHere = async (): Promise<void> => {
    setBusy(true);
    try {
      await api.api("/api/signout", { body: {} });
      location.assign("/");
    } catch (e) {
      toast(errText(e), { bad: true });
      setBusy(false);
    }
  };

  /** Ends every device's session. The passkey check comes first, before anything posts. */
  const signEverywhere = async (): Promise<void> => {
    setBusy(true);
    // the confirm closes before the passkey sheet opens: one dialog standing at a time
    if (!(await ask({ action: "Sign out everywhere" }))) {
      setBusy(false);
      return;
    }
    try {
      await api.api("/api/signout/all", { body: {} });
      location.assign("/");
    } catch (e) {
      toast(errText(e), { bad: true });
      setBusy(false);
    }
  };

  /** The confirm's act: close it, then run the sign-out it stood for. */
  const confirmNow = (): void => {
    const which = confirm;
    setConfirm(null);
    void (which === "everywhere" ? signEverywhere() : signHere());
  };

  return (
    <section aria-labelledby="signout-h" data-signout="" class="flex flex-col gap-1">
      {sheet}
      <h2
        id="signout-h"
        class="mb-1.5 text-xs font-semibold tracking-[0.06em] uppercase text-[var(--ui-muted)]"
      >
        Sign out
      </h2>
      <div class={CARD}>
        <Button variant="secondary" class="w-full" disabled={busy} onClick={() => setConfirm("here")}>
          Sign out of this browser
        </Button>
        <Button variant="danger" class="w-full" disabled={busy} onClick={() => setConfirm("everywhere")}>
          Sign out everywhere
        </Button>
      </div>
      <p class={NOTE}>
        Sign out everywhere ends every device's session, this browser too. It asks for your passkey first.
      </p>
      <Dialog
        open={confirm !== null}
        onClose={() => setConfirm(null)}
        title={confirm === "everywhere" ? "Sign out everywhere" : "Sign out of this browser"}
      >
        <div class="flex flex-col gap-4 px-4" data-signout-confirm="">
          <p class={NOTE}>
            {confirm === "everywhere"
              ? "Sign out everywhere ends every device's session, this one too."
              : "This browser signs out of the hub. You sign in again when you come back."}
          </p>
          <div class="flex justify-end gap-2">
            <Button variant="ghost" onClick={() => setConfirm(null)}>
              Cancel
            </Button>
            <Button variant={confirm === "everywhere" ? "danger" : "primary"} onClick={confirmNow}>
              {confirm === "everywhere" ? "Sign out everywhere" : "Sign out"}
            </Button>
          </div>
        </div>
      </Dialog>
    </section>
  );
}
