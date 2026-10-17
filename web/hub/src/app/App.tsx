// App.tsx — the shell: one component tree that draws its chrome per viewport (a header, the pane
// and a five-link bottom nav under 640 px; a 72 px icon rail with a list and a detail pane to
// 1023 px; a 52 px top bar with list, detail and context panes from 1024 px) over the tab the
// address names — Now, Channels, Radar, Kanban or Settings. Every later tab fills its own panes; today's
// channel pages stay reachable under Channels, with their destination tabs, their intro and the
// session rail beside them. The signed-out and no-channel pages stand alone.

import { Icon as SharedIcon } from "@huddle/ui/icons.tsx";
import { PageIntro, Panel } from "@huddle/ui/page.tsx";
import type { Stamp } from "@huddle/ui/time.ts";
import type { ComponentChildren, ComponentType, JSX } from "preact";
import { useCallback, useEffect, useMemo, useRef, useState } from "preact/hooks";
import type { ComposeOptions } from "../compose/drafts.ts";
import { Icon, type IconName, Logo } from "../icons.tsx";
import { Time } from "../kit.tsx";
import { KanbanTab } from "../kanban/KanbanTab.tsx";
import { assertionFromJSON, credentialToJSON } from "../passkey.ts";
import { RadarTab } from "../radar/app/RadarTab.tsx";
import { radarAct, useRadarState } from "../radar/mount.tsx";
import { machinesLine, machinesOf } from "../registry.ts";
import { FIN } from "../status.ts";
import { readPref, writePref } from "../storage.ts";
import type { HuddleState } from "../store.ts";
import { errText, useHuddle } from "./context.tsx";
import { IntroCtx } from "./intro.tsx";
import { type Layout, useLayout } from "./layout.ts";
import { NowContext, NowPane, SessionScreen } from "./Now.tsx";
import { Pair } from "./Pair.tsx";
import { Rail } from "./Rail.tsx";
import {
  channelHref,
  type Dest,
  parseHash,
  RADAR_VIEWS,
  type RadarView,
  radarLabel,
  TABS,
  type Tab,
  tabHref,
  tabLabel,
} from "./router.ts";
import { SettingsTab, settingsSectionOf } from "./SettingsTab.tsx";
import { DesktopBar, MachinesLine } from "./TopBar.tsx";

/** What a page registry holds: one component per channel page, picked by name. */
export type Pages = Record<string, ComponentType>;

/** What the composer needs from the shell: open it, with a prefill when the caller has one. */
export type ComposeOpen = (prefill?: ComposeOptions) => void;

/** The label a channel page shows when its page is missing (Home's is the channels list itself). */
const LABELS: Record<Dest, string> = {
  home: "Channels",
  overview: "Overview",
  today: "Today",
  inbox: "Inbox",
  team: "Team",
  work: "Work",
  knowledge: "Knowledge",
  settings: "Settings",
};

/** What each channel page shows and does there: the intro the shell paints above every page, so
 *  a page starts with its panels and never paints a title of its own. */
const DEST_INTRO: Record<Dest, { icon: IconName; label: string; description: ComponentChildren }> = {
  home: {
    icon: "layers",
    label: "Channels",
    description: "Each channel is one team of sessions. The ones that need you come first.",
  },
  overview: {
    icon: "activity",
    label: "Overview",
    description: "The channel at a glance: the plan, the team and what needs you.",
  },
  today: {
    icon: "clock",
    label: "Today",
    description: "What each session got done, what is stuck and what still waits for an answer.",
  },
  inbox: {
    icon: "inbox",
    label: "Inbox",
    description:
      "Only what needs you: questions, approvals, paused sessions, blocked tasks and Radar's alerts. Answer or dismiss a card and it leaves the list.",
  },
  team: {
    icon: "users",
    label: "Team",
    description: "Who is in, what each is doing, and everything that happened.",
  },
  work: {
    icon: "list",
    label: "Work",
    description:
      "Every task the plan carries, as a list, board, graph or map. Filter them, change a status, or open a task to work it.",
  },
  knowledge: {
    icon: "book",
    label: "Knowledge",
    description: "What the sessions learned and shared, so nobody pays for it twice.",
  },
  settings: {
    icon: "sliders",
    label: "Settings",
    description:
      "The channel's identity, turn and repo, and how this browser hears that a session needs you.",
  },
};

/** The three tab groups over a channel's pages: the channel's own, the work pages, the system's. */
const NAV_GROUPS: { label: string; tabs: Exclude<Dest, "home">[] }[] = [
  { label: "Channel", tabs: ["overview", "today", "inbox", "team"] },
  { label: "Work", tabs: ["work", "knowledge"] },
  { label: "System", tabs: ["settings"] },
];

