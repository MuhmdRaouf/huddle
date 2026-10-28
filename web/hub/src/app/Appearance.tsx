// Appearance.tsx — Settings › Appearance: the System, Light and Dark control over one live
// preview card per registered theme. Each group is one radiogroup on the ARIA radio pattern: a
// single tab stop, the arrows move and pick. A pick paints at once — the scheme through
// pickScheme, the theme through applyTheme — and stays in this browser. A theme that paints only
// dark blocks Light: the control draws it at half strength and picking it does nothing. Each
// card's preview carries the theme's id and the side the Appearance choice resolves to — live,
// so a flip of the device's setting while System holds re-sides every preview — and theme.css
// paints it from that theme's own tokens; no colour is written here.

import { readScheme, resolveScheme, type SchemePref } from "@huddle/ui/theme.ts";
import { applyTheme, readTheme, sideOf, THEMES, themeOf } from "@huddle/ui/themes.ts";
import type { JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import { Icon } from "../icons.tsx";
import { pickScheme, SCHEMES, systemDark } from "./scheme.ts";

/** A section's heading, the same small caps on every Settings page. */
const HEAD = "mb-1.5 text-xs font-semibold tracking-[0.06em] uppercase text-[var(--ui-muted)]";

/** Muted body copy under a heading. */
const NOTE = "m-0 text-sm text-[var(--ui-muted)]";

/** The three scheme choices, in the order the control draws them. */
const SCHEME_ORDER: readonly SchemePref[] = ["system", "light", "dark"];

/** One segmented-control radio: a full tap target, the pick's ink on the checked one. */
const SEGMENT =
  "flex min-h-[var(--ui-tap)] cursor-pointer items-center justify-center rounded-[var(--ui-radius-sm)] text-sm font-semibold outline-none focus-visible:ring-2 focus-visible:ring-[var(--ui-accent)]";

/** One theme card. */
const CARD =
  "relative overflow-hidden rounded-[var(--ui-radius-lg)] border bg-[var(--ui-surface)] text-left shadow-[var(--ui-shadow-1)] outline-none cursor-pointer focus-visible:ring-2 focus-visible:ring-[var(--ui-accent)]";

/** The step an arrow key asks for: the four arrows in both directions, anything else none. */
function arrowStep(key: string): number {
  if (key === "ArrowRight" || key === "ArrowDown") return 1;
  if (key === "ArrowLeft" || key === "ArrowUp") return -1;
  return 0;
}

/** Settings › Appearance: the scheme control and the theme cards. */
export function Appearance(): JSX.Element {
  const [pref, setPref] = useState<SchemePref>(() => readScheme(localStorage));
  const [theme, setTheme] = useState<string>(() => readTheme(localStorage));
  // the device's side, kept live: while Appearance is System the previews and the metas follow a
  // flip of the OS's setting, not just the side the page booted with
  const [dark, setDark] = useState(systemDark);
  useEffect(() => {
    const query = window.matchMedia("(prefers-color-scheme: dark)");
    const follow = (): void => setDark(query.matches);
    // a flip that lands between the first render and this effect must not be lost
    follow();
    query.addEventListener("change", follow);
    return () => query.removeEventListener("change", follow);
  }, []);
  const darkOnly = themeOf(theme).sides.length === 1;
  const scheme = resolveScheme(pref, dark);

  const pickPref = (id: SchemePref): void => {
    pickScheme(localStorage, id);
    setPref(id);
  };

  const pickTheme = (id: string): void => {
    applyTheme(document.documentElement, id, pref, dark, localStorage);
    setTheme(id);
  };

  const onSchemeKey = (e: KeyboardEvent): void => {
    const step = arrowStep(e.key);
    if (step === 0) return;
    e.preventDefault();
    let at = SCHEME_ORDER.indexOf(pref);
    do {
      at = (at + step + SCHEME_ORDER.length) % SCHEME_ORDER.length;
      // a dark-only theme blocks Light: the arrows move over it
    } while (darkOnly && SCHEME_ORDER[at] === "light");
    const id = SCHEME_ORDER[at];
    if (id === undefined) return;
    pickPref(id);
    (e.currentTarget as HTMLElement).querySelector<HTMLElement>(`[data-scheme-value="${id}"]`)?.focus();
  };

  const onThemeKey = (e: KeyboardEvent): void => {
    const step = arrowStep(e.key);
    if (step === 0) return;
    e.preventDefault();
    const at = THEMES.findIndex((t) => t.id === theme);
    const next = THEMES[(at + step + THEMES.length) % THEMES.length];
    if (next === undefined) return;
    pickTheme(next.id);
    (e.currentTarget as HTMLElement).querySelector<HTMLElement>(`[data-theme-value="${next.id}"]`)?.focus();
  };

  return (
    <div class="flex flex-col gap-6">
      <section aria-labelledby="appearance-h" class="flex flex-col gap-3">
        <h2 id="appearance-h" class={HEAD}>
          Appearance
        </h2>
        <div
          role="radiogroup"
          aria-label="Appearance"
          onKeyDown={onSchemeKey}
          class="grid grid-cols-3 gap-1 rounded-[var(--ui-radius)] bg-[var(--ui-surface-2)] p-1"
        >
          {SCHEME_ORDER.map((id) => {
            const label = SCHEMES.find(([s]) => s === id)?.[1] ?? id;
            const blocked = id === "light" && darkOnly;
            const on = pref === id;
            return (
              <span
                key={id}
                role="radio"
                aria-checked={on}
                aria-disabled={blocked || undefined}
                data-scheme-value={id}
                tabindex={on ? 0 : -1}
                onClick={blocked ? undefined : () => pickPref(id)}
                class={`${SEGMENT} ${on ? "bg-[var(--ui-surface)] text-[var(--ui-text)]" : "text-[var(--ui-muted)]"} ${blocked ? "opacity-50" : ""}`}
              >
                {label}
              </span>
            );
          })}
        </div>
        <p class={NOTE}>System follows this device's setting.</p>
      </section>
      <section aria-labelledby="theme-h" class="flex flex-col gap-3">
        <h2 id="theme-h" class={HEAD}>
          Theme
        </h2>
        <div role="radiogroup" aria-label="Theme" onKeyDown={onThemeKey} class="grid grid-cols-2 gap-3">
          {THEMES.map((t) => {
            const on = t.id === theme;
            return (
              <span
                key={t.id}
                role="radio"
                aria-checked={on}
                data-theme-value={t.id}
                tabindex={on ? 0 : -1}
                onClick={() => pickTheme(t.id)}
                class={`${CARD} ${
                  on ? "border-[var(--ui-accent)] ring-1 ring-[var(--ui-accent)]" : "border-[var(--ui-line)]"
                }`}
              >
                <span
                  data-theme={t.id}
                  data-scheme={sideOf(t.id, scheme)}
                  class="block h-24 bg-[var(--ui-bg)] p-2.5"
                >
                  <span class="block rounded-[var(--ui-radius-sm)] border border-[var(--ui-line)] bg-[var(--ui-surface)] p-2">
                    <span class="block h-1.5 w-3/5 rounded-[var(--ui-radius-full)] bg-[var(--ui-text)]" />
                    <span class="mt-1 block h-1.5 w-2/5 rounded-[var(--ui-radius-full)] bg-[var(--ui-muted)]" />
                  </span>
                  <span class="mt-2 flex items-center gap-2">
                    <span class="h-3 w-10 rounded-[var(--ui-radius-full)] bg-[var(--ui-accent)]" />
                    <span class="size-2 rounded-full bg-[var(--ui-ok)]" />
                    <span class="size-2 rounded-full bg-[var(--ui-warn)]" />
                    <span class="size-2 rounded-full bg-[var(--ui-fail)]" />
                    <span class="size-2 rounded-full bg-[var(--ui-info)]" />
                  </span>
                </span>
                <span class="flex items-center gap-2 px-3 py-2.5">
                  <span class="min-w-0 flex-1 truncate text-sm font-semibold text-[var(--ui-text)]">
                    {t.label}
                  </span>
                  {t.sides.length === 1 ? (
                    <span class="shrink-0 rounded-[var(--ui-radius-sm)] border border-[var(--ui-line)] px-1.5 text-xs text-[var(--ui-muted)]">
                      Dark only
                    </span>
                  ) : null}
                </span>
                {on ? (
                  <span
                    aria-hidden="true"
                    class="absolute right-2 top-2 flex size-6 items-center justify-center rounded-full bg-[var(--ui-accent)] text-[var(--ui-on-accent)]"
                  >
                    <Icon name="check" class="size-3.5" />
                  </span>
                ) : null}
              </span>
            );
          })}
        </div>
        <p class={NOTE}>
          Dark-only themes stay dark when Appearance is Light. Your choice is kept in this browser.
        </p>
      </section>
    </div>
  );
}
