// session/types.ts — the wire types of the hub's session screens' routes: one type per answer,
// every key spelled as the Go answer spells it on the wire. The screens read only these; the
// client in session/api.ts decodes every body into them at the edge.

/** One subagent of a session, as GET /api/sessions/<id>/agents answers it. */
export type SessionAgent = {
  id: string;
  type: string;
  description?: string;
  depth: number;
  running: boolean;
  model?: string;
  side_chat?: boolean;
  started_at?: string;
};

/** One item of a session's transcript, as GET /api/sessions/<id>/agents/<agent>/conversation
 *  answers it. The kind is one of the transcript's own kinds; which other keys ride along
 *  depends on it. */
export type ConvoItem = {
  id: string;
  kind: "user" | "assistant" | "thinking" | "tool_use" | "tool_result" | "file" | "image";
  at: string;
  text?: string;
  duration_ms?: number;
  tool?: string;
  tool_use_id?: string;
  input?: string;
  is_error?: boolean;
  result?: ToolFacts;
  cut?: boolean;
  parent?: string;
  path?: string;
  lines?: number;
  media_type?: string;
  bytes?: number;
};

/** What the transcript knows about one tool call's outcome. */
export type ToolFacts = {
  exit_code?: number;
  stderr?: string;
  interrupted?: boolean;
  background_id?: string;
  file_path?: string;
  hunks?: Hunk[];
  answers?: Answer[];
  cut?: boolean;
};

/** One hunk of an edit a tool call made to a file: the old and new line spans and their lines. */
export type Hunk = {
  old_start: number;
  old_lines: number;
  new_start: number;
  new_lines: number;
  lines: string[];
};

/** One question a tool asked the owner and the answer it got. */
export type Answer = {
  header?: string;
  question: string;
  answer: string;
};

/** One page of a transcript: the items from the cursor on, and the cursor to ask for next. */
export type ConvoPage = {
  items: ConvoItem[];
  next: number;
};

/** One background shell of a session's agent, as GET /api/sessions/<id>/agents/<agent>/shells
 *  answers it. */
export type Shell = {
  id: string;
  command: string;
  description?: string;
  state: string;
  status?: string;
  exit_code?: number;
  started_at: string;
  ended_at?: string;
};

/** One page of a shell's output: the text from the byte cursor on, the cursor to ask for next,
 *  and the output's whole size. */
export type ShellOutputPage = {
  text: string;
  next: number;
  size: number;
};

/** What a session is working on, as GET /api/sessions/<id>/tasks answers it: its own todo list,
 *  the channel tasks its member owns, and the kanban cards it holds. `board` names the kanban
 *  board the cards sit on; each card carries its cluster and whether the session's member holds
 *  it or only authored it. An older hub answers none of these. */
export type SessionTasks = {
  todos: {
    id: string;
    content: string;
    status: string;
    active_form?: string;
  }[];
  tasks: {
    id: string;
    title: string;
    status: string;
    channel: string;
  }[];
  cards: {
    id: string;
    title: string;
    column: string;
    cluster?: string;
    source?: string;
  }[];
  board?: string;
};

/** The facts of a session's current turn, as GET /api/sessions/<id>/turn answers it; radar's
 *  output tokens for the turn ride along when radar knows them. */
export type TurnFacts = {
  started_at?: string;
  ended: boolean;
  duration_ms?: number;
  permission_mode?: string;
  output_tokens?: number;
};

/** What runs in a session right now, as GET /api/sessions/<id>/live answers it: each running
 *  subagent and each running background shell. Both lists are always arrays. */
export type LiveTray = {
  agents: LiveAgent[];
  shells: LiveShell[];
};

/** One running subagent, with radar's facts when radar knows the agent. */
export type LiveAgent = {
  id: string;
  type: string;
  description?: string;
  started_at?: string;
  output_tokens?: number;
  last_activity?: string;
  what?: string;
};

/** One running background shell and the agent whose transcript ran it. */
export type LiveShell = {
  agent: string;
  id: string;
  command: string;
  description?: string;
  started_at: string;
};
