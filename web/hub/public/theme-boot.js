/* theme-boot.js — the theme before first paint: the saved choice (a family plus a mode, kept as
 * JSON under "huddle:theme"; a legacy "mocha"/"latte"/"system"/"dark"/"light" reads as its new
 * pref and is written back), else Catppuccin dark, the default. A file of its own because the
 * page's CSP allows no inline script (SPEC 7.5). Mirrors ui/src/app/theme.ts; keep the two in
 * step. */
(function () {
  const ids = {
    catppuccin: ["mocha", "latte"],
    gruvbox: ["gruvbox-dark", "gruvbox-light"],
    tokyo: ["tokyo-night", "tokyo-day"],
    "rose-pine": ["rose-pine", "rose-pine-dawn"],
    nord: ["nord", "nord-light"],
    solarized: ["solarized-dark", "solarized-light"],
  };
  let pref = { family: "catppuccin", mode: "dark" };
  try {
    const raw = JSON.parse(localStorage.getItem("huddle:theme") || "null");
    if (raw === "mocha" || raw === "dark") pref = { family: "catppuccin", mode: "dark" };
    else if (raw === "latte" || raw === "light") pref = { family: "catppuccin", mode: "light" };
    else if (raw === "system") pref = { family: "catppuccin", mode: "system" };
    else if (
      raw !== null &&
      typeof raw === "object" &&
      ids[raw.family] !== undefined &&
      (raw.mode === "system" || raw.mode === "dark" || raw.mode === "light")
    )
      pref = { family: raw.family, mode: raw.mode };
    if (typeof raw === "string") localStorage.setItem("huddle:theme", JSON.stringify(pref));
  } catch {
    /* site data is blocked: the default stands until the app can ask again */
  }
  const dark =
    pref.mode === "dark" || (pref.mode === "system" && window.matchMedia("(prefers-color-scheme: dark)").matches);
  document.documentElement.dataset.theme = ids[pref.family][dark ? 0 : 1];
})();