/** The g chords' destinations: g n, g c, g r, g b and g s open the five tabs. */
const CHORD: Readonly<Record<string, Tab>> = {
  n: "now",
  c: "channels",
  r: "radar",
  b: "kanban",
  s: "settings",
};

/** The Inbox badge over the paint's snapshot: the attention items plus the extras' needs — the
 *  same count the store's inboxCount() computes, read here from state alone. */
export function inboxOf(state: HuddleState): number {
  const a = state.attention;
  const att = a
    ? (a.asks?.length ?? 0) + (a.gates?.length ?? 0) + (a.paused?.length ?? 0) + (a.blocked?.length ?? 0)
    : 0;
  const x = state.extras;
  const needs = x && x.ch === state.ch ? x.approvals.length + (x.obs?.alerts?.length ?? 0) : 0;
  return att + needs;
}

/** The counts the channel tabs read: the Inbox needs-you badge, the online sessions, the open tasks. */
export type NavCounts = { n: number; sessions: number; open: number };

/** The count a channel tab carries: the Inbox's needs-you in error red, the Team's online sessions
 *  and Work's open tasks in quiet grey; the pages without a count and a zero draw nothing. */
function tabBadge(dest: Dest, c: NavCounts): JSX.Element | null {
  if (dest === "inbox") return c.n > 0 ? <span class="badge badge-error badge-sm tnum">{c.n}</span> : null;
  const n = dest === "team" ? c.sessions : dest === "work" ? c.open : 0;
  return n > 0 ? <span class="num badge badge-ghost badge-sm">{n}</span> : null;
}

/** One channel tab: its icon, its label, the counts that follow it, and the active tab's glow. */
function ViewTab({
  dest,
  counts,
  active,
}: {
  dest: Exclude<Dest, "home">;
  counts: NavCounts;
  active: boolean;
}): JSX.Element {
  const { state, go } = useHuddle();
  const ch = state.ch ?? "";
  const meta = DEST_INTRO[dest];
  return (
    <button
      type="button"
      role="tab"
      data-dest={dest}
      class={
        active ? "tab tab-active h-10 gap-2 px-4 text-[0.9375rem]" : "tab h-10 gap-2 px-4 text-[0.9375rem]"
      }
      aria-selected={active ? "true" : "false"}
      aria-current={active ? "page" : undefined}
      onClick={() => go(channelHref(ch, `/${dest}`))}
    >
      <Icon name={meta.icon} class="size-4.5" />
      <span>{meta.label}</span>
      {tabBadge(dest, counts)}
    </button>
  );
}

/** The rows of tab groups over a channel's pages: three daisyUI tabs boxes with a gap, the active
 *  tab glowing. The ids are the pages, the address moves by hash. */
function ViewTabs({ dest, counts }: { dest: Dest; counts: NavCounts }): JSX.Element {
  return (
    <div class="flex w-full flex-wrap gap-3 px-4 pt-4 sm:px-6" data-view-tabs="">
      {NAV_GROUPS.map((group) => (
        <div key={group.label} role="tablist" aria-label={group.label} class="tabs tabs-box">
          {group.tabs.map((k) => (
            <ViewTab key={k} dest={k} counts={counts} active={dest === k} />
          ))}
        </div>
      ))}
    </div>
  );
}

/** A placeholder pane: what a tab shows until its own screen lands. */
export function EmptyPane({ title, line }: { title: string; line?: string | undefined }): JSX.Element {
  return (
    <div class="mx-auto flex max-w-lg flex-col items-center gap-2 rounded-box border border-dashed border-base-content/20 px-6 py-16 text-center text-sm muted">
      <b class="text-base font-semibold text-base-content">{title}</b>
      <p>{line ?? "Coming soon."}</p>
    </div>
  );
}

/** The placeholder for a channel page no page is registered for yet. */
export function ComingSoon({ label }: { label: string }): JSX.Element {
  return (
    <div class="mx-auto max-w-3xl py-6">
      <div class="flex flex-col items-center gap-2 rounded-box border border-dashed border-base-content/20 px-6 py-10 text-center text-sm muted">
        <b class="text-base font-semibold text-base-content">{label}</b>
        <p>Coming soon.</p>
      </div>
    </div>
  );
}

/** One tab's mark: the shared glyphs for the three the design draws, the settings sunburst and
 *  the kanban columns from the owner UI's own set. */
function TabGlyph({ id, class: c }: { id: Tab; class?: string | undefined }): JSX.Element {
  return id === "settings" ? (
    <Icon name="sparkle" class={c} />
  ) : id === "kanban" ? (
    <Icon name="columns" class={c} />
  ) : (
    <SharedIcon name={id} class={c} />
  );
}

