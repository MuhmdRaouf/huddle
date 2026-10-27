/* theme-boot.js — the scheme and the theme before first paint: the override this browser keeps under
 * "huddle:scheme" ("light" or "dark"), else the OS's choice. It sets <html data-scheme>, which the
 * tokens and the side rules read, and the kept theme's id on <html data-theme> (a value outside the
 * registry is the default), which theme.css reads. It also sets <html data-standalone> when the page
 * runs as the installed app, which the shell's styles read. It paints the theme-color metas (the
 * installed app's status bar) with the kept theme's own bar colour: one side's colour on every meta
 * with no media query when a side is already painted, and the dark-then-light pair keyed to the OS
 * when the OS picks a two-sided theme. A file of its own because the page's CSP allows no inline
 * script (SPEC 7.5). Mirrors ui/src/theme.ts and the registry's ids and bars in ui/src/themes.ts;
 * keep the three in step. */

(function () {
  /** The registered theme ids, mirroring web/ui/src/themes.ts's THEMES. */
  const THEME_IDS = ["huddle", "tokyo-night", "monokai", "dracula", "nord"];
  /** Each theme's status bar colour per side it paints, mirroring the registry's bar map. */
  const THEME_BARS = {
    "huddle": { "light": "#f4f5f7", "dark": "#0b0c0f" },
    "tokyo-night": { "light": "#d0d5e3", "dark": "#16161e" },
    "monokai": { "dark": "#1e1f1c" },
    "dracula": { "dark": "#21222c" },
    "nord": { "light": "#e5e9f0", "dark": "#2e3440" }
  };
  let pref = "system";
  try {
    const raw = localStorage.getItem("huddle:scheme");
    if (raw === "light" || raw === "dark") pref = raw;
  } catch {
    /* site data is blocked: the OS's choice stands */
  }
  let theme = THEME_IDS[0] ?? "huddle";
  try {
    const raw = localStorage.getItem("huddle:theme");
    if (raw !== null && THEME_IDS.includes(raw)) theme = raw;
  } catch {
    /* site data is blocked: the default theme stands */
  }
  const dark = pref === "dark" || (pref === "system" && window.matchMedia("(prefers-color-scheme: dark)").matches);
  const bars = THEME_BARS[theme] ?? {};
  const sides = ["light", "dark"].filter((s) => bars[s] !== undefined);
  const scheme = dark ? "dark" : "light";
  // a one-sided theme answers with its only side whatever the scheme asks
  const side = sides.length === 1 ? (sides[0] ?? scheme) : scheme;
  document.documentElement.dataset.theme = theme;
  document.documentElement.dataset.scheme = side;
  const installed = navigator.standalone === true || window.matchMedia("(display-mode: standalone)").matches;
  if (installed) document.documentElement.dataset.standalone = "true";
  const metas = document.querySelectorAll('meta[name="theme-color"]');
  if (pref === "system" && sides.length > 1) {
    ["dark", "light"].forEach((one, i) => {
      const meta = metas[i % 2];
      if (meta) {
        meta.content = bars[one] ?? "";
        meta.media = "(prefers-color-scheme: " + one + ")";
      }
    });
  } else {
    for (const meta of metas) {
      meta.content = bars[side] ?? "";
      meta.removeAttribute("media");
    }
  }
})();
