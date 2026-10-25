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
  /** The coding agent the session runs in, by its harness id; a report without one is Claude Code. */
  harness?: string;
  /** The tool a PreToolUse or PostToolUse event runs, and its short target (a Bash command's
   *  first line, a file path, an MCP tool's name); an older hook sends neither. */
  tool?: string;
  tool_target?: string;
  /** The first line of the prompt a UserPromptSubmit event carries. */
  prompt_line?: string;
  last_event: string;
  last_event_at: string;
};

/** One Claude Code agent-team teammate under a session: its name and the state it reported. */
export type RegistryTeammate = { name: string; state: string };

/** One registry row: the report's fields with the state computed when it was read (SPEC 5.2),
 *  the route a message to this session would take (SPEC 6.4), its teammates, sorted by name,
 *  when it leads an agent team, and whether its hooks stopped reporting. */
export type RegistrySession = RegistryReport & {
  state: string;
  delivery_route?: string;
  teammates?: RegistryTeammate[];
  /** The session's transcript runs ahead of its reports while its process lives: its hooks no
   *  longer reach the registry. The list and detail reads carry it; a live row comes from a
   *  report, so it reads false. */
  hooks_stale?: boolean;
};

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
  stuck: RegistrySession[];
  ended: RegistrySession[];
};

/** What a group-by toggle holds: the rows group by machine or by project. */
export type GroupMode = "machine" | "project";

/** The word each state's mark carries: shape plus word, never colour alone (SPEC 9). */
export function stateWord(state: string): string {
  return state === "needs-you" ? "needs you" : state;
}

/** The state roles tokens.css names: a state paints its mark with --ui-<role>, its chip with
 *  --ui-<role>-soft behind --ui-<role>-ink. */
export type StateRole = "needs-you" | "turn-done" | "working" | "idle" | "failed" | "cancelled";

/** The role each state takes; stuck reads failed and ended stays quiet. */
const STATE_ROLES: Record<string, StateRole> = {
  "needs-you": "needs-you",
  working: "working",
  idle: "idle",
  stuck: "failed",
  ended: "cancelled",
};

/** The role a state paints with. A session that needs the owner for a finished turn reads
 *  turn-done (orange); any other ask, or none known, reads needs-you (red). An unknown state
 *  reads idle, the way its hollow mark does. */
export function stateRole(state: string, item?: NeedItem): StateRole {
  if (state === "needs-you" && item?.kind === "turn") return "turn-done";
  return STATE_ROLES[state] ?? "idle";
}

/** The state a session shows: an open item holds it in needs-you whatever its last report said,
 *  since a live report row does not know the item; an ended session stays ended. */
export function shownState(s: RegistrySession, needs: NeedItem[] | null): string {
  if (s.state === "ended") return s.state;
  return openItemsOf(needs, s.session_id).length > 0 ? "needs-you" : s.state;
}

