// registry.ts — every reported session on every machine (the registry the report hook fills),
// as the Now list and the Session screen read it: the rows GET /api/registry/sessions answers,
// the needs-you items GET /api/needs answers, one session's detail with the events that led to
// it, and the all-channels live stream (/api/registry/live) whose registry.session and
// needs.item rows move both lists without a reload. No DOM here: the API client and the stream
// constructor are injected, so tests drive every branch.
import type { Api } from "./api.ts";
import type { EventSourceFactory, EventSourceLike } from "./live.ts";
import type { LiveStatus } from "./store.ts";

/** One report as the registry stored it (SPEC 5.1): the session's fields as of one event. */
export type RegistryReport = {
  session_id: string;
  machine?: string;
  project?: string;
  branch?: string;
  title?: string;
  task?: string;
  model?: string;
  context_percent?: number;
  cwd?: string;
  pid?: number;
  channel?: string;
  last_event: string;
  last_event_at: string;
};

/** One registry row: the report's fields with the state computed when it was read (SPEC 5.2),
 *  and the route a message to this session would take (SPEC 6.4). */
export type RegistrySession = RegistryReport & { state: string; delivery_route?: string };

/** One stored event of a session's list: its number and the report as it came. */
export type RegistryEvent = { seq: number; report: RegistryReport };

/** One needs-you item (SPEC 6.1): what GET /api/needs answers and a needs.item event carries. */
export type NeedItem = {
  id: number;
  session: string;
  kind: string;
  summary: string;
  created_at: string;
  state: string;
  snoozed_until?: string;
  tool?: string;
  input?: string;
  truncated?: boolean;
  decision?: string;
  message?: string;
};

/** GET /api/registry/sessions/<id>: the row plus the events that led to it. */
export type RegistryDetail = RegistrySession & { events: RegistryEvent[] };

/** The list's five groups, in the order the Now list shows them. */
export type RegistryGroups = {
  needsYou: RegistrySession[];
  working: RegistrySession[];
  idle: RegistrySession[];
  stale: RegistrySession[];
  ended: RegistrySession[];
};

/** What a group-by toggle holds: the rows group by machine or by project. */
export type GroupMode = "machine" | "project";

/** The word each state's mark carries: shape plus word, never colour alone (SPEC 9). */
export function stateWord(state: string): string {
  return state === "needs-you" ? "needs you" : state;
}

/** Splits the rows into the Now list's groups, keeping the read's newest-activity order. */
export function groupSessions(sessions: RegistrySession[] | null): RegistryGroups {
  const g: RegistryGroups = { needsYou: [], working: [], idle: [], stale: [], ended: [] };
  for (const s of sessions ?? []) {
    if (s.state === "needs-you") g.needsYou.push(s);
    else if (s.state === "working") g.working.push(s);
    else if (s.state === "idle") g.idle.push(s);
    else if (s.state === "stale") g.stale.push(s);
    else if (s.state === "ended") g.ended.push(s);
  }
  return g;
}

/** How many sessions need the owner: the count the Now tab's badge carries. */
export function needsYouCount(sessions: RegistrySession[] | null): number {
  return (sessions ?? []).filter((s) => s.state === "needs-you").length;
}

/** The machines the reported sessions run on, each once, sorted. */
export function machinesOf(sessions: RegistrySession[] | null): string[] {
  const names = new Set<string>();
  for (const s of sessions ?? []) if (s.machine) names.add(s.machine);
  return [...names].sort();
}

/** The machines line's text: the count on a phone, the names beside each other elsewhere. */
export function machinesLine(layout: "phone" | "tablet" | "desktop", names: string[]): string {
  if (names.length === 0) return "This machine";
  return layout === "phone" ? `${names.length} machines` : names.join(" · ");
}

/** A project path as the list shows it: its last segment (a bare name stays as it is). */
export function projectName(project: string | undefined): string {
  if (!project) return "";
  const cut = project.split("/").filter(Boolean);
  return cut[cut.length - 1] ?? project;
}

/** A session's display name: its title (the first prompt), else the head of its id. */
export function sessionName(row: RegistrySession): string {
  return row.title || row.session_id.slice(0, 8);
}

/** An event's age the way the rows carry it: seconds to days, "" when unknown. */
export function ageText(ts: string | undefined, now: number): string {
  const ms = ts ? Date.parse(ts) : Number.NaN;
  if (!Number.isFinite(ms)) return "";
  const s = Math.max(0, (now - ms) / 1000);
  if (s < 60) return `${Math.round(s)} s`;
  if (s < 3600) return `${Math.round(s / 60)} min`;
  if (s < 86400) return `${Math.round(s / 3600)} h`;
  return `${Math.round(s / 86400)} d`;
}

