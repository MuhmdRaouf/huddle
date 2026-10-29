// App.tsx — the shell: one component tree that draws its chrome per viewport (a header, the pane
// and the five tabs in a bar pinned to the bottom edge under 640 px; a 72 px icon rail with a list
// and a detail pane to 1023 px; a left sidebar beside a 52 px top bar over list, detail and
// context panes from 1024 px) inside a frame as tall as the viewport, over the tab the address names — Sessions, Channels, Radar, Kanban or Settings. Every later tab fills its own panes; today's
// channel pages stay reachable under Channels, with their destination tabs, their intro and the
// session rail beside them. The landing and no-channel pages stand alone.

import { Badge } from "@huddle/ui/badge.tsx";
import { PageIntro } from "@huddle/ui/page.tsx";
import type { Stamp } from "@huddle/ui/time.ts";
import type { ComponentChildren, ComponentType, JSX } from "preact";
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "preact/hooks";
import type { ComposeOptions } from "../compose/drafts.ts";
import { useHealth } from "../health.ts";
import { Icon, type IconName } from "../icons.tsx";
import { Time } from "../kit.tsx";
import { KanbanTab } from "../kanban/KanbanTab.tsx";
import { markPasskeyHere } from "../passkey.ts";
import { PHONE_VIEWS } from "../radar/app/nav.ts";
import { RadarTab } from "../radar/app/RadarTab.tsx";
import { radarAct, useRadarState } from "../radar/mount.tsx";
import { machinesLine, machinesOf, shownState } from "../registry.ts";
import { FIN } from "../status.ts";
import { readPref, writePref } from "../storage.ts";
import type { HuddleState } from "../store.ts";
import { useHuddle } from "./context.tsx";
import { IntroCtx } from "./intro.tsx";
import { type Layout, useLayout } from "./layout.ts";
import { BottomNav, IconRail, type NeedsBadge, needsBadge, SideNav, TabBadge } from "./Nav.tsx";
import { NowContext, SessionScreen } from "./Now.tsx";
import { Pair } from "./Pair.tsx";
import { deskRows, NoSessionsWelcome, SessionsPane } from "./Sessions.tsx";
import { Rail } from "./Rail.tsx";
import {
  channelHref,
  type Dest,
  parseHash,
  RADAR_VIEWS,
  type RadarView,
  radarLabel,
  type Tab,
  tabHref,
  tabLabel,
} from "./router.ts";
import { SettingsHead, SettingsTab, settingsSectionOf } from "./SettingsTab.tsx";
import { PasskeyOffer } from "./Totp.tsx";
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
  n: "sessions",
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

/** The counts the channel tabs read: the needs-you badge, the online sessions, the open tasks. */
export type NavCounts = { needs: NeedsBadge; sessions: number; open: number };

/** The count a channel tab carries: the Inbox's needs-you by the one badge rule the Sessions tab
 *  follows, the Team's online sessions and Work's open tasks in neutral; the pages without a
 *  count and a zero draw nothing. */
function tabBadge(dest: Dest, c: NavCounts): JSX.Element | null {
  if (dest === "inbox") return <TabBadge needs={c.needs} />;
  const n = dest === "team" ? c.sessions : dest === "work" ? c.open : 0;
  return n > 0 ? <Badge>{n}</Badge> : null;
}

