import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vite-plus/test";

/**
 * The installed plugin's kanban share. hooks.json runs in every session of every project: a malformed file
 * (hook objects listed directly under an event instead of matcher groups) makes the hook silently not run.
 * The command files and the bin wrapper name the launcher's entry words; a wrong word prints an
 * unknown-command error instead of the command's output, and the marketplace entry is what Claude Code
 * installs from. The three parts share the one plugin, so the launcher, the manifest and the marketplace
 * are huddle's; these cases hold kanban's lines in them.
 */
const root = fileURLToPath(new URL("../../..", import.meta.url));
const plugin = `${root}/plugin`;

type HookEntry = { type?: unknown; command?: unknown; timeout?: unknown };
type Group = { matcher?: unknown; hooks?: unknown };

const parsed = JSON.parse(readFileSync(`${plugin}/hooks/hooks.json`, "utf8")) as {
  hooks?: Record<string, Group[]>;
};

/** The launcher line one command file runs, with the part word and the command's own word. */
const runLine = (name: string) => `!\`sh "\${CLAUDE_PLUGIN_ROOT}/dist/run" kanban ${name} $ARGUMENTS 2>&1\``;

/** The eight commands kanban installs; the open/start/status/stop clash with radar's is resolved in
 *  kanban's favour of its own part name. */
const COMMANDS = [
  "export",
  "init",
  "kanban-open",
  "kanban-start",
  "kanban-status",
  "kanban-stop",
  "lint",
  "refresh",
] as const;

/** Every command body ends with the same instruction about its output. */
const SHOW_AS_IS = "Show the output above to the user as it is, with no preamble or recap.";

describe("plugin/hooks/hooks.json, kanban's share", () => {
  /** The groups of one event whose command runs the launcher's kanban word. */
  const kanbanGroups = (event: string): Group[] =>
    (parsed.hooks?.[event] ?? []).filter((group) =>
      ((group?.hooks ?? []) as HookEntry[]).some((entry) =>
        String(entry.command).startsWith(`sh "\${CLAUDE_PLUGIN_ROOT}/dist/run" kanban `),
      ),
    );

  it("runs one hook, on SessionStart", () => {
    const groups = kanbanGroups("SessionStart");
    expect(groups).toHaveLength(1);
    expect(groups[0]?.matcher).toBe("startup|resume|clear|compact");
    const entries = groups[0]?.hooks as HookEntry[] | undefined;
    expect(entries).toHaveLength(1);
    expect(entries?.[0]?.type).toBe("command");
    expect(entries?.[0]?.command).toBe(`sh "\${CLAUDE_PLUGIN_ROOT}/dist/run" kanban hook session-start`);
    expect(entries?.[0]?.timeout).toBe(5);
  });

  it("hooks no other event", () => {
    for (const event of Object.keys(parsed.hooks ?? {})) {
      if (event === "SessionStart") continue;
      expect(kanbanGroups(event), `${event}: kanban hooks SessionStart only`).toHaveLength(0);
    }
  });
});

describe("plugin/commands, kanban's share", () => {
  it("holds exactly kanban's eight commands among the plugin's fifteen", () => {
    const files = readdirSync(`${plugin}/commands`).sort();
    for (const name of COMMANDS) {
      expect(files).toContain(`${name}.md`);
    }
    expect(files.filter((file) => file === "kanban-open.md" || file.startsWith("kanban-"))).toHaveLength(4);
    expect(new Set(files).size, "command names are unique").toBe(files.length);
  });

  it.each(COMMANDS)("runs %s through the launcher's kanban word and shows its output as is", (name) => {
    const word = name.replace(/^kanban-/, "");
    const text = readFileSync(`${plugin}/commands/${name}.md`, "utf8");
    expect(text.startsWith("---\n"), `${name}: front matter first`).toBe(true);
    expect(text, `${name}: needs a description`).toMatch(/^description: \S/m);
    expect(text, `${name}: runs Bash only`).toMatch(/^allowed-tools: Bash$/m);
    expect(text).toContain(runLine(word));
    expect(text).toContain(SHOW_AS_IS);
  });

  it("offers the kanban skill after init", () => {
    expect(readFileSync(`${plugin}/commands/init.md`, "utf8")).toContain(
      "write the first tickets with the kanban skill",
    );
  });

  it("tells export where to write and how to publish", () => {
    const text = readFileSync(`${plugin}/commands/export.md`, "utf8");
    expect(text).toContain("--out");
    expect(text).toContain("Artifact tool");
  });
});

describe("plugin/bin/kanban", () => {
  it("is an executable shell wrapper over the launcher's kanban word", () => {
    const text = readFileSync(`${plugin}/bin/kanban`, "utf8");
    expect(text.startsWith("#!/bin/sh\n")).toBe(true);
    expect(text).toContain(`exec sh "$d/dist/run" kanban "$@"`);
    expect(statSync(`${plugin}/bin/kanban`).mode & 0o111, "the wrapper needs the exec bit").not.toBe(0);
  });
});

describe("the kanban skill and the plugin that ships it", () => {
  it("ships the kanban skill with its references", () => {
    expect(existsSync(`${plugin}/skills/kanban/SKILL.md`)).toBe(true);
    expect(readFileSync(`${plugin}/skills/kanban/SKILL.md`, "utf8")).toContain("name: kanban");
    for (const reference of ["flow", "lead", "rundown", "tickets"]) {
      expect(existsSync(`${plugin}/skills/kanban/references/${reference}.md`), `${reference}.md`).toBe(true);
    }
  });

  it("names the plugin huddle at the repository's version, kanban's commands inside", () => {
    const manifest = JSON.parse(readFileSync(`${plugin}/.claude-plugin/plugin.json`, "utf8")) as {
      name?: string;
      version?: string;
      description?: string;
      author?: { name?: string };
    };
    expect(manifest.name).toBe("huddle");
    expect(manifest.version).toBe(readFileSync(`${root}/VERSION`, "utf8").trim());
    expect(manifest.description ?? "").toContain("kanban");
    expect(manifest.author?.name).toBe("Raouf");
  });

  it("is the marketplace's one plugin, installed from ./plugin", () => {
    const marketplace = JSON.parse(readFileSync(`${root}/.claude-plugin/marketplace.json`, "utf8")) as {
      name?: string;
      plugins?: { name?: string; source?: string; version?: string }[];
    };
    expect(marketplace.name).toBe("huddle");
    const manifest = JSON.parse(readFileSync(`${plugin}/.claude-plugin/plugin.json`, "utf8")) as {
      version?: string;
    };
    expect(marketplace.plugins).toEqual([
      expect.objectContaining({ name: "huddle", source: "./plugin", version: manifest.version }),
    ]);
  });

  it("commits the launcher the hook and every command run", () => {
    expect(statSync(`${plugin}/dist/run`).isFile()).toBe(true);
    expect(readFileSync(`${plugin}/dist/run`, "utf8")).toContain("kanban) entry=kanban ;;");
  });
});
