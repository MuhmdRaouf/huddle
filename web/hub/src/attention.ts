// attention.ts — the one rule that decides which group a session reads as, for the list's
// sections and the Sessions tab's badge alike. It reads the row and the needs-you items alone,
// never a time, so a session idle for days keeps its place.
import type { NeedItem, RegistrySession } from "./registry.ts";

/** The groups the Now list shows, in the order it shows them. */
export type Attention = "needs-you" | "working" | "idle" | "listening" | "stuck" | "ended";

/** The group a session reads as, decided in this order: an ended session stays ended and its
 *  open ask is history (Ended, never the badge); an open ask waits on the owner whatever the
 *  last report said (Needs you, so a listening session with an ask jumps there); a row whose
 *  idle-wake waiter the hub found armed reads Listening; and the rest read their server state,
 *  an unknown one as idle. */
export function attentionOf(
  row: Pick<RegistrySession, "session_id" | "state" | "listening">,
  needs: NeedItem[] | null,
): Attention {
  if (row.state === "ended") return "ended";
  if ((needs ?? []).some((n) => n.session === row.session_id && n.state === "open")) return "needs-you";
  if (row.listening) return "listening";
  if (row.state === "needs-you") return "needs-you";
  if (row.state === "working") return "working";
  if (row.state === "stuck") return "stuck";
  return "idle";
}
