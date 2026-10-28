// theme.ts — the light or dark scheme the tokens paint in. The scheme is the OS's choice, unless
// this browser keeps an override (the switch in Settings) under "huddle:scheme". The resolved side
// lands on <html data-scheme>, which tokens.css and theme.css read, and the kept theme's id (the
// registry in themes.ts) lands on <html data-theme>, which theme.css's side rules and variants
// read; with the OS choice it follows every flip of prefers-color-scheme while the page is open.
// The installed app's status bar takes its colour from the theme-color metas, painting the kept
// theme's own bar for the side it paints: one colour on every meta for an override or a one-sided
// theme, and the dark-then-light pair keyed to the OS when System picks a two-sided theme.
import { readTheme, sideOf, themeOf, type Theme, type ThemeSide } from "./themes.ts";

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

/** Where the override is kept. */
export const SCHEME_KEY = "huddle:scheme";

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

/** The sides the theme-color metas name in document order when the OS chooses. */
const BAR_ORDER: readonly ThemeSide[] = ["dark", "light"];

/** Paints the kept theme's own bar colour into every theme-color meta: one side's colour on every
 *  meta when a side is already painted (an override, or a one-sided theme under System), and the
 *  dark-then-light pair keyed to the OS when System picks a two-sided theme. */
function paintBar(doc: Document, pref: SchemePref, theme: Theme): void {
  const metas = doc.head.querySelectorAll<HTMLMetaElement>('meta[name="theme-color"]');
  const keyed = pref === "system" && theme.sides.length > 1;
  const order: readonly ThemeSide[] = keyed
    ? BAR_ORDER
    : [sideOf(theme.id, pref === "system" ? "dark" : pref)];
  metas.forEach((meta, i) => {
    const side = order[i % order.length] ?? "dark";
    meta.content = theme.bar[side] ?? "";
    if (keyed) meta.media = `(prefers-color-scheme: ${side})`;
    else meta.removeAttribute("media");
  });
}

/** Puts the resolved side and the kept theme on the root element, paints the status bar to match,
 *  and answers the side. The store is the one the page already reads and writes the scheme and the
 *  theme through: reaching for the localStorage global here would itself throw on blocked site
 *  data. */
export function applyScheme(
  root: HTMLElement,
  pref: SchemePref,
  systemDark: boolean,
  store: SchemeStore,
): Scheme {
  const scheme = resolveScheme(pref, systemDark);
  const theme = themeOf(readTheme(store));
  root.dataset.theme = theme.id;
  root.dataset.scheme = sideOf(theme.id, scheme);
  paintBar(root.ownerDocument, pref, theme);
  return scheme;
}

/** Applies the kept scheme now and again on every OS flip; the answer stops following. */
export function followScheme(root: HTMLElement, store: SchemeStore, query: SchemeQuery): () => void {
  const apply = (): void => {
    applyScheme(root, readScheme(store), query.matches, store);
  };
  apply();
  query.addEventListener("change", apply);
  return () => query.removeEventListener("change", apply);
}
