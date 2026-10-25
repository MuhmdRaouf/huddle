// theme.ts — the light or dark scheme the tokens paint in: the OS's choice, unless this browser keeps
// an override (the switch in Settings) under "huddle:scheme". The resolved scheme lands on
// <html data-scheme>, which tokens.css reads, and as the matching Catppuccin variant on
// <html data-theme>, which the daisyUI themes in theme.css read; with the OS choice it follows
// every flip of prefers-color-scheme while the page is open.

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

/** The daisyUI variant each scheme paints the pages not yet on the tokens in. */
const VARIANT: Record<Scheme, string> = { dark: "mocha", light: "latte" };

/** Puts the resolved scheme and its daisyUI variant on the root element and answers the scheme. */
export function applyScheme(root: HTMLElement, pref: SchemePref, systemDark: boolean): Scheme {
  const scheme = resolveScheme(pref, systemDark);
  root.dataset.scheme = scheme;
  root.dataset.theme = VARIANT[scheme];
  return scheme;
}

/** Applies the kept choice now and again on every OS flip; the answer stops following. */
export function followScheme(root: HTMLElement, store: SchemeStore, query: SchemeQuery): () => void {
  const apply = (): void => {
    applyScheme(root, readScheme(store), query.matches);
  };
  apply();
  query.addEventListener("change", apply);
  return () => query.removeEventListener("change", apply);
}
