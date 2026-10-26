// session/api.ts — the typed client for the hub's session screens: one method per route, each
// asking the exact path with its id parts encoded and decoding the answer at the edge into the
// types of session/types.ts. A decoder checks every required field, drops unknown fields, keeps
// optional fields absent when the wire leaves them out, and throws one Error naming the route
// and the first bad field when a body fails its decode. No caching, polling or state: the
// screens own that.
import type { Api } from "../api.ts";
import type {
  Answer,
  ConvoItem,
  ConvoPage,
  Hunk,
  LiveAgent,
  LiveShell,
  LiveTray,
  SessionAgent,
  SessionTasks,
  Shell,
  ShellOutputPage,
  ToolFacts,
  TurnFacts,
} from "./types.ts";

/** The typed client the session screens read their routes through. */
export type SessionClient = {
  agents: (id: string) => Promise<SessionAgent[]>;
  conversation: (id: string, agent: string, after?: number) => Promise<ConvoPage>;
  shells: (id: string, agent: string) => Promise<Shell[]>;
  shellOutput: (id: string, agent: string, shell: string, after?: number) => Promise<ShellOutputPage>;
  tasks: (id: string) => Promise<SessionTasks>;
  turn: (id: string) => Promise<TurnFacts>;
  live: (id: string) => Promise<LiveTray>;
};

/** The client over the dashboard's own API: every method is a GET, `after` pages a transcript or
 *  a shell output from a byte cursor. */
export function sessionApi(api: Api): SessionClient {
  const enc = encodeURIComponent;
  return {
    agents: async (id) => {
      const path = `/api/sessions/${enc(id)}/agents`;
      return decodeAgents(path, await api.api(path));
    },
    conversation: async (id, agent, after) => {
      const path = `/api/sessions/${enc(id)}/agents/${enc(agent)}/conversation${afterQuery(after)}`;
      return decodePage(path, await api.api(path));
    },
    shells: async (id, agent) => {
      const path = `/api/sessions/${enc(id)}/agents/${enc(agent)}/shells`;
      return decodeShells(path, await api.api(path));
    },
    shellOutput: async (id, agent, shell, after) => {
      const path = `/api/sessions/${enc(id)}/agents/${enc(agent)}/shells/${enc(shell)}/output${afterQuery(after)}`;
      return decodeOutputPage(path, await api.api(path));
    },
    tasks: async (id) => {
      const path = `/api/sessions/${enc(id)}/tasks`;
      return decodeTasks(path, await api.api(path));
    },
    turn: async (id) => {
      const path = `/api/sessions/${enc(id)}/turn`;
      return decodeTurn(path, await api.api(path));
    },
    live: async (id) => {
      const path = `/api/sessions/${enc(id)}/live`;
      return decodeLive(path, await api.api(path));
    },
  };
}

/** The `?after=` suffix of a paged route: empty when the caller reads from the start. */
function afterQuery(after: number | undefined): string {
  return after === undefined ? "" : `?after=${after}`;
}

/** One decode failure: the route, the field trail and what the field should have been. */
function fail(at: string, want: string): never {
  throw new Error(`${at} must be ${want}`);
}

/** One decoded body as an object; anything else fails. */
function obj(at: string, v: unknown): Record<string, unknown> {
  if (typeof v !== "object" || v === null || Array.isArray(v)) fail(at, "an object");
  return v as Record<string, unknown>;
}

function str(at: string, v: unknown): string {
  if (typeof v !== "string") fail(at, "a string");
  return v;
}

function num(at: string, v: unknown): number {
  if (typeof v !== "number") fail(at, "a number");
  return v;
}

function bool(at: string, v: unknown): boolean {
  if (typeof v !== "boolean") fail(at, "a boolean");
  return v;
}

function arr(at: string, v: unknown): unknown[] {
  if (!Array.isArray(v)) fail(at, "an array");
  return v;
}

/** The kinds a transcript item can have, as the wire spells them. */
const kinds = ["user", "assistant", "thinking", "tool_use", "tool_result", "file", "image"] as const;

function kind(at: string, v: unknown): ConvoItem["kind"] {
  for (const k of kinds) {
    if (v === k) return k;
  }
  fail(at, `one of ${kinds.join(", ")}`);
}

// The decoders, in route order. Each rebuilds the answer key by key: unknown fields are left
// out, an optional key is set only when the wire carries it, and every field names itself in the
// error trail as `route: items[1].result.hunks[0].lines`.

function decodeAgents(path: string, v: unknown): SessionAgent[] {
  const o = obj(`${path}: answer`, v);
  return arr(`${path}: agents`, o.agents).map((agent, i) => decodeAgent(`${path}: agents[${i}]`, agent));
}

