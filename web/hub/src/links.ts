// links.ts — the linking facts every screen shares (SPEC 8): the shape of a link as the API
// answers it, the rule a channel's name must satisfy, and the one line each level says about
// itself — what the picker and the Settings section both spell out.

/** The three levels a link hears its channel at. */
export type LinkLevel = "overhear" | "digest" | "on-request";

/** One link as GET /api/answers answers it: the tie, plus the time of the session's newest
 *  digest when it has digested anything yet. */
export type LinkRow = {
  channel: string;
  session_id: string;
  level: LinkLevel;
  linked_at: string;
  digest_at?: string;
};

/** One channel's links, as GET /api/links groups them. */
export type ChannelLinks = { channel: string; links: LinkRow[] };

/** A channel's name: a lowercase letter or digit to start, then lowercase letters, digits and
 *  dashes, 40 characters at most — the same rule the server enforces. */
export const CHANNEL_RE = /^[a-z0-9][a-z0-9-]{0,39}$/;

/** Why the name is refused, or null when it passes the rule. */
export function channelNameError(name: string): string | null {
  return CHANNEL_RE.test(name)
    ? null
    : "Lowercase letters, digits and dashes, 40 at most, starting with a letter or digit.";
}

/** What each level says under the picker: what the session would hear, in plain words. */
export const LEVEL_TEXT: Record<LinkLevel, string> = {
  overhear: "Hears every message posted in the channel.",
  digest: "Gets each peer's newest digest once, on its next prompt.",
  "on-request": "Hears nothing until you brief it.",
};

/** The one word each level is named by, in picker and select alike. */
export const LEVEL_LABEL: Record<LinkLevel, string> = {
  overhear: "Overhear",
  digest: "Digest",
  "on-request": "On request",
};
