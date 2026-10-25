// theme.ts — the light or dark scheme the tokens paint in, and the flavour they paint it with. The
// scheme is the OS's choice, unless this browser keeps an override (the switch in Settings) under
// "huddle:scheme". The resolved scheme lands on <html data-scheme>, which tokens.css reads, and as the
// matching Catppuccin variant on <html data-theme>, which the daisyUI themes in theme.css read; with
// the OS choice it follows every flip of prefers-color-scheme while the page is open. The flavour,
// kept under "huddle:flavour", lands on <html data-flavour>, where tokens.css remaps its colour
// tokens to Catppuccin latte or mocha; plain leaves the attribute off.

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

/** The daisyUI variant each scheme paints the pages not yet on the tokens in. */
const VARIANT: Record<Scheme, string> = { dark: "mocha", light: "latte" };

/** Puts the resolved scheme and its daisyUI variant on the root element and answers the scheme. */
export function applyScheme(root: HTMLElement, pref: SchemePref, systemDark: boolean): Scheme {
  const scheme = resolveScheme(pref, systemDark);
  root.dataset.scheme = scheme;
  root.dataset.theme = VARIANT[scheme];
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
    applyScheme(root, readScheme(store), query.matches);
  };
  apply();
  query.addEventListener("change", apply);
  return () => query.removeEventListener("change", apply);
}