function decodeAgent(base: string, v: unknown): SessionAgent {
  const o = obj(base, v);
  const out: SessionAgent = {
    id: str(`${base}.id`, o.id),
    type: str(`${base}.type`, o.type),
    depth: num(`${base}.depth`, o.depth),
    running: bool(`${base}.running`, o.running),
  };
  if (o.description !== undefined) out.description = str(`${base}.description`, o.description);
  if (o.model !== undefined) out.model = str(`${base}.model`, o.model);
  if (o.side_chat !== undefined) out.side_chat = bool(`${base}.side_chat`, o.side_chat);
  if (o.started_at !== undefined) out.started_at = str(`${base}.started_at`, o.started_at);
  return out;
}

function decodePage(path: string, v: unknown): ConvoPage {
  const o = obj(`${path}: answer`, v);
  return {
    items: arr(`${path}: items`, o.items).map((item, i) => decodeItem(`${path}: items[${i}]`, item)),
    next: num(`${path}: next`, o.next),
  };
}

function decodeItem(base: string, v: unknown): ConvoItem {
  const o = obj(base, v);
  const out: ConvoItem = {
    id: str(`${base}.id`, o.id),
    kind: kind(`${base}.kind`, o.kind),
    at: str(`${base}.at`, o.at),
  };
  if (o.text !== undefined) out.text = str(`${base}.text`, o.text);
  if (o.duration_ms !== undefined) out.duration_ms = num(`${base}.duration_ms`, o.duration_ms);
  if (o.tool !== undefined) out.tool = str(`${base}.tool`, o.tool);
  if (o.tool_use_id !== undefined) out.tool_use_id = str(`${base}.tool_use_id`, o.tool_use_id);
  if (o.input !== undefined) out.input = str(`${base}.input`, o.input);
  if (o.is_error !== undefined) out.is_error = bool(`${base}.is_error`, o.is_error);
  if (o.result !== undefined) out.result = decodeToolFacts(`${base}.result`, o.result);
  if (o.cut !== undefined) out.cut = bool(`${base}.cut`, o.cut);
  if (o.parent !== undefined) out.parent = str(`${base}.parent`, o.parent);
  if (o.path !== undefined) out.path = str(`${base}.path`, o.path);
  if (o.lines !== undefined) out.lines = num(`${base}.lines`, o.lines);
  if (o.media_type !== undefined) out.media_type = str(`${base}.media_type`, o.media_type);
  if (o.bytes !== undefined) out.bytes = num(`${base}.bytes`, o.bytes);
  return out;
}

function decodeToolFacts(base: string, v: unknown): ToolFacts {
  const o = obj(base, v);
  const out: ToolFacts = {};
  if (o.exit_code !== undefined) out.exit_code = num(`${base}.exit_code`, o.exit_code);
  if (o.stderr !== undefined) out.stderr = str(`${base}.stderr`, o.stderr);
  if (o.interrupted !== undefined) out.interrupted = bool(`${base}.interrupted`, o.interrupted);
  if (o.background_id !== undefined) out.background_id = str(`${base}.background_id`, o.background_id);
  if (o.file_path !== undefined) out.file_path = str(`${base}.file_path`, o.file_path);
  if (o.hunks !== undefined) {
    out.hunks = arr(`${base}.hunks`, o.hunks).map((hunk, i) => decodeHunk(`${base}.hunks[${i}]`, hunk));
  }
  if (o.answers !== undefined) {
    out.answers = arr(`${base}.answers`, o.answers).map((answer, i) =>
      decodeAnswer(`${base}.answers[${i}]`, answer),
    );
  }
  if (o.cut !== undefined) out.cut = bool(`${base}.cut`, o.cut);
  return out;
}

function decodeHunk(base: string, v: unknown): Hunk {
  const o = obj(base, v);
  return {
    old_start: num(`${base}.old_start`, o.old_start),
    old_lines: num(`${base}.old_lines`, o.old_lines),
    new_start: num(`${base}.new_start`, o.new_start),
    new_lines: num(`${base}.new_lines`, o.new_lines),
    lines: arr(`${base}.lines`, o.lines).map((line, i) => str(`${base}.lines[${i}]`, line)),
  };
}

function decodeAnswer(base: string, v: unknown): Answer {
  const o = obj(base, v);
  const out: Answer = {
    question: str(`${base}.question`, o.question),
    answer: str(`${base}.answer`, o.answer),
  };
  if (o.header !== undefined) out.header = str(`${base}.header`, o.header);
  return out;
}

function decodeShells(path: string, v: unknown): Shell[] {
  const o = obj(`${path}: answer`, v);
  return arr(`${path}: shells`, o.shells).map((shell, i) => decodeShell(`${path}: shells[${i}]`, shell));
}

