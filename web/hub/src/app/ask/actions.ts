// actions.ts — what each kind of needs-you item offers its owner: the row of two buttons, the
// link under them, the note beneath, the plain title, and the keys the list's footer names for
// the focused row. Pure data: Needs.tsx wires each id to the call it makes.

import type { Layout } from "../layout.ts";

/** The id a button or link carries as its `data-act`. */
export type AskActionId = "deny" | "allow" | "reply" | "dismiss" | "answer" | "copy-terminal";

/** How loud a button draws: the one main answer is primary. */
export type AskTone = "danger" | "primary" | "secondary";

export type AskButton = { id: AskActionId; label: string; tone: AskTone };

/** The quiet link under the buttons. */
export type AskLink = { id: AskActionId; label: string };

export type AskActions = {
  /** The row of two, left to right. */
  buttons: readonly AskButton[];
  ghost: AskLink | null;
  note: string | null;
};

const PERMISSION_BUTTONS: readonly AskButton[] = [
  { id: "deny", label: "Deny", tone: "danger" },
  { id: "allow", label: "Allow once", tone: "primary" },
];

/** The wide layouts' permission ask keeps Reply instead under its buttons. */
const PERMISSION_WIDE: AskActions = {
  buttons: PERMISSION_BUTTONS,
  ghost: { id: "reply", label: "Reply instead" },
  note: null,
};

/** The phone's permission ask is the two buttons alone: a third stacked action would push the
 *  transcript below the fold. */
const PERMISSION_PHONE: AskActions = { buttons: PERMISSION_BUTTONS, ghost: null, note: null };

const TURN: AskActions = {
  buttons: [
    { id: "dismiss", label: "Mark read", tone: "secondary" },
    { id: "reply", label: "Reply", tone: "primary" },
  ],
  ghost: { id: "copy-terminal", label: "Copy terminal command" },
  note: null,
};

const QUESTION: AskActions = {
  buttons: [
    { id: "dismiss", label: "Dismiss", tone: "secondary" },
    { id: "answer", label: "Answer", tone: "primary" },
  ],
  ghost: null,
  note: "Your answer arrives on the session's next prompt.",
};

/** The actions of one item kind on one layout. A blocked task, and any kind the hub adds later,
 *  is answered like a question. */
export function askActionsOf(kind: string, layout: Layout): AskActions {
  if (kind === "permission") return layout === "phone" ? PERMISSION_PHONE : PERMISSION_WIDE;
  if (kind === "turn") return TURN;
  return QUESTION;
}

/** What a permission ask for one tool would do, as its title says it. */
function permissionTitle(tool: string): string {
  if (tool === "Bash") return "Allow this command?";
  if (tool === "Edit" || tool === "MultiEdit") return "Allow this edit?";
  if (tool === "Write") return "Allow this file write?";
  if (tool === "WebFetch") return "Allow this page fetch?";
  return `Allow ${tool}?`;
}

/** The plain title of an item's box, or null when the item's own summary is the title. */
export function askTitleOf(kind: string, tool: string): string | null {
  if (kind === "permission") return permissionTitle(tool);
  if (kind === "question") return "Claude asks you a question";
  if (kind === "turn") return "Finished, waiting for you";
  return null;
}

/** The keys the list's footer names while a row of this kind has the focus. */
export function footerKeysOf(kind: string): readonly string[] {
  if (kind === "permission") return ["a", "d", "r"];
  if (kind === "ended") return [];
  return ["r"];
}
