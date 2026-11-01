// actions.test.ts — what each kind of needs-you item offers its owner: the buttons, the link
// under them, the note, the plain title, and the keys the list's footer names for the focused row.

import { describe, expect, it } from "vite-plus/test";
import { askActionsOf, askTitleOf, footerKeysOf } from "../../../src/app/ask/actions.ts";

describe("askActionsOf", () => {
  it("offers a permission Deny and Allow once, with Reply instead under them on the wide layouts", () => {
    for (const layout of ["tablet", "desktop"] as const) {
      const a = askActionsOf("permission", layout);
      expect(a.buttons).toEqual([
        { id: "deny", label: "Deny", tone: "danger" },
        { id: "allow", label: "Allow once", tone: "primary" },
      ]);
      expect(a.ghost, layout).toEqual({ id: "reply", label: "Reply instead" });
      expect(a.note).toBeNull();
    }
  });

  it("offers the phone's permission ask the same two buttons and no Reply instead", () => {
    const a = askActionsOf("permission", "phone");
    expect(a.buttons).toEqual(askActionsOf("permission", "desktop").buttons);
    expect(a.ghost).toBeNull();
  });

  it("offers a finished turn Mark read and Reply, with Copy terminal command under them", () => {
    const a = askActionsOf("turn", "phone");
    expect(a.buttons).toEqual([
      { id: "dismiss", label: "Mark read", tone: "secondary" },
      { id: "reply", label: "Reply", tone: "primary" },
    ]);
    expect(a.ghost).toEqual({ id: "copy-terminal", label: "Copy terminal command" });
    expect(a.note).toBeNull();
  });

  it("offers a question Dismiss and Answer, and says when the answer arrives", () => {
    const a = askActionsOf("question", "phone");
    expect(a.buttons).toEqual([
      { id: "dismiss", label: "Dismiss", tone: "secondary" },
      { id: "answer", label: "Answer", tone: "primary" },
    ]);
    expect(a.ghost).toBeNull();
    expect(a.note).toBe("Your answer arrives on the session's next prompt.");
  });

  it("gives a blocked task, and any kind it does not know, the question's actions", () => {
    expect(askActionsOf("blocked", "phone")).toEqual(askActionsOf("question", "phone"));
    expect(askActionsOf("something-new", "desktop")).toEqual(askActionsOf("question", "desktop"));
  });
});

describe("askTitleOf", () => {
  it("names a permission by what it would do", () => {
    expect(askTitleOf("permission", "Bash")).toBe("Allow this command?");
    expect(askTitleOf("permission", "Edit")).toBe("Allow this edit?");
    expect(askTitleOf("permission", "MultiEdit")).toBe("Allow this edit?");
    expect(askTitleOf("permission", "Write")).toBe("Allow this file write?");
    expect(askTitleOf("permission", "WebFetch")).toBe("Allow this page fetch?");
    expect(askTitleOf("permission", "mcp__x__y")).toBe("Allow mcp__x__y?");
  });

  it("says plainly what a question and a finished turn are, and leaves any other kind untitled", () => {
    expect(askTitleOf("question", "")).toBe("Claude asks you a question");
    expect(askTitleOf("turn", "")).toBe("Finished, waiting for you");
    expect(askTitleOf("blocked", "")).toBeNull();
  });
});

describe("footerKeysOf", () => {
  it("names the keys the focused row's kind answers to", () => {
    expect(footerKeysOf("permission")).toEqual(["a", "d", "r"]);
    expect(footerKeysOf("question")).toEqual(["r"]);
    expect(footerKeysOf("turn")).toEqual(["r"]);
    expect(footerKeysOf("blocked")).toEqual(["r"]);
    expect(footerKeysOf("stuck")).toEqual(["r"]);
    expect(footerKeysOf("ended")).toEqual([]);
  });
});