/** The needs-you count a tab's mark carries; nothing when nobody needs the owner. */
function TabBadge({ n }: { n: number }): JSX.Element | null {
  return n > 0 ? <span class="badge badge-warning badge-sm tnum">{n}</span> : null;
}

/** The phone chrome's bottom nav: five links of 52 px targets under the safe-area inset. */
function BottomNav({ tab, n }: { tab: Tab; n: number }): JSX.Element {
  return (
    <nav
      data-nav="bottom"
      aria-label="Main"
      class="grid shrink-0 grid-cols-5 border-t hairline bg-base-200 px-1 pt-1.5 pb-[env(safe-area-inset-bottom)]"
    >
      {TABS.map(([id, label, href]) => (
        <a
          key={id}
          href={href}
          data-tab={id}
          aria-current={tab === id ? "page" : undefined}
          class={`relative flex min-h-[52px] flex-col items-center justify-center gap-1 rounded-lg text-[11px] outline-none focus-visible:ring-2 focus-visible:ring-primary ${
            tab === id ? "font-semibold text-primary" : "text-base-content/70"
          }`}
        >
          <TabGlyph id={id} class="size-[22px]" />
          <span>{label}</span>
          {id === "now" ? (
            <span class="absolute top-1 left-[54%]">
              <TabBadge n={n} />
            </span>
          ) : null}
        </a>
      ))}
    </nav>
  );
}

/** The tablet chrome's icon rail: the five tabs as 52 px labelled icons, Settings at the bottom. */
function IconRail({ tab, n }: { tab: Tab; n: number }): JSX.Element {
  return (
    <nav
      data-nav="rail"
      aria-label="Main"
      class="flex w-[72px] shrink-0 flex-col items-center gap-2 border-r hairline bg-base-200 py-[18px]"
    >
      {TABS.map(([id, label, href]) => (
        <a
          key={id}
          href={href}
          data-tab={id}
          aria-label={label}
          title={label}
          aria-current={tab === id ? "page" : undefined}
          class={`relative flex size-[52px] items-center justify-center rounded-[14px] outline-none focus-visible:ring-2 focus-visible:ring-primary ${
            id === "settings" ? "mt-auto" : ""
          } ${
            tab === id
              ? "bg-base-100 text-primary"
              : "text-base-content/70 hover:bg-base-100/60 hover:text-base-content"
          }`}
        >
          <TabGlyph id={id} class="size-6" />
          {id === "now" ? (
            <span class="absolute top-1 right-1">
              <TabBadge n={n} />
            </span>
          ) : null}
        </a>
      ))}
    </nav>
  );
}

/** The Day/Week/Month range control the Radar tab carries: right of the sub-tabs on a wide pane,
 *  in the phone header's place. A range outside the three presets leaves none pressed. */
function RadarRange(): JSX.Element {
  const state = useRadarState();
  const preset = state?.range.preset ?? null;
  const choices: readonly (readonly [string, string])[] = [
    ["24h", "Day"],
    ["7d", "Week"],
    ["30d", "Month"],
  ];
  return (
    <fieldset
      aria-label="Range"
      class="flex shrink-0 gap-0.5 rounded-[10px] border border-base-content/15 bg-base-200 p-[3px]"
      data-radar-range=""
    >
      {choices.map(([key, label]) => (
        <button
          key={key}
          type="button"
          aria-pressed={preset === key ? "true" : "false"}
          data-range={key}
          class={`min-h-8 min-w-13 rounded-[7px] px-3 text-[13px] font-medium outline-none focus-visible:ring-2 focus-visible:ring-primary ${
            preset === key ? "bg-base-300 font-semibold text-base-content" : "text-base-content/70"
          }`}
          onClick={() => radarAct("range", key)}
        >
          {label}
        </button>
      ))}
    </fieldset>
  );
}

/** What the Export CSV button hands over for the view on screen: the costs tree, the tools table,
 *  or the requests table every other view reads. */
const RADAR_EXPORT: Record<RadarView, string> = {
  overview: "requests",
  costs: "attribution",
  models: "requests",
  usage: "requests",
  blocks: "requests",
  requests: "requests",
  tools: "tools",
  agents: "requests",
  routers: "requests",
  alerts: "requests",
  timeline: "requests",
};

/** Radar's sub-tab row: an underline row on the desktop bar's second line, pills elsewhere, the
 *  range right-aligned and — on the desktop — the Export CSV button beside it. The Alerts pill
 *  carries the active alert count. */
