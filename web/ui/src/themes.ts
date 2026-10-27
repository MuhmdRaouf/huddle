// themes.ts — the registry of the ui's themes: what <html data-theme> may name, and the sides each
// paints. theme-boot.js mirrors the ids before first paint; theme.css paints each side. New themes
// join the list here.

/** The two sides a theme can paint. */
export type ThemeSide = "light" | "dark";

/** One registered theme: its id on <html data-theme>, its label, the sides it has, and the status
 *  bar colour of each side it paints — the theme's own background. */
export type Theme = {
  id: string;
  label: string;
  sides: readonly ThemeSide[];
  bar: { light?: string; dark?: string };
};

/** Every theme, the default first; the list is never empty. */
export const THEMES: readonly [Theme, ...Theme[]] = [
  {
    id: "huddle",
    label: "Huddle",
    sides: ["light", "dark"],
    bar: { light: "#f4f5f7", dark: "#0b0c0f" },
  },
  {
    id: "tokyo-night",
    label: "Tokyo Night",
    sides: ["light", "dark"],
    bar: { light: "#d0d5e3", dark: "#16161e" },
  },
  { id: "monokai", label: "Monokai", sides: ["dark"], bar: { dark: "#1e1f1c" } },
  { id: "dracula", label: "Dracula", sides: ["dark"], bar: { dark: "#21222c" } },
  {
    id: "nord",
    label: "Nord",
    sides: ["light", "dark"],
    bar: { light: "#e5e9f0", dark: "#2e3440" },
  },
  {
    id: "gruvbox",
    label: "Gruvbox",
    sides: ["light", "dark"],
    bar: { light: "#f2e5bc", dark: "#1d2021" },
  },
  {
    id: "solarized",
    label: "Solarized",
    sides: ["light", "dark"],
    bar: { light: "#eee8d5", dark: "#00212b" },
  },
  {
    id: "rose-pine",
    label: "Rosé Pine",
    sides: ["light", "dark"],
    bar: { light: "#f2e9e1", dark: "#191724" },
  },
  { id: "one-dark", label: "One Dark", sides: ["dark"], bar: { dark: "#21252b" } },
  {
    id: "github",
    label: "GitHub",
    sides: ["light", "dark"],
    bar: { light: "#f6f8fa", dark: "#010409" },
  },
  {
    id: "everforest",
    label: "Everforest",
    sides: ["light", "dark"],
    bar: { light: "#efebd4", dark: "#232a2e" },
  },
  {
    id: "kanagawa",
    label: "Kanagawa",
    sides: ["light", "dark"],
    bar: { light: "#e5ddb0", dark: "#16161d" },
  },
];

/** Where this browser keeps its theme. */
export const THEME_KEY = "huddle:theme";

/** The theme an id names, or the default when none does. */
export function themeOf(id: string): Theme {
  return THEMES.find((t) => t.id === id) ?? THEMES[0];
}

/** The kept theme's id; a missing, unknown or unreadable value answers the default. */
export function readTheme(store: Pick<Storage, "getItem">): string {
  try {
    return themeOf(store.getItem(THEME_KEY) ?? "").id;
  } catch {
    // site data is blocked: the default stands
    return THEMES[0].id;
  }
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
  const sides = themeOf(id).sides;
  return sides.length === 1 ? (sides[0] ?? scheme) : scheme;
}
