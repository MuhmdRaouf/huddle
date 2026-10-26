// scheme.ts — the hub's scheme choice: System, Dark or Light, the way every switch offers it
// (Settings › Appearance, the desktop's menu, the palette, the landing's toggle). A pick is kept
// under "huddle:scheme" and painted at once; main.tsx follows the OS while the choice is System.

import { applyScheme, type SchemePref, type SchemeStore, writeScheme } from "@huddle/ui/theme.ts";

/** The three choices, in the order every switch shows them: the key, its label and its icon
 *  (a name both the hub's and the design system's icon sets carry). */
export const SCHEMES: readonly (readonly [SchemePref, string, "monitor" | "moon" | "sun"])[] = [
  ["system", "System", "monitor"],
  ["dark", "Dark", "moon"],
  ["light", "Light", "sun"],
];

/** Does the OS prefer dark right now? */
export const systemDark = (): boolean => window.matchMedia("(prefers-color-scheme: dark)").matches;

/** Keeps the choice and paints it on <html>. */
export function pickScheme(store: SchemeStore, pref: SchemePref): void {
  writeScheme(store, pref);
  applyScheme(document.documentElement, pref, systemDark(), store);
}
