/* theme-boot.js — the scheme and the theme before first paint: the override this browser keeps under
 * "huddle:scheme" ("light" or "dark"), else the OS's choice. It sets <html data-scheme>, which the
 * tokens and the side rules read, and the kept theme's id on <html data-theme> (a value outside the
 * registry is the default), which theme.css reads. It also sets <html data-standalone> when the page
 * runs as the installed app, which the shell's styles read. It paints the theme-color metas (the
 * installed app's status bar) in tokens.css's plain --ui-bg: an override sets every one to its scheme
 * with no media query; the OS choice leaves the dark-then-light pair index.html ships, which sits
 * ahead of this script. A file of its own because the page's CSP allows no inline script (SPEC 7.5).
 * Mirrors ui/src/theme.ts and the registry's ids in ui/src/themes.ts; keep the three in step. */

(function () {
  /** The registered theme ids, mirroring web/ui/src/themes.ts's THEMES. */
  const THEME_IDS = ["huddle"];
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
  document.documentElement.dataset.theme = theme;
  document.documentElement.dataset.scheme = dark ? "dark" : "light";
  const installed = navigator.standalone === true || window.matchMedia("(display-mode: standalone)").matches;
  if (installed) document.documentElement.dataset.standalone = "true";
  if (pref !== "system") {
    for (const meta of document.querySelectorAll('meta[name="theme-color"]')) {
      meta.content = dark ? "#0e1014" : "#f6f7f9";
      meta.removeAttribute("media");
    }
  }
})();