function RadarViews({ view, layout }: { view: RadarView; layout: Layout }): JSX.Element {
  const state = useRadarState();
  const alerts = state?.alerts.length ?? 0;
  return (
    <nav
      aria-label="Radar views"
      class={
        layout === "desktop"
          ? "flex min-h-[52px] shrink-0 items-center gap-0.5 border-b hairline pr-5 pl-5"
          : "flex shrink-0 items-center gap-1.5 overflow-x-auto px-4 py-2"
      }
    >
      <div
        class={
          layout === "desktop"
            ? "flex h-full min-w-0 flex-1 items-center gap-0.5 overflow-x-auto"
            : "flex min-w-0 flex-1 items-center gap-1.5 overflow-x-auto"
        }
      >
        {RADAR_VIEWS.map((v) => (
          <a
            key={v}
            href={`#/radar/${v}`}
            data-view={v}
            aria-current={v === view ? "page" : undefined}
            class={
              layout === "desktop"
                ? `flex h-full shrink-0 items-center border-b-2 px-3.5 text-sm outline-none focus-visible:ring-2 focus-visible:ring-primary ${
                    v === view
                      ? "border-primary font-semibold text-base-content"
                      : "border-transparent text-base-content/70 hover:text-base-content"
                  }`
                : `flex min-h-11 shrink-0 items-center rounded-full px-3.5 text-sm outline-none focus-visible:ring-2 focus-visible:ring-primary ${
                    v === view
                      ? "bg-primary font-semibold text-base-300"
                      : "border hairline text-base-content/80 hover:bg-base-100"
                  }`
            }
          >
            {radarLabel(v)}
            {v === "alerts" && alerts > 0 && (
              <span class="badge badge-error badge-sm ml-1.5 tnum">{alerts}</span>
            )}
          </a>
        ))}
      </div>
      <div class="ml-auto flex shrink-0 items-center gap-2.5 pl-3">
        {layout === "phone" ? null : <RadarRange />}
        {layout === "desktop" && (
          <button
            type="button"
            class="btn btn-ghost btn-sm h-[34px] border border-base-content/20"
            data-radar-export=""
            onClick={() => radarAct("export", RADAR_EXPORT[view])}
          >
            <Icon name="download" class="size-4" />
            Export CSV
          </button>
        )}
      </div>
    </nav>
  );
}

/** The channel the address names does not exist. */
export function NoChannel({ ch }: { ch: string }): JSX.Element {
  return (
    <div class="mx-auto flex max-w-lg flex-col items-center gap-2 rounded-box border border-dashed border-base-content/20 px-6 py-20 text-center text-sm muted">
      <Icon name="hash" class="size-8" />
      <h1 class="text-xl font-semibold tracking-tight text-base-content">No channel “{ch}”</h1>
      <p>
        A session's first <code>join</code> creates a channel, or you can create one on the{" "}
        <a class="link link-primary" href="#/channels">
          channels page
        </a>
        .
      </p>
    </div>
  );
}

/** A read-only command with a copy button at the end. */
function CmdField({ id, label, cmd }: { id: string; label: string; cmd: string }): JSX.Element {
  return (
    <div class="flex flex-col gap-1.5">
      <span class="text-sm font-medium" id={`${id}-l`}>
        {label}
      </span>
      {/* biome-ignore lint/a11y/useAriaPropsSupportedByRole: the cmd field's labelled box */}
      <div
        class="flex h-10 items-center gap-2.5 rounded-lg border border-base-content/15 bg-base-200 pr-1 pl-3"
        aria-labelledby={`${id}-l`}
      >
        <span class="muted">
          <Icon name="terminal" />
        </span>
        <code class="min-w-0 flex-1 truncate text-sm">{cmd}</code>
        <CopyButton text={cmd} done="" ariaLabel={`Copy ${cmd}`} />
      </div>
    </div>
  );
}

/** A copy button that flashes a check and a word for a moment when the copy landed. */
function CopyButton({
  text,
  done,
  ariaLabel,
  class: c = "btn btn-ghost btn-square btn-sm muted",
  children,
}: {
  text: string;
  done: string;
  ariaLabel?: string | undefined;
  class?: string | undefined;
  children?: ComponentChildren;
}): JSX.Element {
  const [hit, setHit] = useState(false);
  useEffect(() => {
    if (!hit) return;
    const t = setTimeout(() => setHit(false), 1600);
    return () => clearTimeout(t);
  }, [hit]);
  const copy = (): void => {
    navigator.clipboard?.writeText(text).then(
      () => setHit(true),
      () => {},
    );
  };
  return (
    <button type="button" class={c} aria-label={ariaLabel} onClick={copy}>
      {hit ? (
        <>
          <Icon name="check" />
          {done}
        </>
      ) : (
        children
      )}
    </button>
  );
}

