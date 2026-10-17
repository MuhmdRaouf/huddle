// router.ts — the hash routes, parsed and built: the five tabs (`#/now[/<session id>]`,
// `#/channels[/<name>[/…]]`, `#/radar/<view>`, `#/kanban[/<board key>]`, `#/settings[/<section>]`),
// the channel pages that live under Channels (`overview, today, inbox, team, work/knowledge,
// settings`), the two drawers (`?t=<task>`, `?s=<session>`), the alias redirects (bookmarks,
// notifications, links in agents' context) and the old `#/channels/…` and `#/` addresses rewritten
// into the new ones. Pure: a hash goes in, a route comes out; the shell applies what it says.

import type { IconName } from "../icons.tsx";

/** The five tabs of the shell. */
export type Tab = "now" | "channels" | "radar" | "kanban" | "settings";

/** Every page a channel serves under Channels; Home is the channels list itself. */
export type Dest = "home" | "overview" | "today" | "inbox" | "team" | "work" | "knowledge" | "settings";

/** Radar's views, in sub-tab order. */
export const RADAR_VIEWS = [
  "overview",
  "costs",
  "models",
  "usage",
  "blocks",
  "requests",
  "tools",
  "agents",
  "routers",
  "alerts",
  "timeline",
] as const;

/** One of Radar's views. */
export type RadarView = (typeof RADAR_VIEWS)[number];

/** The label a radar view carries in its sub-tab. */
const RADAR_LABEL: Record<RadarView, string> = {
  overview: "Overview",
  costs: "Costs",
  models: "Models",
  usage: "Usage",
  blocks: "Blocks",
  requests: "Requests",
  tools: "Tools",
  agents: "Agents",
  routers: "Routers",
  alerts: "Alerts",
  timeline: "Timeline",
};

/** The sub-tab label of a radar view. */
export const radarLabel = (view: RadarView): string => RADAR_LABEL[view];

/** The five tabs, in nav order: id, label, address. */
export const TABS: readonly (readonly [Tab, string, string])[] = [
  ["now", "Now", "#/now"],
  ["channels", "Channels", "#/channels"],
  ["radar", "Radar", "#/radar/overview"],
  ["kanban", "Kanban", "#/kanban"],
  ["settings", "Settings", "#/settings"],
];

/** The address a tab opens. */
export const tabHref = (tab: Tab): string => TABS.find((t) => t[0] === tab)?.[2] ?? "#/now";

/** The label a tab carries in the chrome. */
export const tabLabel = (tab: Tab): string => TABS.find((t) => t[0] === tab)?.[1] ?? "";

/** The channel nav's entries: name, label, icon and keyboard hint. */
export const DESTS: readonly (readonly [
  name: Exclude<Dest, "home" | "overview" | "today">,
  label: string,
  icon: IconName,
  key: string,
])[] = [
  ["inbox", "Inbox", "inbox", "i"],
  ["team", "Team", "users", "t"],
  ["work", "Work", "list", "w"],
  ["knowledge", "Knowledge", "book", "k"],
  ["settings", "Settings", "sliders", ""],
];

/** The pages parseChannel accepts under a channel; anything else reads as Overview. */
const KNOWN: ReadonlySet<string> = new Set<string>(["overview", "today", ...DESTS.map((d) => d[0])]);

/** Reads a per-viewer preference the aliases carry over ("wview:<ch>"); tests pass a fake. */
export type PrefReader = (key: string, fallback: string) => string;

/** One alias hit: the canonical tail under `#/channels/<ch>/…` and the per-viewer prefs it carries. */
export type Redirect = { to: string; prefs?: Record<string, string | number> };

/** What a hash says: the tab to paint, its sub path, both drawers, and the canonical address
 *  (`replace`) plus prefs to save when an alias, an old address or an unknown one was used. */
