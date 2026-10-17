// theme.ts — the theme choice: a family (six, each a dark and a light variant) plus a mode (System,
// Dark, Light), kept per browser under "huddle:theme" as JSON and applied to <html data-theme>
// before anything paints (public/theme-boot.js does it at boot; this is the running half).
// Catppuccin dark (mocha) is the default. A choice kept under the old single-palette names
// ("mocha", "latte", "system", "dark", "light") reads as its new one and is written back migrated.

import type { IconName } from "../icons.tsx";
import { readPref, type Storage, writePref } from "../storage.ts";

/** The six families the shared theme.css ships variants for. */
export type ThemeFamily = "catppuccin" | "gruvbox" | "tokyo" | "rose-pine" | "nord" | "solarized";

/** Whether the family's dark or its light variant is on, or the system picks. */
export type ThemeMode = "system" | "dark" | "light";

/** The choice: which family, and how its two variants are picked between. */
export type ThemePref = { family: ThemeFamily; mode: ThemeMode };

/** One variant of a family: its data-theme id and the tokens the picker's swatch half shows. */
export type ThemeVariant = {
  id: string;
  /** base-100: the half's own background. */
  base: string;
  /** base-content: the half's ink bar. */
  content: string;
  primary: string;
  success: string;
  warning: string;
};

/** One family: its id, its label, its variants' label, and the two variants. */
export type ThemeFamilyInfo = {
  id: ThemeFamily;
  label: string;
  /** The two variants' names, the way the card's subline shows them ("Mocha · Latte"). */
  variants: string;
  dark: ThemeVariant;
  light: ThemeVariant;
};

/** The families, in picker order; the swatch tokens mirror the shared theme.css. */
export const FAMILIES: readonly ThemeFamilyInfo[] = [
  {
    id: "catppuccin",
    label: "Catppuccin",
    variants: "Mocha · Latte",
    dark: {
      id: "mocha",
      base: "#1e1e2e",
      content: "#cdd6f4",
      primary: "#89b4fa",
      success: "#a6e3a1",
      warning: "#fab387",
    },
    light: {
      id: "latte",
      base: "#eff1f5",
      content: "#4c4f69",
      primary: "#1e66f5",
      success: "#40a02b",
      warning: "#fe640b",
    },
  },
  {
    id: "gruvbox",
    label: "Gruvbox",
    variants: "Dark · Light",
    dark: {
      id: "gruvbox-dark",
      base: "#32302f",
      content: "#ebdbb2",
      primary: "#83a598",
      success: "#b8bb26",
      warning: "#fe8019",
    },
    light: {
      id: "gruvbox-light",
      base: "#fbf1c7",
      content: "#3c3836",
      primary: "#076678",
      success: "#79740e",
      warning: "#af3a03",
    },
  },
  {
    id: "tokyo",
    label: "Tokyo",
    variants: "Night · Day",
    dark: {
      id: "tokyo-night",
      base: "#1a1b26",
      content: "#c0caf5",
      primary: "#7aa2f7",
      success: "#9ece6a",
      warning: "#ff9e64",
    },
    light: {
      id: "tokyo-day",
      base: "#e1e2e7",
      content: "#2d4e9c",
      primary: "#2e7de9",
      success: "#587539",
      warning: "#b15c00",
    },
  },
  {
    id: "rose-pine",
    label: "Rosé Pine",
    variants: "Main · Dawn",
    dark: {
      id: "rose-pine",
      base: "#1f1d2e",
      content: "#e0def4",
      primary: "#c4a7e7",
      success: "#9ccfd8",
      warning: "#f6c177",
    },
    light: {
      id: "rose-pine-dawn",
      base: "#fffaf3",
      content: "#575279",
      primary: "#907aa9",
      success: "#56949f",
      warning: "#ea9d34",
    },
  },
  {
    id: "nord",
    label: "Nord",
    variants: "Polar · Snow",
    dark: {
      id: "nord",
      base: "#3b4252",
      content: "#eceff4",
      primary: "#88c0d0",
      success: "#a3be8c",
      warning: "#d08770",
    },
    light: {
      id: "nord-light",
      base: "#eceff4",
      content: "#2e3440",
      primary: "#5e81ac",
      success: "#4c7a3a",
      warning: "#b5573d",
    },
  },
  {
    id: "solarized",
    label: "Solarized",
    variants: "Dark · Light",
    dark: {
      id: "solarized-dark",
      base: "#073642",
      content: "#93a1a1",
      primary: "#268bd2",
      success: "#859900",
      warning: "#cb4b16",
    },
    light: {
      id: "solarized-light",
      base: "#fdf6e3",
      content: "#4e6268",
      primary: "#268bd2",
      success: "#859900",
      warning: "#cb4b16",
    },
  },
];

/** The modes, in segment order: the key, its label and its icon. */
export const MODES: readonly (readonly [ThemeMode, string, IconName])[] = [
  ["system", "System", "monitor"],
  ["dark", "Dark", "moon"],
  ["light", "Light", "sun"],
];

/** Catppuccin Mocha: the default choice (SPEC 9.2). */
export const DEFAULT_PREF: ThemePref = { family: "catppuccin", mode: "dark" };

/** Does this browser prefer the dark palette right now? */
export const prefersDark = (mq: { matches: boolean }): boolean => mq.matches;

/** The family record a pref names, or Catppuccin's when it names none. */
function familyOf(id: ThemeFamily): ThemeFamilyInfo {
  return FAMILIES.find((f) => f.id === id) ?? (FAMILIES[0] as ThemeFamilyInfo);
}

/** The data-theme id <html> should carry: the family's variant for the resolved mode. */
export function resolvedTheme(pref: ThemePref, systemDark: boolean): string {
  const fam = familyOf(pref.family);
  return (pref.mode === "dark" || (pref.mode === "system" && systemDark) ? fam.dark : fam.light).id;
}

/** What a kept value means: a legacy single-palette name moves to its Catppuccin pref; an object
 *  must name a family and a mode; anything else is no choice at all. */
function migrate(raw: unknown): ThemePref | null {
  if (raw === "mocha" || raw === "dark") return { family: "catppuccin", mode: "dark" };
  if (raw === "latte" || raw === "light") return { family: "catppuccin", mode: "light" };
  if (raw === "system") return { family: "catppuccin", mode: "system" };
  if (
    typeof raw === "object" &&
    raw !== null &&
    FAMILIES.some((f) => f.id === (raw as { family?: unknown }).family) &&
    MODES.some(([m]) => m === (raw as { mode?: unknown }).mode)
  )
    return { family: (raw as { family: ThemeFamily }).family, mode: (raw as { mode: ThemeMode }).mode };
  return null;
}

/** The saved choice, Catppuccin dark when none was kept yet. A legacy name moves to its new pref
 *  in the same read. */
export function currentTheme(storage: Storage): ThemePref {
  const raw = readPref<unknown>(storage, "theme", null);
  const pref = migrate(raw);
  if (pref !== null && typeof raw === "string") writePref(storage, "theme", pref);
  return pref ?? DEFAULT_PREF;
}

/** Puts the resolved variant on <html>. */
export function applyTheme(pref: ThemePref, systemDark: boolean): void {
  document.documentElement.dataset.theme = resolvedTheme(pref, systemDark);
}

/** Keeps the choice and puts it on <html>; the menus re-render from the storage themselves. */
export function setTheme(storage: Storage, pref: ThemePref, systemDark: boolean): void {
  writePref(storage, "theme", pref);
  applyTheme(pref, systemDark);
}
