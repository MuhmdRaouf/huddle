// theme.ts — the light or dark scheme the tokens paint in, and the flavour they paint it with. The
// scheme is the OS's choice, unless this browser keeps an override (the switch in Settings) under
// "huddle:scheme". The resolved side lands on <html data-scheme>, which tokens.css and theme.css
// read, and the kept theme's id (the registry in themes.ts) lands on <html data-theme>, which
// theme.css's side rules and variants read; with the OS choice it follows every flip of
// prefers-color-scheme while the page is open. The installed app's status bar takes its colour from
// the theme-color metas: an override paints every one in its scheme with no media query, and the OS
// choice keeps the dark-then-light pair keyed to the OS. The flavour, kept under "huddle:flavour",
// lands on <html data-flavour>, where tokens.css remaps its colour tokens to Catppuccin latte or
// mocha; plain leaves the attribute off.
import { readTheme, sideOf } from "./themes.ts";

/** The two schemes tokens.css paints. */
export type Scheme = "light" | "dark";

/** What the switch keeps: the OS's choice, or one scheme regardless of it. */
export type SchemePref = "system" | Scheme;

/** The slice of localStorage the scheme needs; every call may throw on blocked site data. */
export type SchemeStore = Pick<Storage, "getItem" | "setItem">;

/** The slice of a prefers-color-scheme MediaQueryList the scheme needs. */
export type SchemeQuery = {
  readonly matches: boolean;
  addEventListener: (type: "change", fn: () => void) => void;
  removeEventListener: (type: "change", fn: () => void) => void;
};

/** The palettes the tokens paint a scheme with: their own, or Catppuccin mapped onto them. */
export type Flavour = "plain" | "catppuccin";

/** Where the override is kept. */
export const SCHEME_KEY = "huddle:scheme";

/** Where the flavour is kept. */
export const FLAVOUR_KEY = "huddle:flavour";

/** The override this browser keeps, or "system" when none (or site data is blocked). */
export function readScheme(store: SchemeStore): SchemePref {
  try {
    const raw = store.getItem(SCHEME_KEY);
    return raw === "light" || raw === "dark" ? raw : "system";
  } catch {
    return "system";
  }
}

/** Keeps the switch's choice; blocked site data keeps it for this page only. */
export function writeScheme(store: SchemeStore, pref: SchemePref): void {
  try {
    store.setItem(SCHEME_KEY, pref);
  } catch {
    // site data is blocked: the choice lasts until the page reloads
  }
}

/** The scheme a choice paints in, given whether the OS prefers dark right now. */
export function resolveScheme(pref: SchemePref, systemDark: boolean): Scheme {
  if (pref !== "system") return pref;
  return systemDark ? "dark" : "light";
}

/** The status bar colour of each scheme: tokens.css's plain --ui-bg, which .ui-shell paints under
 *  the bar. theme-boot.js and index.html carry the same two values. */
const BAR: Record<Scheme, string> = { light: "#f6f7f9", dark: "#0e1014" };

/** The schemes the theme-color metas name in document order when the OS chooses. */
const BAR_ORDER: readonly Scheme[] = ["dark", "light"];

/** Paints every theme-color meta in the chosen scheme, or keys the pair to the OS for "system". */
function paintBar(doc: Document, pref: SchemePref): void {
  doc.head.querySelectorAll<HTMLMetaElement>('meta[name="theme-color"]').forEach((meta, i) => {
    const scheme = pref === "system" ? (BAR_ORDER[i % 2] ?? "dark") : pref;
    meta.content = BAR[scheme];
    if (pref === "system") meta.media = `(prefers-color-scheme: ${scheme})`;
    else meta.removeAttribute("media");
  });
}

/** Puts the resolved side and the kept theme on the root element, paints the status bar to match,
 *  and answers the side. */
export function applyScheme(
  root: HTMLElement,
  pref: SchemePref,
  systemDark: boolean,
  store: SchemeStore = localStorage,
): Scheme {
  const scheme = resolveScheme(pref, systemDark);
  const theme = readTheme(store);
  root.dataset.theme = theme;
  root.dataset.scheme = sideOf(theme, scheme);
  paintBar(root.ownerDocument, pref);
  return scheme;
}

/** The flavour this browser keeps, or "plain" when none (or site data is blocked). */
export function readFlavour(store: SchemeStore): Flavour {
  try {
    return store.getItem(FLAVOUR_KEY) === "catppuccin" ? "catppuccin" : "plain";
  } catch {
    return "plain";
  }
}

/** Keeps the flavour; blocked site data keeps it for this page only. */
export function writeFlavour(store: SchemeStore, flavour: Flavour): void {
  try {
    store.setItem(FLAVOUR_KEY, flavour);
  } catch {
    // site data is blocked: the choice lasts until the page reloads
  }
}

/** Puts the flavour on the root element; plain takes the attribute off. */
export function applyFlavour(root: HTMLElement, flavour: Flavour): void {
  if (flavour === "plain") delete root.dataset.flavour;
  else root.dataset.flavour = flavour;
}

/** Applies the kept flavour, and the kept scheme now and again on every OS flip; the answer stops
 *  following. */
export function followScheme(root: HTMLElement, store: SchemeStore, query: SchemeQuery): () => void {
  applyFlavour(root, readFlavour(store));
  const apply = (): void => {
    applyScheme(root, readScheme(store), query.matches, store);
  };
  apply();
  query.addEventListener("change", apply);
  return () => query.removeEventListener("change", apply);
}