export type Route = {
  tab: Tab;
  /** The channel under `#/channels/<name>`, null everywhere else. */
  ch: string | null;
  /** Under Channels: the channel's page. Off it: the tab's own name, so key stays unique. */
  dest: Dest | Tab;
  /** The parts after the page (a work view under Work, an id under Knowledge, a section under
   *  Settings, a session id under Now, a board key under Kanban). */
  sub: string[];
  /** The radar view under `#/radar/<view>`, Overview when none was named. */
  view: RadarView;
  /** The tab, channel, page, sub path and view as one string: the shell repaints when it changes. */
  key: string;
  /** The task drawer's task, from ?t=. */
  task: string | null;
  /** The session drawer's session, from ?s=; the task drawer wins when both are present. */
  sess: string | null;
  /** The canonical hash for the address bar, or null when the hash already is canonical. */
  replace: string | null;
  /** Per-viewer preferences an alias carried ("wphase:<ch>", "wfilter:<ch>"), to save. */
  prefs: Record<string, string | number>;
};

/** Decodes a path part; a broken escape stays as written. */
const safeDec = (x: string): string => {
  try {
    return decodeURIComponent(x);
  } catch {
    return x;
  }
};

/** Encodes a path part for an address the shell writes back. */
const enc = (x: string): string => encodeURIComponent(x);

/** The path part of a hash, without a drawer query; empty reads as the channels list. */
export const pathOf = (hash: string): string => hash.split("?")[0] || "#/";

/** A channel link: #/channels/<ch><p>, without the channel's drawer. */
export const channelHref = (ch: string, p = ""): string => `#/channels/${ch}${p}`;

/** A task drawer link over the current channel view; over Work when the view is not the
 *  channel's. The drawer query rides on whatever path is open. */
export function taskHref(ch: string, p: string, id: string): string {
  return `${p.startsWith(`#/channels/${ch}/`) ? p : channelHref(ch, "/work")}?t=${encodeURIComponent(id)}`;
}

/** A session drawer link over the current channel view; over Team otherwise. */
export function sessHref(ch: string, p: string, name: string): string {
  return `${p.startsWith(`#/channels/${ch}/`) ? p : channelHref(ch, "/team")}?s=${encodeURIComponent(name)}`;
}

/** Which canonical page an alias stands for: needs→Inbox, live→Team, plan→Work's list
 *  (carrying the phase), board and graph→Work, review→Work's list filtered to notes,
 *  kb→Knowledge, repo→Work's repo views, t/<id>→the task drawer over the channel's last Work
 *  view. Anything else is not an alias. */
export function redirect(ch: string, p: readonly string[], readPref: PrefReader): Redirect | null {
  const [v, ...rest] = p;
  const a = rest.join("/");
  const work = readPref(`wview:${ch}`, "list");
  if (v === "needs") return { to: "inbox" };
  if (v === "live") return { to: "team" };
  if (v === "plan") return planRedirect(ch, a);
  if (v === "board" || v === "graph") return { to: `work/${v}` };
  if (v === "review") return { to: "work/list", prefs: { [`wfilter:${ch}`]: "notes" } };
  if (v === "kb") return kbRedirect(a);
  if (v === "repo") return repoRedirect(a);
  if (v === "t") return taskRedirect(a, work);
  return null;
}

/** plan → Work's list, carrying the phase the hash named. */
function planRedirect(ch: string, a: string): Redirect {
  const m = /^p\/(-?\d+)$/.exec(a);
  return m ? { to: "work/list", prefs: { [`wphase:${ch}`]: Number(m[1]) } } : { to: "work/list" };
}

/** kb[/id] → Knowledge, optionally onto one entry. */
function kbRedirect(a: string): Redirect {
  return { to: `knowledge${a ? `/${a}` : ""}` };
}

/** repo[/view] → Work's repo views. */
function repoRedirect(a: string): Redirect {
  return { to: `work/repo${a ? `/${a}` : ""}` };
}

/** t/<id> → the task drawer over the channel's last Work view; no id, no alias. */
function taskRedirect(a: string, work: string): Redirect | null {
  return a ? { to: `work/${work}?t=${encodeURIComponent(a)}` } : null;
}

/** The drawers a hash carries; the shell lets a task drawer win over a session drawer. */
const drawers = (q: URLSearchParams): { task: string | null; sess: string | null } => ({
  task: q.get("t"),
  sess: q.get("s"),
});

/** One route under Channels: the channel's page (unknown ones read as Overview), its sub path,
 *  both drawers, and the canonical address to rewrite to when the hash was not canonical. */
