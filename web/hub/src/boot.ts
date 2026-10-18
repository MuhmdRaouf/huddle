// boot.ts — the shell's start: one question before any real API call. /api/login/available
// answers 200 for a signed-in browser and a signed-out one alike, so the shell asks it first
// and only a signed-in answer starts the shell's own reads (the registry stream, the address's
// channel, whoami): a signed-out visit paints the landing without one 401 in the console, and
// the real API calls keep their 401. No DOM here: the client and both continuations are
// injected, so tests drive every branch.

import type { Api } from "./api.ts";

/** What boot needs: the client it asks through, and the two ways the answer sends it. */
export type BootDeps = {
  api: Pick<Api, "api">;
  /** Starts the shell's own reads; the answer said this browser holds a session. */
  start: () => void;
  /** Paints the signed-out landing; the answer said no session stands. */
  signedOut: () => void;
};

/** Asks the one question that answers 200 in both states, then starts the shell or paints the
 *  landing. A question that cannot be asked (the hub unreachable) starts the shell: unknown is
 *  not signed out, and the shell's own reads say what they say. */
export function boot(d: BootDeps): void {
  void d.api
    .api("/api/login/available")
    .then(
      (a) => (a as { signedIn?: unknown }).signedIn === true,
      () => true,
    )
    .then((signedIn) => (signedIn ? d.start() : d.signedOut()));
}