/** Splits the rows into the Now list's groups, keeping the read's newest-activity order. */
export function groupSessions(sessions: RegistrySession[] | null): RegistryGroups {
  const g: RegistryGroups = { needsYou: [], working: [], idle: [], stuck: [], ended: [] };
  for (const s of sessions ?? []) {
    if (s.state === "needs-you") g.needsYou.push(s);
    else if (s.state === "working") g.working.push(s);
    else if (s.state === "idle") g.idle.push(s);
    else if (s.state === "stuck") g.stuck.push(s);
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
  if (layout !== "phone") return names.join(" · ");
  return `${names.length} ${names.length === 1 ? "machine" : "machines"}`;
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

/** One coding agent a session runs in, as the chips draw it: its id, its name in words and its
 *  two-letter mark. */
export type Harness = { id: string; name: string; mark: string };

/** Every harness huddle reports from: Claude Code's own hook, and the agents the harness word
 *  wires (internal/harness), keyed by the id their reports carry. */
export const HARNESSES: Readonly<Record<string, Harness>> = {
  claude: { id: "claude", name: "Claude Code", mark: "CC" },
  codex: { id: "codex", name: "Codex", mark: "CX" },
  gemini: { id: "gemini", name: "Gemini", mark: "GE" },
  qwen: { id: "qwen", name: "Qwen Code", mark: "QW" },
  opencode: { id: "opencode", name: "opencode", mark: "OC" },
  crush: { id: "crush", name: "Crush", mark: "CR" },
  omp: { id: "omp", name: "omp", mark: "OM" },
  pi: { id: "pi", name: "pi", mark: "PI" },
  zcode: { id: "zcode", name: "zcode", mark: "ZC" },
};

/** The harness a session runs in: a report without one comes from Claude Code's hook, and an id
 *  the table does not know is named by the id itself. */
export function harnessOf(row: Pick<RegistryReport, "harness">): Harness {
  const id = row.harness || "claude";
  return HARNESSES[id] ?? { id, name: id, mark: id.slice(0, 2).toUpperCase() };
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

/** One tool call inside a stream row: running from its PreToolUse until its PostToolUse makes it
 *  done. A call whose PostToolUse never lands (a denied permission, a failed tool, an ended
 *  session) stays running until the session's next Stop or prompt, which closes it as ended. */
export type ToolCall = { target: string; state: "running" | "done" | "ended"; at: string };

/** One item of a session's stream: a user prompt, a run of consecutive calls of one tool, or a
 *  plain note. */
export type StreamItem =
  | { kind: "prompt"; text: string; at: string }
  | { kind: "tool"; tool: string; calls: ToolCall[]; at: string }
  | { kind: "note"; text: string; at: string };

/** The session's events as its stream shows them: prompts as bubbles, consecutive calls of one
 *  tool as one row named by the tool (a different tool, a prompt or a note starts the next row),
 *  and any other text the report carries as a plain note. A PreToolUse opens a running call that
 *  its PostToolUse (the same tool and target) finishes; a PostToolUse with no open call stands
 *  alone. A Stop or a prompt closes every call still running as ended, since no PostToolUse
 *  follows the end of a turn. Kinds that carry no text (a session starting, a quiet
 *  notification) draw nothing. */
export function streamItems(events: RegistryEvent[]): StreamItem[] {
  const items: StreamItem[] = [];
  const open: OpenCall[] = [];
  for (const { report } of events) {
    const at = report.last_event_at;
    const text = report.task || report.title || "";
    if (report.last_event === "UserPromptSubmit" || report.last_event === "Stop") {
      for (const { call } of open.splice(0)) call.state = "ended";
    }
    switch (report.last_event) {
      case "UserPromptSubmit":
        items.push({ kind: "prompt", text: report.prompt_line || text || "You sent a prompt", at });
        break;
      case "PreToolUse":
      case "PostToolUse":
        addToolEvent(items, open, report);
        break;
      default:
        if (text) items.push({ kind: "note", text, at });
    }
  }
  return items;
}

/** A call still waiting for its PostToolUse, with the tool it runs. */
type OpenCall = { tool: string; call: ToolCall };

/** One PreToolUse or PostToolUse into the stream: a PostToolUse finishes the open call of its
 *  tool and target; any other joins the last row when that row runs the same tool, else starts
 *  a new row. */
function addToolEvent(items: StreamItem[], open: OpenCall[], report: RegistryReport): void {
  const tool = report.tool || "Tool";
  const target = shortTarget(report.tool_target ?? "", report.cwd);
  const running = report.last_event === "PreToolUse";
  const started = running ? -1 : open.findIndex((o) => o.tool === tool && o.call.target === target);
  const finished = started >= 0 ? open.splice(started, 1)[0] : undefined;
  if (finished) {
    finished.call.state = "done";
    return;
  }
  const call: ToolCall = { target, state: running ? "running" : "done", at: report.last_event_at };
  if (running) open.push({ tool, call });
  const last = items[items.length - 1];
  if (last?.kind === "tool" && last.tool === tool) last.calls.push(call);
  else items.push({ kind: "tool", tool, calls: [call], at: call.at });
}

/** A tool's target as its row shows it: a path inside the session's directory by its path
 *  from there, anything else as it came. */
function shortTarget(target: string, cwd: string | undefined): string {
  const root = cwd ? `${cwd.replace(/\/+$/, "")}/` : "";
  return root && target.startsWith(root) ? target.slice(root.length) : target;
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

  /** The answered turn items of the open session, kept on the list while the owner reads it. */
  let held = new Set<number>();

  function applyNeed(item: NeedItem): void {
    if (!item || typeof item.id !== "number") return;
    const shown = (state.needs ?? []).some((n) => n.id === item.id);
    // reading a session answers its finished turn; the turn's sheet stays until the owner leaves
    if (item.state === "answered" && item.kind === "turn" && item.session === state.openId && shown) {
      held.add(item.id);
      return;
    }
    const rest = (state.needs ?? []).filter((n) => n.id !== item.id);
    // open and snoozed items wait; a decided, withdrawn or expired one no longer does
    state.needs = item.state === "open" || item.state === "snoozed" ? [item, ...rest] : rest;
    changed();
  }

  /** Drops the turn items held while their session was open. */
  function dropHeld(): void {
    if (held.size === 0) return;
    const gone = held;
    held = new Set();
    if (state.needs) state.needs = state.needs.filter((n) => !gone.has(n.id));
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
      dropHeld();
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