/** A model id as its chip spells it: the vendor prefix off, lowercase. */
export function shortModel(model: string): string {
  return model.toLowerCase().replace(/^claude-/, "");
}

/** A session's open needs-you items: the ones the owner can still act on. */
export function openItemsOf(needs: NeedItem[] | null, sessionId: string): NeedItem[] {
  return (needs ?? []).filter((n) => n.session === sessionId && n.state === "open");
}

/** The session's open permission ask: the one item an allow or a deny decides (SPEC 6.2). */
export function permissionOf(needs: NeedItem[] | null, sessionId: string): NeedItem | undefined {
  return openItemsOf(needs, sessionId).find((n) => n.kind === "permission");
}

/** The item a session's action bar shows: its permission ask when it has one, else its first
 *  open item. */
export function openItemOf(needs: NeedItem[] | null, sessionId: string): NeedItem | undefined {
  const open = openItemsOf(needs, sessionId);
  return open.find((n) => n.kind === "permission") ?? open[0];
}

/** One item of a session's stream: a user prompt, a tool event's one line, or a plain note. */
export type StreamItem =
  | { kind: "prompt"; text: string; at: string }
  | { kind: "tool"; target: string; at: string }
  | { kind: "note"; text: string; at: string };

/** The session's events as its stream shows them: prompts as bubbles, tool events as one line
 *  with their target, and any other text the report carries as a plain note. Kinds that carry
 *  no text (a session starting, a quiet notification) draw nothing. */
export function streamItems(events: RegistryEvent[]): StreamItem[] {
  return events.map(eventItem).filter((x): x is StreamItem => x !== null);
}

/** One event as its stream item: a prompt, a tool event's line, a note — or nothing, for the
 *  kinds that carry no text (a session starting, a quiet notification). */
function eventItem({ report }: RegistryEvent): StreamItem | null {
  const text = report.task || report.title || "";
  const at = report.last_event_at;
  switch (report.last_event) {
    case "UserPromptSubmit":
      return { kind: "prompt", text: text || "New prompt", at };
    case "PreToolUse":
    case "PostToolUse":
      return report.cwd ? { kind: "tool", target: report.cwd, at } : null;
    default:
      return text ? { kind: "note", text, at } : null;
  }
}

/** One message of the registry's stream, before its type is known. */
type StreamMessage = { type?: string; data?: unknown };

/** Does a stream message's data hold a session row? */
const isSessionRow = (d: unknown): d is RegistrySession => !!d && typeof d === "object";

/** Does a stream message's data hold a needs-you item? */
const isNeedItem = (d: unknown): d is NeedItem =>
  !!d && typeof d === "object" && typeof (d as { id?: unknown }).id === "number";

/** The registry's own state in one object, mutated in place the way the channel store's is. */
export type RegistryState = {
  /** Every reported session, newest activity first; null until the first read answered. */
  sessions: RegistrySession[] | null;
  /** Every waiting needs-you item, newest first; null until the first read answered. */
  needs: NeedItem[] | null;
  /** The session the address names, with its events; null while it loads or when none is open. */
  open: RegistryDetail | null;
  /** The session the address names, or null on every other tab. */
  openId: string | null;
  /** The named session answered 404: the address names no session there is. */
  openMissing: boolean;
  live: LiveStatus;
};

/** The registry store: its state, its readers and the live stream that patches it. */
export type RegistryStore = {
  getState: () => RegistryState;
  /** Subscribes to every change of the registry's state. */
  subscribe: (f: () => void) => () => void;
  /** GET /api/registry/sessions and /api/needs: both lists, each read kept alone on a failure. */
  load: () => Promise<void>;
  /** GET /api/registry/sessions/<id>: the open session's detail. */
  loadSession: (id: string) => Promise<void>;
  /** Names the session the address opened, loading its detail; null closes it. */
  setOpenId: (id: string | null) => void;
  /** One registry.session row: replaces or adds it, first, and refreshes the open detail. */
  applyRow: (row: RegistrySession) => void;
  /** One needs.item row: replaces it, or drops it once it is no longer waiting. */
  applyNeed: (item: NeedItem) => void;
  /** Drops one needs-you item by id (a decision's or a dismissal's optimistic half). */
  removeNeed: (id: number) => void;
  /** Replaces the whole needs-you list (a refused action's rollback). */
  setNeeds: (items: NeedItem[] | null) => void;
  /** Opens the all-channels stream (/api/registry/live). */
  connect: () => void;
  disconnect: () => void;
  /** How many sessions need the owner right now. */
  needs: () => number;
};

