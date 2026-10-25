/* theme-boot.js — the scheme before first paint: the override this browser keeps under
 * "huddle:scheme" ("light" or "dark"), else the OS's choice. It sets <html data-scheme>, which the
 * tokens read, and the matching Catppuccin variant on <html data-theme>, which the daisyUI themes
 * read. A file of its own because the page's CSP allows no inline script (SPEC 7.5). Mirrors
 * ui/src/theme.ts; keep the two in step. */
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
})();