/** The page this browser gets when its sign-in has ended: one passkey touch when the home holds
 *  a passkey, and the link commands under a smaller heading either way — /huddle:setup in a
 *  Claude session, `huddle open` in a terminal — with the server's version under it all. */
export function SignedOut(): JSX.Element {
  const { api } = useHuddle();
  const [ver, setVer] = useState("Huddle");
  const [passkeys, setPasskeys] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    document.title = "Huddle: signed out";
    fetch("/health")
      .then((r) => r.json())
      .then((h: { version?: string }) => {
        if (h.version) setVer(`Huddle v${h.version}`);
      })
      .catch(() => {});
    api
      .api("/api/login/available")
      .then((r) => {
        if ((r as { passkeys?: unknown }).passkeys === true) setPasskeys(true);
      })
      .catch(() => {});
  }, []);

  /** One touch: begin, the browser's own credential, finish, and the reload that lands the shell. */
  const signIn = async (): Promise<void> => {
    setBusy(true);
    setError("");
    try {
      const r = (await api.api("/api/login/begin", { body: {} })) as { options?: unknown };
      let cred: PublicKeyCredential;
      try {
        cred = (await navigator.credentials.get({
          publicKey: assertionFromJSON(r.options).publicKey,
        })) as PublicKeyCredential;
      } catch (e) {
        if ((e as { name?: string }).name === "NotAllowedError") {
          setError("Sign-in cancelled. Try again.");
        } else {
          setError("No passkey on this device. Save one below, or use a sign-in link.");
        }
        return;
      }
      await api.api("/api/login/finish", { body: { response: credentialToJSON(cred) } });
      location.reload();
    } catch (e) {
      setError(errText(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <main class="grid min-h-dvh place-items-center bg-base-200 p-4">
      <div class="flex w-full max-w-md flex-col gap-8">
        <div class="flex flex-col items-center gap-3 text-center">
          <Logo />
          <h1 class="text-3xl font-bold tracking-tight">Huddle</h1>
          <p class="text-[15px] muted">Sign in to see your channels and sessions</p>
        </div>
        <Panel class="w-full" label="How to sign back in">
          <div class="flex flex-col gap-5">
            <div class="alert alert-warning alert-soft text-sm" role="status">
              <span class="shrink-0">
                <Icon name="logout" />
              </span>
              <span>
                <b class="font-semibold" id="so-h">
                  Signed out.
                </b>{" "}
                <span class="muted">
                  This browser's sign-in ended: Huddle was set up again, its member was removed, the link
                  expired, or the sign-in expired after 7 days unused.
                </span>
              </span>
            </div>
            {passkeys ? (
              <>
                <button
                  type="button"
                  class="btn btn-primary h-12 w-full text-sm"
                  disabled={busy}
                  onClick={() => void signIn()}
                >
                  <Icon name="key" />
                  Sign in with a passkey
                </button>
                {error ? (
                  <p role="alert" class="m-0 text-sm text-error">
                    {error}
                  </p>
                ) : null}
              </>
            ) : (
              <p class="text-sm muted">
                Run <code>huddle open</code> once, then save a passkey: next time Touch ID signs you in.
              </p>
            )}
            <div class="flex flex-col gap-3">
              <h2 class="text-sm font-semibold">No passkey on this device?</h2>
              <p class="text-sm muted">
                Run <code>/huddle:setup</code> in any session of this huddle — or <code>huddle open</code> in
                a terminal — for a new sign-in link, then open it here.
              </p>
              <CmdField id="so-c" label="In a Claude session" cmd="/huddle:setup" />
              <CmdField id="so-t" label="Or in a terminal" cmd="huddle open" />
              <CopyButton
                text="/huddle:setup"
                done="Copied: paste it into a Claude session"
                class="btn btn-primary h-10 w-full text-sm"
              >
                <Icon name="copy" />
                Copy /huddle:setup
              </CopyButton>
            </div>
          </div>
        </Panel>
        <p class="muted text-center text-xs">{ver}</p>
      </div>
    </main>
  );
}

/** The banner a browser signed in by link shows at the top of the shell: save a passkey here and
 *  the next sign-in is Touch ID, with no terminal and no link. */
function SavePasskeyBanner({
  busy,
  error,
  onSave,
  onClose,
}: {
  busy: boolean;
  error: string;
  onSave: () => void;
  onClose: () => void;
}): JSX.Element {
  return (
    <div
      class="flex shrink-0 flex-col gap-1.5 border-b hairline bg-base-100 px-4 py-2.5 sm:px-6"
      data-banner="passkey"
    >
      <div class="flex items-center gap-3">
        <span class="shrink-0 text-primary">
          <Icon name="key" />
        </span>
        <p class="m-0 min-w-0 flex-1 text-sm muted">
          Save a passkey for this browser — next time Touch ID signs you in.
        </p>
        <button type="button" class="btn btn-primary btn-sm" disabled={busy} onClick={onSave}>
          Save passkey
        </button>
        <button
          type="button"
          class="btn btn-ghost btn-square btn-sm muted"
          aria-label="Dismiss"
          onClick={onClose}
        >
          <Icon name="x" />
        </button>
      </div>
      {error ? (
        <p role="alert" class="m-0 pl-9 text-sm text-error">
          {error}
        </p>
      ) : null}
    </div>
  );
}

/** The shell's single-key layer: the g chords open the tabs, ⌘K / Ctrl-K and / focus the jump
 *  field (the palette where it is not drawn), and c composes over the open channel. A dialog
 *  keeps the keys, and a text field is never interrupted. Renders nothing. */
function useShellKeys(d: {
  go: (href: string) => void;
  ch: string | null;
  onCompose: ComposeOpen | undefined;
  onPalette: (() => void) | undefined;
}): void {
  const gAt = useRef(0);
  // no dependency list: the shell repaints on every change, and the handler reads it fresh
  useEffect(() => {
    const jump = (): boolean => {
      const el = document.getElementById("jump");
      if (el instanceof HTMLElement) {
        el.focus();
        return true;
      }
      return false;
    };
    // oxlint-disable-next-line complexity -- one guard per key the shell owns
    const on = (e: KeyboardEvent): void => {
      if (document.querySelector("dialog[open]")) return;
      if ((e.metaKey || e.ctrlKey) && !e.altKey && e.key.toLowerCase() === "k") {
        e.preventDefault();
        if (!jump()) d.onPalette?.();
        return;
      }
      if (e.metaKey || e.ctrlKey || e.altKey) return;
      const t = e.target instanceof HTMLElement ? e.target : null;
      if (t?.closest("input, textarea, select, [contenteditable]")) return;
      if (e.key === "/") {
        e.preventDefault();
        if (!jump()) d.onPalette?.();
        return;
      }
      if (gAt.current > 0 && Date.now() - gAt.current < 1200) {
        gAt.current = 0;
        const tab = CHORD[e.key];
        if (tab) {
          e.preventDefault();
          d.go(tabHref(tab));
        }
        return;
      }
      if (e.key === "g") {
        gAt.current = Date.now();
        return;
      }
      if (e.key === "c" && d.ch) {
        e.preventDefault();
        d.onCompose?.();
      }
    };
    window.addEventListener("keydown", on);
    return () => window.removeEventListener("keydown", on);
  });
}

/** The sentence under a channel page's title: Overview speaks with the channel's own words when
 *  it has them, Settings carries the channel's facts, the rest come straight from the table. */
function introDescription(dest: Dest, state: HuddleState, ch: string | null, now: number): ComponentChildren {
  if (dest === "overview") {
    const d = state.info?.config?.description;
    if (typeof d === "string" && d) return d;
    return DEST_INTRO.overview.description;
  }
  if (dest === "settings") {
    const c = (state.info?.config ?? {}) as { created_at?: Stamp | null };
    const st = (state.info?.stats ?? {}) as { events?: number; tasks?: number; knowledge?: number };
    return (
      <>
        Channel <code>{ch}</code>, created <Time ts={c.created_at ?? null} now={now} />. {st.events ?? 0}{" "}
        events, {st.tasks ?? 0} tasks, {st.knowledge ?? 0} knowledge entries.
      </>
    );
  }
  return DEST_INTRO[dest].description;
}

/** The shell over the current hash: the chrome the viewport draws, and the panes of the tab the
 *  address names. `onCompose` opens the composer from the c key, `onPalette` the command palette
 *  from the jump field and ⌘K where it is not drawn, `onHelp` the shortcuts. */
// oxlint-disable-next-line complexity -- one branch per layout and per tab whose panes it draws
export function App({
  pages,
  onCompose,
  onPalette,
  onHelp,
  loadChannels,
}: {
  pages: Pages;
  onCompose?: ComposeOpen | undefined;
  onPalette?: (() => void) | undefined;
  onHelp?: (() => void) | undefined;
  /** Refills the channel list the switcher and the palette offer (the store's loadChannels). */
  loadChannels?: (() => void) | undefined;
}): JSX.Element {
  // the pairing link's screen owns this window until it says otherwise (the state, not the
  // pathname, drops the gate — a repaint with the session cookie in lands the shell on Now)
  const [pairing, setPairing] = useState(() => location.pathname === "/pair");
  // the token the banner minted, which runs the same pair screen in place
  const [pairToken, setPairToken] = useState<string | null>(null);
  const [bannerOff, setBannerOff] = useState(() => readPref(localStorage, "passkey-banner", false));
  const [minting, setMinting] = useState(false);
  const [mintError, setMintError] = useState("");
  const { state, registry, now, go, who, api } = useHuddle();
  const layout = useLayout();
  const [hash, setHash] = useState(location.hash);
  // the page-wide controls the open page handed up, kept with the page that named them
  const [introActs, setIntroActs] = useState<{ dest: Dest; render: () => ComponentChildren } | null>(null);

  // the shell follows the address bar
  useEffect(() => {
    const on = (): void => setHash(location.hash);
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, []);

  const route = useMemo(() => parseHash(hash, (k, fb) => readPref(localStorage, k, fb)), [hash]);

  // an alias, an old address or an unknown one rewrites the address to the canonical form
  useEffect(() => {
    if (route.replace) history.replaceState(null, "", route.replace);
  }, [route.replace]);

  // the preferences an alias carried land in this browser's settings before any view reads them
  useEffect(() => {
    for (const [k, v] of Object.entries(route.prefs)) writePref(localStorage, k, v);
  }, [route]);

  useShellKeys({ go, ch: state.ch, onCompose, onPalette });

  // the channel list feeds the switcher and the palette: one read per page load, and again
  // whenever the open channel changes (a freshly created channel joins the list this way)
  useEffect(() => {
    loadChannels?.();
  }, [loadChannels, state.ch]);

  // the needs-you count the chrome carries: what the open channel holds, plus the registry's
  // sessions that wait on the owner (the Now tab's own list)
  const n = inboxOf(state) + registry.needs();
  const chname = state.info?.config?.title || state.ch;
  useEffect(() => {
    document.title = `${n ? `(${n}) ` : ""}${
      route.tab === "channels" && state.ch ? `${chname} · ` : ""
    }Huddle`;
  }, [n, chname, state.ch, route.tab]);

  // the gate stands under every hook, so the render that leaves it keeps its hook order: the
  // pair screen needs no credential and no channel, and onPaired drops it once the finish's
  // cookie is in. A pairing the banner opened leaves the banner off for good.
  const paired = (): void => {
    setPairing(false);
    setPairToken(null);
    writePref(localStorage, "passkey-banner", true);
    setBannerOff(true);
  };
  if (pairing) return <Pair token={pairToken ?? undefined} onPaired={paired} />;

  /** Mints one pairing link without a terminal and opens the ceremony it pays for. */
  const savePasskey = async (): Promise<void> => {
    setMinting(true);
    setMintError("");
    try {
      const r = (await api.api("/api/pair/mint", { body: { action: "mint" } })) as { token?: string };
      if (!r.token) throw new Error("the server sent no pairing link");
      setPairToken(r.token);
      setPairing(true);
    } catch (e) {
      setMintError(errText(e));
    } finally {
      setMinting(false);
    }
  };
  // the banner asks only a browser whose credential is the owner's link session: a paired
  // device already has its passkey, and a member may not mint one
  const shellBanner: ComponentChildren =
    who.kind === "owner" && !bannerOff ? (
      <SavePasskeyBanner
        busy={minting}
        error={mintError}
        onSave={() => void savePasskey()}
        onClose={() => {
          writePref(localStorage, "passkey-banner", true);
          setBannerOff(true);
        }}
      />
    ) : null;

  const steps = state.board?.steps ?? [];
  const roster = state.sessions?.sessions ?? [];
  const online = roster.filter((s) => s.state !== "left").length;
  const open = steps.filter((s) => !FIN.has(s.status ?? "todo")).length;
  const counts: NavCounts = { n, sessions: online, open };
  const ch = state.ch;

  // the pages the pages registry holds sit under Channels alone
  const Page = route.tab === "channels" ? pages[route.dest] : undefined;
  const meta = DEST_INTRO[route.dest as Dest];
  const body = Page ? (
    <Page />
  ) : route.dest === "home" ? null : (
    <ComingSoon label={LABELS[route.dest as Dest]} />
  );

  // the hand pages register their page-wide controls through, tagged with the page
  const takeIntro = useCallback(
    (render: () => ComponentChildren): void => {
      setIntroActs({ dest: route.dest as Dest, render });
    },
    [route.dest],
  );
  const introActions = introActs && introActs.dest === route.dest ? introActs.render() : null;

  // each tab's panes: the list beside the detail on tablet and desktop, and the desktop's
  // context pane where a screen carries one (Now's session screen: the linked sessions and the
  // session's facts).
  const nowId = route.tab === "now" ? (route.sub[0] ?? null) : null;
  const list =
    route.tab === "channels" && ch && layout !== "phone" ? (
      <Rail onHelp={onHelp} />
    ) : route.tab === "now" && layout !== "phone" ? (
      <NowPane layout={layout} />
    ) : null;
  const context: ComponentChildren =
    route.tab === "now" && layout === "desktop" && nowId ? <NowContext /> : null;

  const detail: ComponentChildren =
    route.tab === "now" ? (
      nowId ? (
        <SessionScreen id={nowId} layout={layout} />
      ) : layout === "phone" ? (
        <div class="min-h-0 flex-1">
          <NowPane layout="phone" />
        </div>
      ) : (
        <div class="min-h-0 flex-1 overflow-y-auto px-4 py-5">
          <EmptyPane title="No session open" line="Pick a session from the list to read its stream." />
        </div>
      )
    ) : route.tab === "radar" ? (
      <div class="flex min-h-0 flex-1 flex-col">
        <RadarViews view={route.view} layout={layout} />
        <div class="min-h-0 flex-1 overflow-y-auto">
          <RadarTab view={route.view} />
        </div>
      </div>
    ) : route.tab === "kanban" ? (
      <div class="min-h-0 flex-1 overflow-y-auto">
        <KanbanTab boardKey={route.sub[0] ?? null} />
      </div>
    ) : route.tab === "settings" ? (
      <SettingsTab section={settingsSectionOf(route.sub[0])} />
    ) : (
      <div class="flex min-h-0 flex-1 flex-col">
        {ch ? <ViewTabs dest={route.dest as Dest} counts={counts} /> : null}
        <div class="min-h-0 flex-1 overflow-y-auto">
          {body !== null || route.dest !== "home" ? (
            <div class="mx-auto flex w-full max-w-[1600px] min-w-0 flex-col px-4 py-6 sm:px-6 lg:px-8">
              <PageIntro
                icon={<Icon name={meta.icon} />}
                title={meta.label}
                description={introDescription(route.dest as Dest, state, ch, now)}
                actions={introActions}
              />
              <IntroCtx.Provider value={{ take: takeIntro }}>{body}</IntroCtx.Provider>
            </div>
          ) : null}
        </div>
      </div>
    );

  const main = (
    <main id="main" tabindex={-1} class="flex min-h-0 min-w-0 flex-1 flex-col outline-none">
      {detail}
    </main>
  );

  // the machines line reads the registry; its live dot follows the stream the open tab runs on
  // (the registry's on Now, the channel's everywhere else)
  const reg = registry.getState();
  const mline = machinesLine(layout, machinesOf(reg.sessions));
  const liveOf = route.tab === "now" ? reg.live : state.live;

  // the phone: a header over the pane, the bottom nav under it. A session's screen paints its
  // own header (the back link and the name), so the shell's steps aside there.
  if (layout === "phone") {
    return (
      <div class="flex h-dvh flex-col bg-base-300">
        {shellBanner}
        {nowId ? null : (
          <header class="flex shrink-0 items-center justify-between gap-3 px-5 pt-[22px] pb-3">
            <h1 class="text-[28px] font-semibold tracking-tight">{tabLabel(route.tab)}</h1>
            {route.tab === "radar" ? <RadarRange /> : <MachinesLine live={liveOf} text={mline} />}
          </header>
        )}
        {main}
        <BottomNav tab={route.tab} n={n} />
      </div>
    );
  }

  // the tablet: the icon rail, then the list beside the detail when the tab has a list
  if (layout === "tablet") {
    return (
      <div class="flex h-dvh flex-col bg-base-300">
        {shellBanner}
        <div class="grid min-h-0 min-w-0 flex-1 grid-cols-[72px_minmax(0,1fr)]">
          <IconRail tab={route.tab} n={n} />
          <div class={`grid min-h-0 min-w-0 ${list ? "grid-cols-[360px_minmax(0,1fr)]" : "grid-cols-1"}`}>
            {list}
            {main}
          </div>
        </div>
      </div>
    );
  }

  // the desktop: the top bar over the list, the detail and the context pane
  return (
    <div class="flex h-dvh flex-col bg-base-300">
      {shellBanner}
      <div class="grid min-h-0 min-w-0 flex-1 grid-rows-[52px_minmax(0,1fr)]">
        <DesktopBar
          tab={route.tab}
          n={n}
          live={liveOf}
          machines={mline}
          onPalette={onPalette}
          onHelp={onHelp}
        />
        <div
          class={`grid min-h-0 min-w-0 ${
            list && context
              ? "grid-cols-[320px_minmax(0,1fr)_340px]"
              : list
                ? "grid-cols-[320px_minmax(0,1fr)]"
                : "grid-cols-1"
          }`}
        >
          {list}
          {main}
          {context}
        </div>
      </div>
    </div>
  );
}
