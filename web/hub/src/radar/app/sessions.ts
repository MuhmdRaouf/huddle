/**
 * The session-list helper the views share: what counts as a job. Pure; no rail, no picking — the
 * huddle shell owns the list.
 */

import type { SessionListItem } from "../shared/api.ts";

/** A session radar only saw through a provider plugin: jobs carry a plugin id and no transcript of their own. */
export function isJob(item: SessionListItem): boolean {
  return item.external || item.id.includes(":");
}