/** Builds the registry store over its injected host. */
export function createRegistry(deps: {
  api: Pick<Api, "api">;
  EventSource?: EventSourceFactory | undefined;
  fetch?: ((url: string) => Promise<Response>) | undefined;
  onSignedOut?: (() => void) | undefined;
}): RegistryStore {
  const state: RegistryState = {
    sessions: null,
    needs: null,
    open: null,
    openId: null,
    openMissing: false,
    live: "connecting",
  };
  const subs = new Set<() => void>();
  const changed = (): void => {
    for (const f of subs) f();
  };

  async function load(): Promise<void> {
    const [sessions, items] = await Promise.all([
      deps.api.api("/api/registry/sessions").catch(() => null),
      deps.api.api("/api/needs").catch(() => null),
    ]);
    // each read stands alone: a needs list that fails leaves the sessions standing, and the
    // other way round
    const list = sessions as { sessions?: RegistrySession[] } | null;
    if (list) state.sessions = list.sessions ?? [];
    const asks = items as { items?: NeedItem[] } | null;
    if (asks) state.needs = asks.items ?? [];
    if (list || asks) changed();
  }

  async function loadSession(id: string): Promise<void> {
    try {
      const detail = (await deps.api.api(
        `/api/registry/sessions/${encodeURIComponent(id)}`,
      )) as RegistryDetail;
      if (state.openId !== id) return; // the address moved on while the read was out
      state.open = detail;
      state.openMissing = false;
    } catch (e) {
      if ((e as { status?: number }).status !== 404) return; // transient: the old detail stays
      if (state.openId !== id) return;
      state.open = null;
      state.openMissing = true;
    }
    changed();
  }

  function applyRow(row: RegistrySession): void {
    if (!row?.session_id) return;
    if (state.sessions) {
      const rest = state.sessions.filter((s) => s.session_id !== row.session_id);
      state.sessions = [row, ...rest]; // the refreshed row is the newest activity
    }
    if (state.openId === row.session_id) void loadSession(row.session_id);
    changed();
  }

  function applyNeed(item: NeedItem): void {
    if (!item || typeof item.id !== "number") return;
    const rest = (state.needs ?? []).filter((n) => n.id !== item.id);
    // open and snoozed items wait; a decided, withdrawn or expired one no longer does
    state.needs = item.state === "open" || item.state === "snoozed" ? [item, ...rest] : rest;
    changed();
  }

  function removeNeed(id: number): void {
    if (!(state.needs ?? []).some((n) => n.id === id)) return;
    state.needs = (state.needs ?? []).filter((n) => n.id !== id);
    changed();
  }

  function setNeeds(items: NeedItem[] | null): void {
    state.needs = items;
    changed();
  }

  let es: EventSourceLike | null = null;
  let hellos = 0;

  function onStreamMessage(m: StreamMessage): void {
    if (m.type === "hello") {
      onHello();
      return;
    }
    if (m.type === "registry.session" && isSessionRow(m.data)) applyRow(m.data);
    if (m.type === "needs.item" && isNeedItem(m.data)) applyNeed(m.data);
  }

  /** A hello says the stream is live; any but the first also refills what went by while away,
   *  which the stream did not push. */
  function onHello(): void {
    state.live = "live";
    if (hellos++ > 0) {
      void load();
      if (state.openId) void loadSession(state.openId);
    } else if (state.sessions === null) void load();
  }

  function connect(): void {
    disconnect();
    if (!deps.EventSource) return;
    state.live = "connecting";
    es = deps.EventSource("/api/registry/live");
    es.onopen = () => {
      state.live = "live";
      changed();
    };
    es.onerror = () => {
      state.live = "offline";
      changed();
      // a 401 here means the sign-in itself ended, not just the stream (live.ts's own check)
      deps
        .fetch?.("/api/whoami")
        .then((r) =>
          r.status === 401
            ? r.json().then((j) => {
                if (j && (j as { signin?: unknown }).signin) deps.onSignedOut?.();
              })
            : null,
        )
        .catch(() => {});
    };
    es.onmessage = (ev) => {
      try {
        onStreamMessage(JSON.parse(ev.data) as StreamMessage);
      } catch {
        // a frame that is not JSON changes nothing
      }
    };
  }

  function disconnect(): void {
    try {
      es?.close();
    } catch {
      // already gone
    }
    es = null;
  }

  return {
    getState: () => state,
    subscribe(f) {
      subs.add(f);
      return () => subs.delete(f);
    },
    load,
    loadSession,
    setOpenId(id) {
      if (state.openId === id) return;
      state.openId = id;
      state.open = null;
      state.openMissing = false;
      if (id) void loadSession(id);
    },
    applyRow,
    applyNeed,
    removeNeed,
    setNeeds,
    connect,
    disconnect,
    needs: () => needsYouCount(state.sessions),
  };
}