function parseChannel(
  ch: string,
  parts: string[],
  qs: string | undefined,
  q: URLSearchParams,
  readPref: PrefReader,
): Route {
  const r = redirect(ch, parts.slice(2), readPref);
  if (r) {
    const canonical = `#/channels/${ch}/${r.to}`;
    const next = parseHash(canonical, readPref);
    return { ...next, replace: canonical, prefs: { ...r.prefs, ...next.prefs } };
  }
  const raw = parts[2];
  const dest: Dest = raw !== undefined && KNOWN.has(raw) ? (raw as Dest) : "overview";
  const sub = parts.slice(3);
  return {
    tab: "channels",
    ch,
    dest,
    sub,
    view: "overview",
    key: `channels/${ch}/${dest}/${sub.join("/")}`,
    ...drawers(q),
    replace: dest !== raw ? `#/channels/${enc(ch)}/${dest}${qs ? `?${qs}` : ""}` : null,
    prefs: {},
  };
}

/** The channels route an old `#/channels/<ch>/…` address names, with the address to rewrite it to. */
function parseLegacy(
  ch: string,
  parts: string[],
  qs: string | undefined,
  q: URLSearchParams,
  readPref: PrefReader,
): Route {
  const r = parseChannel(ch, ["", "", ...parts.slice(2)], qs, q, readPref);
  const tail =
    r.dest !== "home" ? `/${enc(r.dest)}${r.sub.length ? `/${r.sub.map(enc).join("/")}` : ""}` : "";
  return { ...r, replace: r.replace ?? `#/channels/${enc(ch)}${tail}${qs ? `?${qs}` : ""}` };
}

/** Now's, Kanban's and Settings' own route: the tab, its optional first segment (a session id,
 *  a board key, a section), both drawers. Both addresses are canonical as written. */
function parsePlainTab(tab: "now" | "settings" | "kanban", parts: string[], q: URLSearchParams): Route {
  const sub = parts.slice(1);
  return {
    tab,
    ch: null,
    dest: tab,
    sub,
    view: "overview",
    key: `${tab}/${sub.join("/")}`,
    ...drawers(q),
    replace: null,
    prefs: {},
  };
}

/** Radar's own route: the named view, or Overview with a rewrite when none or an unknown one
 *  was named. */
function parseRadar(parts: string[], q: URLSearchParams): Route {
  const raw = parts[1];
  const known = raw !== undefined && (RADAR_VIEWS as readonly string[]).includes(raw);
  const view: RadarView = known ? (raw as RadarView) : "overview";
  return {
    tab: "radar",
    ch: null,
    dest: "radar",
    sub: [view],
    view,
    key: `radar/${view}`,
    ...drawers(q),
    replace: raw !== view ? "#/radar/overview" : null,
    prefs: {},
  };
}

/** Parses a hash into a route: the five tabs' own addresses, a channel's pages under Channels,
 *  the aliases through their canonical forms, the old addresses rewritten, and anything unknown
 *  as Now with the address to rewrite. */
export function parseHash(hash: string, readPref: PrefReader): Route {
  const [hp = "", qs] = hash.replace(/^#\/?/, "").split("?");
  const q = new URLSearchParams(qs ?? "");
  const parts = hp.split("/").filter(Boolean).map(safeDec);
  const head = parts[0];

  if (head === "now") return parsePlainTab("now", parts, q);

  if (head === "settings") return parsePlainTab("settings", parts, q);

  if (head === "kanban") return parsePlainTab("kanban", parts, q);

  if (head === "radar") return parseRadar(parts, q);

  if (head === "channels") {
    if (parts[1] === undefined) {
      return {
        tab: "channels",
        ch: null,
        dest: "home",
        sub: [],
        view: "overview",
        key: "channels/home/",
        ...drawers(q),
        replace: null,
        prefs: {},
      };
    }
    return parseChannel(parts[1], parts, qs, q, readPref);
  }

  if (head === "c" && parts[1] !== undefined) return parseLegacy(parts[1], parts, qs, q, readPref);

  // the old home, and anything else the shell does not know: the channels list and Now
  if (head === undefined) return { ...parseHash("#/channels", readPref), replace: "#/channels" };
  return { ...parseHash("#/now", readPref), replace: "#/now" };
}
