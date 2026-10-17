import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vite-plus/test";

/**
 * The merged plugin's hooks.json is load-bearing: Claude Code runs it in every session, and a malformed
 * file (hook objects listed directly under an event instead of matcher groups) makes hooks silently not
 * run — or worse, a bad entry can break the session. One line per event runs one process for all three
 * parts, radar's among them; these are radar's cases over its share.
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

/** The event's word in the launcher line: SessionStart -> session-start. */
const eventWord = (event: string): string => event.replaceAll(/([a-z0-9])([A-Z])/g, "$1-$2").toLowerCase();

/** The one command the event line runs: the committed launcher's hook word naming the event, which
 *  starts the huddle binary (downloading it on first use) and never blocks or fails Claude Code.
 *  Radar's hook is one part of that event's process. */
const eventLine = (event: string): RegExp =>
  new RegExp(`^sh "\\$\\{CLAUDE_PLUGIN_ROOT\\}/dist/run" hook ${eventWord(event)}$`);

/** The groups of one event whose command is the event's launcher line. */
const eventGroups = (event: string): Group[] =>
  (parsed.hooks?.[event] ?? []).filter((group) =>
    ((group?.hooks ?? []) as HookEntry[]).some((entry) => eventLine(event).test(String(entry.command))),
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

describe("plugin/hooks/hooks.json, radar's share", () => {
  it("covers every radar event exactly once", () => {
    for (const event of EXPECTED_EVENTS) {
      expect(eventGroups(event), `${event}: one line names the event`).toHaveLength(1);
    }
  });

  it("lists matcher groups of command hooks for every event, never bare hook objects", () => {
    for (const [event, groups] of Object.entries(parsed.hooks ?? {})) {
      expect(Array.isArray(groups), `${event}: must be an array of matcher groups`).toBe(true);
      expect(groups.length, `${event}: at least one group`).toBeGreaterThan(0);
      for (const group of groups) expectCommandGroup(event, group);
    }
  });

  it("keeps every event line inside a 15 s budget", () => {
    for (const event of EXPECTED_EVENTS) {
      for (const group of eventGroups(event)) {
        for (const entry of group.hooks as HookEntry[]) {
          expect(entry.timeout, `${event}: the event's timeout must be a number <= 15`).toBeLessThanOrEqual(
            15,
          );
        }
      }
    }
  });

  it("runs the launcher's hook word naming the event", () => {
    for (const event of EXPECTED_EVENTS) {
      for (const group of eventGroups(event)) {
        for (const entry of group.hooks as HookEntry[]) {
          expect(entry.command, `${event}: the event's launcher line`).toMatch(eventLine(event));
        }
      }
    }
  });
});
