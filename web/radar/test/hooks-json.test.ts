import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vite-plus/test";

/**
 * The merged plugin's hooks.json is load-bearing: Claude Code runs it in every session, and a malformed
 * file (hook objects listed directly under an event instead of matcher groups) makes hooks silently not
 * run — or worse, a bad entry can break the session. One file carries all three parts' hooks; these are
 * radar's cases over its share.
 */
type HookEntry = { type?: unknown; command?: unknown; timeout?: unknown };
type Group = { matcher?: unknown; hooks?: unknown };

const parsed = JSON.parse(
  readFileSync(fileURLToPath(new URL("../../../plugin/hooks/hooks.json", import.meta.url)), "utf8"),
) as { hooks?: Record<string, Group[]> };

/** Every event Claude Code fires that radar observes. */
const EXPECTED_EVENTS = [
  "SessionStart",
  "SessionEnd",
  "UserPromptSubmit",
  "SubagentStart",
  "SubagentStop",
  "Stop",
  "PreCompact",
  "Notification",
];

/** Events fired per tool call: a hook on them would start a process for every tool Claude runs. */
const TOOL_EVENTS = ["PreToolUse", "PostToolUse", "PostToolUseFailure"];

/**
 * The one command every radar hook runs: the committed launcher's radar word, which starts the huddle
 * binary (downloading it on first use) and never blocks or fails Claude Code. A regex here: the literal
 * ${…} in a plain string would trip lint.
 */
const COMMAND = /^sh "\$\{CLAUDE_PLUGIN_ROOT\}\/dist\/run" radar hook$/;

/** The groups of one event whose command is radar's launcher line. */
const radarGroups = (event: string): Group[] =>
  (parsed.hooks?.[event] ?? []).filter((group) =>
    ((group?.hooks ?? []) as HookEntry[]).some((entry) => COMMAND.test(String(entry.command))),
  );

/** One group must hold only command hooks with a command and a bounded timeout. */
function expectCommandGroup(event: string, group: Group): void {
  expect(Array.isArray(group?.hooks), `${event}: group without hooks[]`).toBe(true);
  for (const entry of group.hooks as HookEntry[]) {
    expect(entry.type, `${event}: hook type must be command`).toBe("command");
    const command = entry.command;
    expect(typeof command, `${event}: hook needs a command`).toBe("string");
    expect(String(command).length, `${event}: command is empty`).toBeGreaterThan(0);
    expect(entry.timeout, `${event}: timeout must be a number`).toBeTypeOf("number");
  }
}

/** Every radar hook runs the bundled entrypoint behind the guard. */
function expectGroupWiring(event: string, group: Group): void {
  for (const entry of group.hooks as HookEntry[]) {
    expect(entry.command, `${event}: runs the bundled radar hook`).toMatch(COMMAND);
  }
}

describe("plugin/hooks/hooks.json, radar's share", () => {
  it("covers every radar event exactly once", () => {
    for (const event of EXPECTED_EVENTS) {
      expect(radarGroups(event), `${event}: radar runs one hook`).toHaveLength(1);
    }
  });

  it("lists matcher groups of command hooks for every event, never bare hook objects", () => {
    for (const [event, groups] of Object.entries(parsed.hooks ?? {})) {
      expect(Array.isArray(groups), `${event}: must be an array of matcher groups`).toBe(true);
      expect(groups.length, `${event}: at least one group`).toBeGreaterThan(0);
      for (const group of groups) expectCommandGroup(event, group);
    }
  });

  it("keeps every radar hook inside a 10 s budget", () => {
    for (const event of EXPECTED_EVENTS) {
      for (const group of radarGroups(event)) {
        for (const entry of group.hooks as HookEntry[]) {
          expect(entry.timeout, `${event}: radar's timeout must be a number <= 10`).toBeLessThanOrEqual(10);
        }
      }
    }
  });

  it("never runs a process per tool call: tool calls come from the transcripts", () => {
    for (const event of TOOL_EVENTS) {
      expect(radarGroups(event), `${event}: radar hooks no tool event`).toHaveLength(0);
    }
  });

  it("runs the launcher's radar word", () => {
    for (const event of EXPECTED_EVENTS) {
      for (const group of radarGroups(event)) expectGroupWiring(event, group);
    }
  });
});