function decodeShell(base: string, v: unknown): Shell {
  const o = obj(base, v);
  const out: Shell = {
    id: str(`${base}.id`, o.id),
    command: str(`${base}.command`, o.command),
    state: str(`${base}.state`, o.state),
    started_at: str(`${base}.started_at`, o.started_at),
  };
  if (o.description !== undefined) out.description = str(`${base}.description`, o.description);
  if (o.status !== undefined) out.status = str(`${base}.status`, o.status);
  if (o.exit_code !== undefined) out.exit_code = num(`${base}.exit_code`, o.exit_code);
  if (o.ended_at !== undefined) out.ended_at = str(`${base}.ended_at`, o.ended_at);
  return out;
}

function decodeOutputPage(path: string, v: unknown): ShellOutputPage {
  const o = obj(`${path}: answer`, v);
  return {
    text: str(`${path}: text`, o.text),
    next: num(`${path}: next`, o.next),
    size: num(`${path}: size`, o.size),
  };
}

function decodeTasks(path: string, v: unknown): SessionTasks {
  const o = obj(`${path}: answer`, v);
  return {
    todos: arr(`${path}: todos`, o.todos).map((todo, i) => decodeTodo(`${path}: todos[${i}]`, todo)),
    tasks: arr(`${path}: tasks`, o.tasks).map((task, i) => decodeTaskEntry(`${path}: tasks[${i}]`, task)),
    cards: arr(`${path}: cards`, o.cards).map((card, i) => decodeCardEntry(`${path}: cards[${i}]`, card)),
  };
}

function decodeTodo(base: string, v: unknown): SessionTasks["todos"][number] {
  const o = obj(base, v);
  const out: SessionTasks["todos"][number] = {
    id: str(`${base}.id`, o.id),
    content: str(`${base}.content`, o.content),
    status: str(`${base}.status`, o.status),
  };
  if (o.active_form !== undefined) out.active_form = str(`${base}.active_form`, o.active_form);
  return out;
}

function decodeTaskEntry(base: string, v: unknown): SessionTasks["tasks"][number] {
  const o = obj(base, v);
  return {
    id: str(`${base}.id`, o.id),
    title: str(`${base}.title`, o.title),
    status: str(`${base}.status`, o.status),
    channel: str(`${base}.channel`, o.channel),
  };
}

function decodeCardEntry(base: string, v: unknown): SessionTasks["cards"][number] {
  const o = obj(base, v);
  return {
    id: str(`${base}.id`, o.id),
    title: str(`${base}.title`, o.title),
    column: str(`${base}.column`, o.column),
  };
}

function decodeTurn(path: string, v: unknown): TurnFacts {
  const o = obj(`${path}: answer`, v);
  const out: TurnFacts = { ended: bool(`${path}: ended`, o.ended) };
  if (o.started_at !== undefined) out.started_at = str(`${path}: started_at`, o.started_at);
  if (o.duration_ms !== undefined) out.duration_ms = num(`${path}: duration_ms`, o.duration_ms);
  if (o.permission_mode !== undefined) {
    out.permission_mode = str(`${path}: permission_mode`, o.permission_mode);
  }
  if (o.output_tokens !== undefined) out.output_tokens = num(`${path}: output_tokens`, o.output_tokens);
  return out;
}

function decodeLive(path: string, v: unknown): LiveTray {
  const o = obj(`${path}: answer`, v);
  return {
    agents: arr(`${path}: agents`, o.agents).map((agent, i) =>
      decodeLiveAgent(`${path}: agents[${i}]`, agent),
    ),
    shells: arr(`${path}: shells`, o.shells).map((shell, i) =>
      decodeLiveShell(`${path}: shells[${i}]`, shell),
    ),
  };
}

function decodeLiveAgent(base: string, v: unknown): LiveAgent {
  const o = obj(base, v);
  const out: LiveAgent = {
    id: str(`${base}.id`, o.id),
    type: str(`${base}.type`, o.type),
  };
  if (o.description !== undefined) out.description = str(`${base}.description`, o.description);
  if (o.started_at !== undefined) out.started_at = str(`${base}.started_at`, o.started_at);
  if (o.output_tokens !== undefined) out.output_tokens = num(`${base}.output_tokens`, o.output_tokens);
  if (o.last_activity !== undefined) out.last_activity = str(`${base}.last_activity`, o.last_activity);
  if (o.what !== undefined) out.what = str(`${base}.what`, o.what);
  return out;
}

function decodeLiveShell(base: string, v: unknown): LiveShell {
  const o = obj(base, v);
  const out: LiveShell = {
    agent: str(`${base}.agent`, o.agent),
    id: str(`${base}.id`, o.id),
    command: str(`${base}.command`, o.command),
    started_at: str(`${base}.started_at`, o.started_at),
  };
  if (o.description !== undefined) out.description = str(`${base}.description`, o.description);
  return out;
}
