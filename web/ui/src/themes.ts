// themes.ts — the registry of the ui's themes: what <html data-theme> may name, and the sides each
// paints. theme-boot.js mirrors the ids before first paint; theme.css paints each side. New themes
// join the list here.

/** The two sides a theme can paint. */
export type ThemeSide = "light" | "dark";

/** One registered theme: its id on <html data-theme>, its label, and the sides it has. */
export type Theme = { id: string; label: string; sides: readonly ThemeSide[] };

/** Every theme, the default first; the list is never empty. */
export const THEMES: readonly [Theme, ...Theme[]] = [
  { id: "huddle", label: "Huddle", sides: ["light", "dark"] },
];

/** Where this browser keeps its theme. */
export const THEME_KEY = "huddle:theme";

/** The kept theme's id; a missing, unknown or unreadable value answers the default. */
export function readTheme(store: Pick<Storage, "getItem">): string {
  try {
    const raw = store.getItem(THEME_KEY);
    if (raw && THEMES.some((t) => t.id === raw)) return raw;
  } catch {
    // site data is blocked: the default stands
  }
  return THEMES[0].id;
}

/** Keeps a registered id; an unknown one writes nothing. Blocked site data keeps the choice for
 *  this page only. */
export function writeTheme(store: Pick<Storage, "setItem">, id: string): void {
  if (!THEMES.some((t) => t.id === id)) return;
  try {
    store.setItem(THEME_KEY, id);
  } catch {
    // site data is blocked: the choice lasts until the page reloads
  }
}

/** The side a theme paints for Appearance's resolved scheme: its only side when it has one, the
 *  scheme itself when the theme paints both (or the id is not registered). */
export function sideOf(id: string, scheme: ThemeSide): ThemeSide {
  const sides = THEMES.find((t) => t.id === id)?.sides ?? [];
  return sides.length === 1 ? (sides[0] ?? scheme) : scheme;
}
