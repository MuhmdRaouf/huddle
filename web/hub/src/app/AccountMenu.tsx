// AccountMenu.tsx — the avatar at the right end of the chrome and the menu it opens: who this
// browser is, which hub it talks to, and the one row that leads to Settings. The menu is modal: a
// scrim covers the page and a tap on it closes the menu. The avatar carries the health dot. Every
// colour comes from the tokens.

import { Button } from "@huddle/ui/button.tsx";
import type { JSX } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import { Icon } from "../icons.tsx";
import { useHuddle } from "./context.tsx";
import { type Layout, useLayout } from "./layout.ts";
import { HealthDot } from "./Nav.tsx";

/** What /health says about the hub this page talks to, for the menu's second line. */
type HubFacts = { machine: string; version: string };

/** The machine and version /health answers, checked at the edge. */
function hubFactsOf(r: unknown): HubFacts {
  const h = (r ?? {}) as { machine?: unknown; version?: unknown };
  const text = (v: unknown): string => (typeof v === "string" ? v : "");
  return { machine: text(h.machine), version: text(h.version) };
}

/** Reads /health once, when the menu opens; the facts stay empty until it answers or when it fails. */
function useHubFacts(): HubFacts {
  const [facts, setFacts] = useState<HubFacts>({ machine: "", version: "" });
  useEffect(() => {
    let on = true;
    fetch("/health")
      .then((r) => r.json())
      .then((h: unknown) => {
        if (on) setFacts(hubFactsOf(h));
      })
      .catch(() => {});
    return () => {
      on = false;
    };
  }, []);
  return facts;
}

/** The menu's first line: the display name before the credential's kind, or the kind alone when
 *  the hub names no one else. */
function identityLine(name: string | undefined, kind: string): string {
  return name && name !== kind ? `${name} · ${kind}` : kind;
}

/** The letter in the avatar disc: the first of the name, or of the credential's kind. */
const letterOf = (name: string | undefined, kind: string): string =>
  (name || kind || "account").charAt(0).toUpperCase();

/** The round letter, on the accent's soft fill. */
function Disc({ letter, size }: { letter: string; size: "sm" | "md" }): JSX.Element {
  return (
    <span
      aria-hidden="true"
      class={`flex shrink-0 items-center justify-center rounded-[var(--ui-radius-full)] bg-[var(--ui-accent-soft)] font-semibold text-[var(--ui-accent)] ${
        size === "md" ? "size-8 text-sm" : "size-7 text-meta"
      }`}
    >
      {letter}
    </span>
  );
}

/** The gap between the avatar and the menu's top edge: 8 px on the phone, 14 px beside the top bar. */
const GAP: Readonly<Record<Layout, number>> = { phone: 8, tablet: 14, desktop: 14 };

/** Where the menu sits: under the avatar, its right edge on the avatar's. */
type Anchor = { top: number; right: number };

/** The open menu over its scrim. Escape and a tap on the scrim close it; the Settings row leads on. */
function Menu({
  anchor,
  letter,
  identity,
  onClose,
}: {
  anchor: Anchor;
  letter: string;
  identity: string;
  onClose: (refocus: boolean) => void;
}): JSX.Element {
  const facts = useHubFacts();
  const row = useRef<HTMLAnchorElement>(null);
  // the keydown listener reads the close the shell handed this paint, so it stays one listener
  const closer = useRef(onClose);
  closer.current = onClose;
  const where = [facts.machine, location.host, facts.version].filter(Boolean).join(" · ");

  useEffect(() => {
    row.current?.focus();
    const on = (e: KeyboardEvent): void => {
      if (e.key !== "Escape") return;
      e.stopPropagation();
      closer.current(true);
    };
    document.addEventListener("keydown", on);
    return () => document.removeEventListener("keydown", on);
  }, []);

  return (
    <div class="fixed inset-0 z-[var(--ui-layer-popup)]">
      <div data-scrim="" class="absolute inset-0 bg-[var(--ui-scrim)]" onClick={() => onClose(true)} />
      <div
        role="dialog"
        aria-modal="true"
        aria-label="Account menu"
        class="absolute w-70 max-w-[calc(100vw-2rem)] overflow-hidden rounded-[var(--ui-radius)] bg-[var(--ui-surface)] shadow-[var(--ui-shadow-3)]"
        style={`top:${anchor.top}px;right:${anchor.right}px`}
      >
        <div class="flex items-center gap-3 px-4 py-3">
          <Disc letter={letter} size="md" />
          <span class="flex min-w-0 flex-col">
            <span class="truncate text-row leading-[1.3] font-semibold">{identity}</span>
            <span class="truncate text-meta text-[var(--ui-muted)]">{where}</span>
          </span>
        </div>
        <a
          ref={row}
          href="#/settings"
          onClick={() => onClose(false)}
          class="relative flex min-h-12 items-center gap-3 px-4 py-2 text-row font-medium outline-none focus-visible:ring-2 focus-visible:ring-[var(--ui-accent)] focus-visible:ring-inset"
        >
          <span aria-hidden="true" class="absolute top-0 right-0 left-12 h-px bg-[var(--ui-line)]" />
          <span class="flex size-7 shrink-0 items-center justify-center rounded-[var(--ui-radius-sm)] bg-[var(--ui-surface-2)] text-[var(--ui-muted)]">
            <Icon name="gear" class="size-4" />
          </span>
          <span class="flex-1">Settings</span>
          <Icon name="right" class="size-4 text-[var(--ui-muted)]" />
        </a>
      </div>
    </div>
  );
}

/** The avatar button and, while it is open, the account menu. `healthy` is whether every part of
 *  the hub is well; a failing one puts the red dot on the avatar. */
export function AccountMenu({ healthy }: { healthy: boolean }): JSX.Element {
  const { who } = useHuddle();
  const [anchor, setAnchor] = useState<Anchor | null>(null);
  const anchorEl = useRef<HTMLSpanElement>(null);
  const layout = useLayout();
  const letter = letterOf(who.name, who.kind);

  const close = (refocus: boolean): void => {
    setAnchor(null);
    if (refocus) anchorEl.current?.querySelector("button")?.focus();
  };
  const open = (): void => {
    const r = anchorEl.current?.getBoundingClientRect();
    setAnchor({
      top: (r?.bottom ?? 0) + GAP[layout],
      right: Math.max(8, window.innerWidth - (r?.right ?? 0)),
    });
  };

  return (
    <>
      <span ref={anchorEl} class="relative flex shrink-0">
        <Button
          variant="ghost"
          label="Account menu"
          hasPopup="dialog"
          expanded={anchor !== null}
          data-avatar=""
          onClick={open}
          class="ui-btn-icon relative min-h-[var(--ui-tap)] rounded-[var(--ui-radius-full)] text-[var(--ui-accent)]"
        >
          <Disc letter={letter} size="sm" />
          <span class="absolute top-1 right-1">
            <HealthDot healthy={healthy} />
          </span>
        </Button>
      </span>
      {anchor ? (
        <Menu anchor={anchor} letter={letter} identity={identityLine(who.name, who.kind)} onClose={close} />
      ) : null}
    </>
  );
}
