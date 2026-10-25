/* theme-boot.js — the scheme before first paint: the override this browser keeps under
 * "huddle:scheme" ("light" or "dark"), else the OS's choice. It sets <html data-scheme>, which the
 * tokens read, and the matching Catppuccin variant on <html data-theme>, which the daisyUI themes
 * read. It paints the theme-color metas (the installed app's status bar) in tokens.css's plain
 * --ui-bg: an override sets every one to its scheme with no media query; the OS choice leaves the
 * dark-then-light pair index.html ships, which sits ahead of this script. A file of its own because
 * the page's CSP allows no inline script (SPEC 7.5). Mirrors ui/src/theme.ts; keep the two in step. */
(function () {
  let pref = "system";
  try {
    const raw = localStorage.getItem("huddle:scheme");
    if (raw === "light" || raw === "dark") pref = raw;
  } catch {
    /* site data is blocked: the OS's choice stands */
  }
  const dark = pref === "dark" || (pref === "system" && window.matchMedia("(prefers-color-scheme: dark)").matches);
  document.documentElement.dataset.scheme = dark ? "dark" : "light";
  document.documentElement.dataset.theme = dark ? "mocha" : "latte";
  if (pref !== "system") {
    for (const meta of document.querySelectorAll('meta[name="theme-color"]')) {
      meta.content = dark ? "#0e1014" : "#f6f7f9";
      meta.removeAttribute("media");
    }
  }
})();
