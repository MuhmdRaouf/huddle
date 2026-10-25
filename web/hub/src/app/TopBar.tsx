// TopBar.tsx — the desktop top bar (52 px) beside the sidebar: the jump field with its ⌘K hint,
// the machines line with its live dot, the theme menu and the shortcuts key. The five tabs live in
// Nav.tsx, the phone header in App.tsx, with the shell that draws them. Every colour comes from
// the tokens.

import { type MenuItem, PopMenu } from "@huddle/ui/menu.tsx";
import { readScheme, type SchemePref } from "@huddle/ui/theme.ts";
import type { JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import { Icon } from "../icons.tsx";
import type { LiveStatus } from "../store.ts";
import { isMac } from "./Palette.tsx";
import { pickScheme, SCHEMES } from "./scheme.ts";

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
  const dot =
    live === "live"
      ? "bg-[var(--ui-ok)]"
      : live === "offline"
        ? "bg-[var(--ui-warn)]"
        : "bg-[var(--ui-idle)]";
  return (
    <span
      class="flex min-w-0 items-center gap-1.5 text-[13px] text-[var(--ui-muted)]"
      data-machines=""
      role="img"
      aria-label="Machines"
      title={t}
    >
      <span
        class={`size-2 shrink-0 rounded-[var(--ui-radius-full)] ${dot}`}
        data-live={live}
        aria-hidden="true"
      />
      <span class="truncate">{text ?? "This machine"}</span>
    </span>
  );
}

/** The scheme menu: the kept choice's icon on the button, System, Dark and Light under it. */
export function ThemeMenu(): JSX.Element {
  const [open, setOpen] = useState(false);
  const [btn, setBtn] = useState<HTMLButtonElement | null>(null);
  const [pref, setPref] = useState<SchemePref>(() => readScheme(localStorage));

  const pick = (next: SchemePref): void => {
    pickScheme(localStorage, next);
    setPref(next);
  };

  const row = SCHEMES.find((x) => x[0] === pref) ?? SCHEMES[0];
  const items: MenuItem[] = SCHEMES.map(([k, l, ic]) => ({
    label: l,
    icon: <Icon name={ic} />,
    checked: pref === k,
    run: () => pick(k),
  }));
  return (
    <>
      <button
        type="button"
        ref={setBtn}
        id="theme"
        class="ui-btn ui-btn-ghost ui-btn-icon shrink-0"
        aria-haspopup="menu"
        aria-expanded="false"
        aria-label={`Theme: ${row?.[1] ?? "System"}`}
        title={`Theme: ${row?.[1] ?? "System"}`}
        onClick={() => {
          // Settings may have changed the choice since this menu last drew
          setPref(readScheme(localStorage));
          setOpen(true);
        }}
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
    <header
      class="flex h-[52px] min-h-[52px] items-center gap-5 border-b border-[var(--ui-line)] bg-[var(--ui-surface)] px-5"
      data-desktop-bar=""
    >
      <button
        type="button"
        id="jump"
        class="ml-auto flex h-[var(--ui-tap)] w-[300px] min-w-0 items-center gap-2 rounded-[var(--ui-radius)] border border-[var(--ui-line-strong)] bg-[var(--ui-surface-2)] px-3 text-[13px] text-[var(--ui-muted)] outline-none focus-visible:ring-2 focus-visible:ring-[var(--ui-accent)]"
        aria-keyshortcuts="Meta+K Control+K /"
        title="Jump to a session (⌘K)"
        onClick={() => onPalette?.()}
      >
        <Icon name="search" class="size-4 shrink-0" />
        <span class="min-w-0 truncate">Jump to a session</span>
        <kbd
          class="ui-mono ml-auto shrink-0 rounded-[var(--ui-radius-sm)] border border-[var(--ui-line)] bg-[var(--ui-surface)] px-1.5 text-[length:var(--ui-text-xs)]"
          aria-hidden="true"
        >
          {mac ? "⌘K" : "Ctrl K"}
        </kbd>
      </button>
      <MachinesLine live={live} text={machines} />
      <ThemeMenu />
      <button
        type="button"
        class="ui-btn ui-btn-ghost ui-btn-icon shrink-0"
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
