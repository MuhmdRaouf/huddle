// TopBar.tsx — the desktop top bar (52 px) beside the sidebar: the jump field with its ⌘K hint,
// the machines line with its live dot, the theme menu and the shortcuts key. The five tabs live in
// Nav.tsx, the phone header in App.tsx, with the shell that draws them.

import { type MenuItem, PopMenu } from "@huddle/ui/menu.tsx";
import type { JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import { Icon } from "../icons.tsx";
import type { LiveStatus } from "../store.ts";
import { isMac } from "./Palette.tsx";
import { applyTheme, currentTheme, MODES, setTheme, type ThemePref } from "./theme.ts";

/** Reads the server's version once for the brand's badge; "0.0.1" stands in until /health answers. */
export function useVersion(): string {
  const [v, setV] = useState("0.0.1");
  useEffect(() => {
    let on = true;
    fetch("/health")
      .then((r) => r.json())
      .then((h: { version?: string }) => {
        if (on && h.version) setV(h.version);
      })
      .catch(() => {});
    return () => {
      on = false;
    };
  }, []);
  return v;
}

/** The machines line: the live dot over the machines the registry reports; until one answers,
 *  this machine is the one there is. */
export function MachinesLine({ live, text }: { live: LiveStatus; text?: string | undefined }): JSX.Element {
  const t =
    live === "live"
      ? "Live updates: on"
      : live === "offline"
        ? "Live updates: reconnecting"
        : "Live updates: connecting";
  const dot = live === "live" ? "status-success" : live === "offline" ? "status-warning" : "status-neutral";
  return (
    <span
      class="flex min-w-0 items-center gap-1.5 text-[13px] text-base-content/60"
      data-machines=""
      role="img"
      aria-label="Machines"
      title={t}
    >
      <span class={`status ${dot} shrink-0`} aria-hidden="true" />
      <span class="truncate">{text ?? "This machine"}</span>
    </span>
  );
}

/** The theme menu: the current mode's icon on the button, the three modes under it. The modes
 *  re-pick between the kept family's two variants; the family itself is Settings' picker's. */
export function ThemeMenu(): JSX.Element {
  const [open, setOpen] = useState(false);
  const [btn, setBtn] = useState<HTMLButtonElement | null>(null);
  const [theme, setThemeState] = useState<ThemePref>(() => currentTheme(localStorage));

  // the theme choice goes on <html> right away, the way the boot script did before the app loaded
  useEffect(() => {
    applyTheme(currentTheme(localStorage), window.matchMedia("(prefers-color-scheme: dark)").matches);
  }, []);

  // the variant follows the system while "System" is the mode
  useEffect(() => {
    const mq = window.matchMedia("(prefers-color-scheme: dark)");
    const on = (): void => {
      const pref = currentTheme(localStorage);
      if (pref.mode === "system") applyTheme(pref, mq.matches);
    };
    mq.addEventListener?.("change", on);
    return () => mq.removeEventListener?.("change", on);
  }, []);

  const pick = (mode: ThemePref["mode"]): void => {
    const pref = { ...currentTheme(localStorage), mode };
    setTheme(localStorage, pref, window.matchMedia("(prefers-color-scheme: dark)").matches);
    setThemeState(pref);
  };

  const row = MODES.find((x) => x[0] === theme.mode) ?? MODES[0];
  const items: MenuItem[] = MODES.map(([k, l, ic]) => ({
    label: l,
    icon: <Icon name={ic} />,
    checked: theme.mode === k,
    run: () => pick(k),
  }));
  return (
    <>
      <button
        type="button"
        ref={setBtn}
        id="theme"
        class="btn btn-ghost btn-square btn-sm shrink-0"
        aria-haspopup="menu"
        aria-expanded="false"
        aria-label={`Theme: ${row?.[1] ?? "System"}`}
        title={`Theme: ${row?.[1] ?? "System"}`}
        onClick={() => setOpen(true)}
      >
        <Icon name={row?.[2] ?? "monitor"} />
      </button>
      {open && btn ? <PopMenu anchor={btn} items={items} onClose={() => setOpen(false)} /> : null}
    </>
  );
}

/** The whole desktop bar: the jump field, the machines line, the theme menu and the shortcuts
 *  key, over the shell's panes. */
export function DesktopBar({
  live,
  machines,
  onPalette,
  onHelp,
}: {
  live: LiveStatus;
  /** The machines line's text, from the registry. */
  machines?: string | undefined;
  onPalette?: (() => void) | undefined;
  onHelp?: (() => void) | undefined;
}): JSX.Element {
  const mac = isMac(navigator.platform || navigator.userAgent);
  return (
    <header class="flex h-[52px] min-h-[52px] items-center gap-5 border-b hairline bg-base-200 px-5">
      <button
        type="button"
        id="jump"
        class="ml-auto flex h-[34px] w-[300px] min-w-0 items-center gap-2 rounded-lg border hairline bg-base-300 px-3 text-[13px] text-base-content/50 outline-none focus-visible:ring-2 focus-visible:ring-primary"
        aria-keyshortcuts="Meta+K Control+K /"
        title="Jump to a session (⌘K)"
        onClick={() => onPalette?.()}
      >
        <Icon name="search" class="size-4 shrink-0" />
        <span class="min-w-0 truncate">Jump to a session</span>
        <kbd class="kbd kbd-sm ml-auto shrink-0" aria-hidden="true">
          {mac ? "⌘K" : "Ctrl K"}
        </kbd>
      </button>
      <MachinesLine live={live} text={machines} />
      <ThemeMenu />
      <button
        type="button"
        class="btn btn-ghost btn-square btn-sm shrink-0"
        aria-label="Keyboard shortcuts"
        aria-keyshortcuts="?"
        title="Keyboard shortcuts (?)"
        onClick={() => onHelp?.()}
      >
        <span class="text-base">?</span>
      </button>
    </header>
  );
}