/** One channel tab: its icon, its label and the counts that follow it; its tablist's ui-tabs
 *  paints it and marks the selected one. */
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
  const ref = useRef<HTMLButtonElement>(null);
  // the phone's one row scrolls sideways: the open page's tab comes into view
  useEffect(() => {
    if (active) ref.current?.scrollIntoView({ inline: "nearest", block: "nearest" });
  }, [active]);
  return (
    <button
      ref={ref}
      type="button"
      role="tab"
      data-dest={dest}
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

/** The tab groups over a channel's pages: three ui-tabs strips with a gap. Under 640 px they sit
 *  in one row that scrolls sideways with no scrollbar; wider, they wrap into rows. The ids are the
 *  pages, the address moves by hash. */
function ViewTabs({ dest, counts }: { dest: Dest; counts: NavCounts }): JSX.Element {
  return (
    <div
      class="flex w-full flex-wrap gap-3 px-4 pt-4 max-sm:flex-nowrap max-sm:pt-2 max-sm:overflow-x-auto max-sm:[scrollbar-width:none] max-sm:[&::-webkit-scrollbar]:hidden sm:px-6"
      data-view-tabs=""
    >
      {NAV_GROUPS.map((group) => (
        <div key={group.label} role="tablist" aria-label={group.label} class="ui-tabs max-sm:shrink-0">
          {group.tabs.map((k) => (
            <ViewTab key={k} dest={k} counts={counts} active={dest === k} />
          ))}
        </div>
      ))}
    </div>
  );
}

/** The phone's header over a channel: the back link to the channels list above #channel. */
function ChannelHead({ ch }: { ch: string }): JSX.Element {
  return (
    <div class="flex min-w-0 flex-col">
      <a
        href="#/channels"
        aria-label="Back to Channels"
        class="-ml-2 flex min-h-[var(--ui-tap)] min-w-[var(--ui-tap)] items-center gap-1 self-start rounded-[var(--ui-radius)] pr-2 text-sm font-semibold text-[var(--ui-accent)] outline-none focus-visible:ring-2 focus-visible:ring-[var(--ui-accent)]"
      >
        <Icon name="left" class="size-5" />
        Channels
      </a>
      <h1 class="truncate text-[28px] font-semibold tracking-tight">#{ch}</h1>
    </div>
  );
}

/** The dashed frame a placeholder stands in, on the tokens. */
const PLACEHOLDER =
  "mx-auto flex flex-col items-center gap-2 rounded-[var(--ui-radius-lg)] border border-dashed border-[var(--ui-line-strong)] px-6 text-center text-sm text-[var(--ui-muted)]";

/** The placeholder for a channel page no page is registered for yet. */
export function ComingSoon({ label }: { label: string }): JSX.Element {
  return (
    <div class="mx-auto max-w-3xl py-6">
      <div class={`${PLACEHOLDER} py-10`}>
        <b class="text-base font-semibold text-[var(--ui-text)]">{label}</b>
        <p>Coming soon.</p>
      </div>
    </div>
  );
}

/** The Hour/Day/Week/Month range control the Radar tab carries: in the bar over the view beside
 *  the sidebar, in the phone header's place. Hour is the default range, so it is pressed on first load;
 *  a custom range leaves none pressed. */
function RadarRange(): JSX.Element {
  const state = useRadarState();
  const preset = state?.range.preset ?? null;
  const choices: readonly (readonly [string, string])[] = [
    ["1h", "Hour"],
    ["24h", "Day"],
    ["7d", "Week"],
    ["30d", "Month"],
  ];
  return (
    <fieldset
      aria-label="Range"
      class="flex shrink-0 gap-0.5 rounded-[var(--ui-radius)] border border-[var(--ui-line)] bg-[var(--ui-surface)] p-[3px]"
      data-radar-range=""
    >
      {choices.map(([key, label]) => (
        <button
          key={key}
          type="button"
          aria-pressed={preset === key ? "true" : "false"}
          data-range={key}
          class={`min-h-8 min-w-13 rounded-[7px] max-sm:min-h-[var(--ui-tap)] px-3 text-[13px] font-medium outline-none focus-visible:ring-2 focus-visible:ring-[var(--ui-accent)] ${
            preset === key
              ? "bg-[var(--ui-surface-2)] font-semibold text-[var(--ui-text)]"
              : "text-[var(--ui-muted)]"
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

/** One view's entry in Radar's nav: its label, and the active alert count on Alerts. */
function RadarEntry({ view, alerts }: { view: RadarView; alerts: number }): JSX.Element {
  return (
    <>
      {radarLabel(view)}
      {view === "alerts" && alerts > 0 && (
        <span class="ml-1.5">
          <Badge tone="fail">{alerts}</Badge>
        </span>
      )}
    </>
  );
}

/** Radar's view navigation. The phone's nav is one row of its four views; a view the Overview's
 *  More list opens shows a back link to the Overview in that row's place. The tablet and the
 *  desktop list every view in a sidebar. */
function RadarViews({ view, layout }: { view: RadarView; layout: Layout }): JSX.Element {
  const state = useRadarState();
  const alerts = state?.alerts.length ?? 0;
  const focus = "outline-none focus-visible:ring-2 focus-visible:ring-[var(--ui-accent)]";
  if (layout === "phone" && !PHONE_VIEWS.includes(view)) {
    return (
      <div data-radar-views="" class="flex shrink-0 items-center px-4 py-2">
        <a
          href="#/radar/overview"
          aria-label="Back to Overview"
          class={`-ml-2 flex min-h-[var(--ui-tap)] min-w-[var(--ui-tap)] items-center gap-1 rounded-[var(--ui-radius)] pr-2 text-sm font-semibold text-[var(--ui-accent)] ${focus}`}
        >
          <Icon name="left" class="size-5" />
          Overview
        </a>
      </div>
    );
  }
  if (layout === "phone") {
    return (
      <nav aria-label="Radar views" data-radar-views="" class="grid shrink-0 grid-cols-4 gap-1.5 px-4 py-2">
        {PHONE_VIEWS.map((v) => (
          <a
            key={v}
            href={`#/radar/${v}`}
            data-view={v}
            aria-current={v === view ? "page" : undefined}
            class={`flex min-h-[var(--ui-tap)] min-w-0 items-center justify-center rounded-[var(--ui-radius-full)] px-2 text-sm ${focus} ${
              v === view
                ? "bg-[var(--ui-accent)] font-semibold text-[var(--ui-on-accent)]"
                : "border border-[var(--ui-line)] text-[var(--ui-text)] hover:bg-[var(--ui-surface-2)]"
            }`}
          >
            <RadarEntry view={v} alerts={alerts} />
          </a>
        ))}
      </nav>
    );
  }
  return (
    <nav
      aria-label="Radar views"
      data-radar-views=""
      class="flex w-56 shrink-0 flex-col gap-0.5 overflow-y-auto border-r border-[var(--ui-line)] p-3"
    >
      {RADAR_VIEWS.map((v) => (
        <a
          key={v}
          href={`#/radar/${v}`}
          data-view={v}
          aria-current={v === view ? "page" : undefined}
          class={`flex min-h-[var(--ui-tap)] shrink-0 items-center rounded-[var(--ui-radius)] px-3 text-sm ${focus} ${
            v === view
              ? "bg-[var(--ui-surface-2)] font-semibold text-[var(--ui-text)]"
              : "text-[var(--ui-muted)] hover:bg-[var(--ui-surface-2)] hover:text-[var(--ui-text)]"
          }`}
        >
          <RadarEntry view={v} alerts={alerts} />
        </a>
      ))}
    </nav>
  );
}

/** The bar over a Radar view beside the sidebar: the range, and on the desktop the Export CSV
 *  button beside it. The phone carries the range in its header. */
function RadarBar({ view, layout }: { view: RadarView; layout: Layout }): JSX.Element {
  return (
    <div class="flex min-h-[52px] shrink-0 items-center justify-end gap-2.5 border-b border-[var(--ui-line)] px-5">
      <RadarRange />
      {layout === "desktop" && (
        <button
          type="button"
          class="ui-btn ui-btn-secondary"
          data-radar-export=""
          onClick={() => radarAct("export", RADAR_EXPORT[view])}
        >
          <Icon name="download" class="size-4" />
          Export CSV
        </button>
      )}
    </div>
  );
}

/** The channel the address names does not exist. */
export function NoChannel({ ch }: { ch: string }): JSX.Element {
  return (
    <div class={`${PLACEHOLDER} max-w-lg py-20`}>
      <Icon name="hash" class="size-8" />
      <h1 class="text-xl font-semibold tracking-tight text-[var(--ui-text)]">No channel “{ch}”</h1>
      <p>
        A session's first <code>join</code> creates a channel, or you can create one on the{" "}
        <a class="text-[var(--ui-accent)] underline" href="#/channels">
          channels page
        </a>
        .
      </p>
    </div>
  );
}

/** How long a "Not now" holds the offer away: thirty days, the card's own number. */
const passkeyAskEveryMS = 30 * 24 * 60 * 60 * 1000;

/** The offer a code sign-in earns once: this browser's own lock as a passkey, asked when the
 *  server knows its device row holds no passkey and the platform can make one, right after the
 *  sign-in or after the thirty days a "Not now" bought. Renders nothing while none of that
 *  holds — the code keeps working as the way in whatever the answer was. */
function PasskeyAsk(): JSX.Element | null {
  const who = useHuddle().who;
  const [platform, setPlatform] = useState(false);
  const [dismissed, setDismissed] = useState(false);
  useEffect(() => {
    const pk = window.PublicKeyCredential;
    if (typeof pk?.isUserVerifyingPlatformAuthenticatorAvailable !== "function") return;
    pk.isUserVerifyingPlatformAuthenticatorAvailable()
      .then((yes) => setPlatform(yes))
      .catch(() => {});
  }, []);
  if (who.kind !== "device" || who.devicePasskey || !platform || dismissed) return null;
  let due = false;
  try {
    if (localStorage.getItem("huddle:totp-signed-in") !== null) due = true;
    else due = Date.now() - readPref(localStorage, "passkey-ask", 0) > passkeyAskEveryMS;
  } catch {
    due = false;
  }
  if (!due) return null;
  /** Either answer buys the next thirty days and clears the one-shot marker. */
  const settled = (): void => {
    try {
      localStorage.removeItem("huddle:totp-signed-in");
    } catch {
      // site data is blocked: the answer holds for this shell alone
    }
    writePref(localStorage, "passkey-ask", Date.now());
    setDismissed(true);
  };
  return (
    <PasskeyOffer
      open
      onClose={settled}
      onDone={() => {
        who.devicePasskey = true;
        markPasskeyHere();
        settled();
      }}
    />
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

/** Whether any dialog is open, the `<dialog open>` query watched live: the phone's tab bar steps
 *  aside while a sheet holds the screen. A dialog's `open` attribute is set after it mounts and
 *  dialogs unmount whole, so both the attribute and the tree are watched. */
function useDialogOpen(): boolean {
  const [open, setOpen] = useState(() => document.querySelector("dialog[open]") !== null);
  useEffect(() => {
    const sync = (): void => setOpen(document.querySelector("dialog[open]") !== null);
    sync();
    const mo = new MutationObserver(sync);
    mo.observe(document.documentElement, { subtree: true, childList: true, attributeFilter: ["open"] });
    return () => mo.disconnect();
  }, []);
  return open;
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
  // pathname, drops the gate — a repaint with the session cookie in lands the shell on the sessions tab)
  const [pairing, setPairing] = useState(() => location.pathname === "/pair");
  // the token the checklist minted, which runs the same pair screen in place
  const [pairToken, setPairToken] = useState<string | null>(null);
  const { state, registry, now, go, who } = useHuddle();
  const layout = useLayout();
  const [hash, setHash] = useState(location.hash);
  // the page-wide controls the open page handed up, kept with the page that named them
  const [introActs, setIntroActs] = useState<{
    dest: Dest;
    render: () => ComponentChildren;
  } | null>(null);

  // the shell follows the address bar. A remount (the first-run gate closing) re-attaches the
  // listener in an effect, and a move landing inside that window would be missed for good, so
  // every render reconciles the state with the address first.
  if (hash !== location.hash) setHash(location.hash);
  useEffect(() => {
    const on = (): void => setHash(location.hash);
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, []);

  const route = useMemo(() => parseHash(hash, (k, fb) => readPref(localStorage, k, fb)), [hash]);

  // an alias, an old address or an unknown one rewrites the address to the canonical form — but
  // only while the address still names the route this render parsed: a move that lands between
  // the render and this effect owns the address, and rewriting it would pull the shell off the
  // page it just opened
  useEffect(() => {
    if (!route.replace) return;
    const now = parseHash(location.hash, (k, fb) => readPref(localStorage, k, fb));
    if (now.key !== route.key) return;
    history.replaceState(null, "", route.replace);
  }, [route.replace, route.key]);

  // a session is always open on the desk: with no session picked, the row the list would draw
  // first (needs you, then working, then idle) opens by replacing the address, so Back leaves
  // Sessions instead of bouncing back to the bare list. The registry's answer can land after the
  // first paint, so the effect follows its two lists; the guard keeps a move that lands between
  // the render and this effect in charge of the address.
  const deskRowsKey = registry.getState().sessions;
  const deskNeedsKey = registry.getState().needs;
  useLayoutEffect(() => {
    if (route.tab !== "sessions" || route.sub[0] || layout !== "desktop") return;
    const st = registry.getState();
    const first = deskRows((st.sessions ?? []).map((s) => ({ ...s, state: shownState(s, st.needs) })))[0];
    if (!first) return;
    const now = parseHash(location.hash, (k, fb) => readPref(localStorage, k, fb));
    if (now.key !== route.key) return;
    const href = `#/sessions/${first.session_id}`;
    history.replaceState(null, "", href);
    setHash(href);
    // a replaced address fires no hashchange, so the page-level move that names the open session
    // never runs: the store learns the pick here and loads the detail it needs
    registry.setOpenId(first.session_id);
  }, [route.tab, route.key, route.sub, layout, registry, deskRowsKey, deskNeedsKey]);

  // the preferences an alias carried land in this browser's settings before any view reads them
  useEffect(() => {
    for (const [k, v] of Object.entries(route.prefs)) writePref(localStorage, k, v);
  }, [route]);

  useShellKeys({ go, ch: state.ch, onCompose, onPalette });
  const dialogOpen = useDialogOpen();

  // the channel list feeds the switcher and the palette: one read per page load, and again
  // whenever the open channel changes (a freshly created channel joins the list this way). The
  // read is keyed on the channel alone: the caller hands a fresh function on every paint and the
  // list's answer repaints, so keying on the function would read in a loop
  const loadChannelsRef = useRef(loadChannels);
  loadChannelsRef.current = loadChannels;
  useEffect(() => {
    loadChannelsRef.current?.();
  }, [state.ch]);

  // the needs-you count the chrome carries: what the open channel holds, plus the registry's
  // sessions that wait on the owner (the sessions tab's own list)
  const needs = needsBadge({
    inbox: inboxOf(state),
    sessions: registry.needs(),
    items: registry.getState().needs,
  });
  // whether every part of the hub is well; a failing one paints the red dot in the chrome
  const healthy = useHealth().healthy;
  const n = needs.n;
  const chname = state.info?.config?.title || state.ch;
  useEffect(() => {
    document.title = `${n ? `(${n}) ` : ""}${
      route.tab === "channels" && state.ch ? `${chname} · ` : ""
    }Huddle`;
  }, [n, chname, state.ch, route.tab]);

  // the gate stands under every hook, so the render that leaves it keeps its hook order: the
  // pair screen needs no credential and no channel, and onPaired drops it once the finish's
  // cookie is in. A pairing the checklist's row opened leaves the row off for good.
  const paired = (): void => {
    setPairing(false);
    setPairToken(null);
    writePref(localStorage, "passkey-banner", true);
    // this browser now holds a passkey: the checklist's row stands down and the landing page's
    // next visit leads with it
    who.devicePasskey = true;
    markPasskeyHere();
  };
  if (pairing) return <Pair token={pairToken ?? undefined} onPaired={paired} />;

  const steps = state.board?.steps ?? [];
  const roster = state.sessions?.sessions ?? [];
  const online = roster.filter((s) => s.state !== "left").length;
  const open = steps.filter((s) => !FIN.has(s.status ?? "todo")).length;
  const counts: NavCounts = { needs, sessions: online, open };
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
  // context pane where a screen carries one (the sessions tab's session screen: the linked
  // sessions and the session's facts). The sessions list carries the set-up row; the checklist
  // opens in its sheet.
  const nowId = route.tab === "sessions" ? (route.sub[0] ?? null) : null;
  const reg = registry.getState();
  // the registry read answered and holds nothing: no session ever reported, so the main pane
  // leads with the welcome (the list beside it drops its copy of the card)
  const noSessions = reg.sessions !== null && reg.sessions.length === 0;
  const onPairToken = (token: string): void => {
    setPairToken(token);
    setPairing(true);
  };
  const list =
    route.tab === "channels" && ch && layout !== "phone" ? (
      <Rail onHelp={onHelp} />
    ) : route.tab === "sessions" && layout !== "phone" ? (
      <SessionsPane layout={layout} openId={nowId ?? undefined} onPairToken={onPairToken} />
    ) : null;
  const context: ComponentChildren =
    route.tab === "sessions" && layout === "desktop" && nowId ? <NowContext /> : null;

  const detail: ComponentChildren =
    route.tab === "sessions" ? (
      nowId ? (
        <SessionScreen id={nowId} layout={layout} />
      ) : layout === "phone" ? (
        <div class="flex min-h-0 flex-1 flex-col">
          <SessionsPane layout="phone" onPairToken={onPairToken} />
        </div>
      ) : noSessions ? (
        <div class="min-h-0 flex-1 overflow-y-auto px-4 py-5">
          <div class="mx-auto max-w-lg">
            <NoSessionsWelcome />
          </div>
        </div>
      ) : (
        // the tablet's quiet middle until a pick: the desk has already opened the list's first row
        <div class="min-h-0 flex-1" />
      )
    ) : route.tab === "radar" ? (
      <div class={`flex min-h-0 flex-1 ${layout === "phone" ? "flex-col" : ""}`}>
        <RadarViews view={route.view} layout={layout} />
        <div class="flex min-h-0 min-w-0 flex-1 flex-col">
          {layout === "phone" ? null : <RadarBar view={route.view} layout={layout} />}
          {/* a view with no control of its own still scrolls by keyboard */}
          {/* oxlint-disable-next-line jsx-a11y/no-noninteractive-tabindex */}
          <div class="min-h-0 flex-1 overflow-y-auto" tabIndex={0}>
            <RadarTab view={route.view} />
          </div>
        </div>
      </div>
    ) : route.tab === "kanban" ? (
      <div class="flex min-h-0 flex-1 flex-col overflow-y-auto">
        <KanbanTab boardKey={route.sub[0] ?? null} feature={route.sub[1] ?? null} />
      </div>
    ) : route.tab === "settings" ? (
      <SettingsTab section={route.sub[0] ? settingsSectionOf(route.sub[0]) : null} />
    ) : (
      <div class="flex min-h-0 flex-1 flex-col">
        {ch ? <ViewTabs dest={route.dest as Dest} counts={counts} /> : null}
        <div class="min-h-0 flex-1 overflow-y-auto">
          {body !== null || route.dest !== "home" ? (
            <div class="mx-auto flex w-full max-w-[1600px] min-w-0 flex-col px-4 py-6 sm:px-6 lg:px-8">
              <PageIntro
                icon={<Icon name={meta.icon} />}
                title={meta.label}
                bare={layout === "phone"}
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
    <main id="main" tabindex={-1} class="ui-shell-main bg-[var(--ui-bg)] outline-none">
      {detail}
    </main>
  );

  // the machines line reads the registry; its live dot follows the stream the open tab runs on
  // (the registry's on the sessions tab, the channel's everywhere else)
  const mline = machinesLine(layout, machinesOf(reg.sessions));
  const liveOf = route.tab === "sessions" ? reg.live : state.live;

  // the phone: a header over the pane, the tab bar pinned under it. A session's screen paints its
  // own header (the back link and the name), so the shell's steps aside there.
  const passkeyAsk = <PasskeyAsk />;
  if (layout === "phone") {
    return (
      <div class="ui-shell ui-shell-phone">
        {passkeyAsk}
        {nowId ? null : (
          <header class="flex shrink-0 items-center justify-between gap-3 px-5 pt-[22px] pb-3">
            {route.tab === "channels" && route.ch ? (
              <ChannelHead ch={route.ch} />
            ) : route.tab === "settings" && route.sub[0] ? (
              <SettingsHead section={settingsSectionOf(route.sub[0])} />
            ) : (
              <h1 class="text-[28px] font-semibold tracking-tight">{tabLabel(route.tab)}</h1>
            )}
            {route.tab === "radar" ? <RadarRange /> : <MachinesLine live={liveOf} text={mline} />}
          </header>
        )}
        {main}
        {dialogOpen ? null : <BottomNav tab={route.tab} needs={needs} healthy={healthy} />}
      </div>
    );
  }

  // the tablet: the icon rail, then the list beside the detail when the tab has a list
  if (layout === "tablet") {
    return (
      <div class="ui-shell">
        {passkeyAsk}
        <div class="grid min-h-0 min-w-0 flex-1 grid-cols-[72px_minmax(0,1fr)]">
          <IconRail tab={route.tab} needs={needs} healthy={healthy} />
          <div class={`grid min-h-0 min-w-0 ${list ? "grid-cols-[360px_minmax(0,1fr)]" : "grid-cols-1"}`}>
            {list}
            {main}
          </div>
        </div>
      </div>
    );
  }

  // the desktop: the sidebar, then the top bar over the list, the detail and the context pane
  return (
    <div class="ui-shell">
      {passkeyAsk}
      <div class="flex min-h-0 min-w-0 flex-1">
        <SideNav tab={route.tab} needs={needs} healthy={healthy} />
        <div class="grid min-h-0 min-w-0 flex-1 grid-rows-[52px_minmax(0,1fr)]">
          <DesktopBar live={liveOf} machines={mline} onPalette={onPalette} onHelp={onHelp} />
          <div
            class={`grid min-h-0 min-w-0 ${
              list && context
                ? "grid-cols-[320px_minmax(0,1fr)_300px]"
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
    </div>
  );
}
