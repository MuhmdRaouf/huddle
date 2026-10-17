// The installed plugin's surface, plugin/ as Claude Code loads it: one hooks.json carrying every hook the
// three parts registered, commands and skills with unique names, each file running the launcher with the
// right word, and the manifest and marketplace naming the one plugin. hooks.json is load-bearing: Claude
// Code runs it in every session, and a malformed file (hook objects listed directly under an event instead
// of matcher groups) makes hooks silently not run — or worse, a bad entry can break the session. Run with
// `node --test scripts/plugin.test.mjs`.
import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import assert from "node:assert/strict";
import { describe, it } from "node:test";

const root = fileURLToPath(new URL("..", import.meta.url));
const plugin = join(root, "plugin");

const readJSON = (path) => JSON.parse(readFileSync(path, "utf8"));

/** The launcher line one hook runs, with the words after dist/run. */
const runLine = (...words) => `sh "\${CLAUDE_PLUGIN_ROOT}/dist/run" ${words.join(" ")}`;

/** One huddle hook's place in hooks.json: the event it serves, its matcher, name and timeout. */
const HUDDLE_HOOKS = [
  { event: "SessionStart", matcher: "startup|resume|clear|compact", name: "session-start", timeout: 10 },
  { event: "SessionStart", name: "report", timeout: 5 },
  { event: "UserPromptSubmit", name: "listen", timeout: 5 },
  { event: "UserPromptSubmit", name: "report", timeout: 5 },
  { event: "PreToolUse", matcher: "Bash", name: "approve", timeout: 5 },
  { event: "PreToolUse", name: "report", timeout: 5 },
  { event: "PostToolUse", name: "listen", timeout: 5 },
  { event: "PostToolUse", name: "report", timeout: 5 },
  { event: "Notification", name: "report", timeout: 5 },
  { event: "PermissionRequest", name: "permit", timeout: 600 },
  { event: "PermissionRequest", name: "report", timeout: 5 },
  { event: "Stop", name: "stop", timeout: 15 },
  { event: "Stop", name: "report", timeout: 5 },
  { event: "SubagentStop", name: "report", timeout: 5 },
  { event: "SessionEnd", name: "report", timeout: 5 },
];

/** The eight events radar observed in its own plugin. */
const RADAR_EVENTS = [
  "SessionStart",
  "SessionEnd",
  "UserPromptSubmit",
  "SubagentStart",
  "SubagentStop",
  "Stop",
  "PreCompact",
  "Notification",
];

/** Events fired per tool call: neither radar nor kanban hooks them, or every tool call starts a process. */
const TOOL_EVENTS = ["PreToolUse", "PostToolUse", "PostToolUseFailure"];

/** The 15 command files, by part. The open/start/status/stop clash between radar and kanban is resolved by
 *  prefixing kanban's four with its part name; every other name is the one it had. */
const COMMANDS = {
  huddle: ["invite", "join", "setup"],
  radar: ["open", "start", "status", "stop"],
  kanban: ["export", "init", "kanban-open", "kanban-start", "kanban-status", "kanban-stop", "lint", "refresh"],
};

const hooks = readJSON(join(plugin, "hooks", "hooks.json"));

/** Every group's every entry, flattened with its event and group. */
function allEntries() {
  const out = [];
  for (const [event, groups] of Object.entries(hooks.hooks ?? {})) {
    for (const group of groups ?? []) {
      for (const entry of group.hooks ?? []) out.push({ event, group, entry });
    }
  }
  return out;
}

describe("plugin/hooks/hooks.json", () => {
  it("carries every event the three parts registered", () => {
    assert.deepEqual(
      Object.keys(hooks.hooks ?? {}).sort(),
      [...new Set([...HUDDLE_HOOKS.map((hook) => hook.event), ...RADAR_EVENTS])].sort(),
    );
  });

  it("lists matcher groups of command hooks, never bare hook objects, every entry bounded", () => {
    for (const { event, group, entry } of allEntries()) {
      assert.ok(Array.isArray(group.hooks), `${event}: group without hooks[]`);
      assert.equal(entry.type, "command", `${event}: hook type must be command`);
      assert.equal(typeof entry.command, "string", `${event}: hook needs a command`);
      assert.ok(entry.command.length > 0, `${event}: command is empty`);
      assert.equal(typeof entry.timeout, "number", `${event}: timeout must be a number`);
    }
  });

  it("runs huddle's hooks first on every event they serve, with the matchers and timeouts they had", () => {
    const huddleLines = allEntries().filter(({ entry }) => entry.command.startsWith(runLine("hook", "")));
    assert.deepEqual(
      huddleLines.map(({ event, group, entry }) => ({
        event,
        matcher: group.matcher,
        command: entry.command,
        timeout: entry.timeout,
      })),
      HUDDLE_HOOKS.map(({ event, matcher, name, timeout }) => ({
        event,
        matcher,
        command: runLine("hook", name),
        timeout,
      })),
    );
  });

  it("runs radar's hook once on each of its events, within its 5 s budget", () => {
    for (const event of RADAR_EVENTS) {
      const mine = (hooks.hooks?.[event] ?? []).filter((group) =>
        (group.hooks ?? []).some((entry) => entry.command === runLine("radar", "hook")),
      );
      assert.equal(mine.length, 1, `${event}: radar runs one hook`);
      assert.equal(mine[0].matcher, undefined, `${event}: radar's hook names no matcher`);
      assert.equal(mine[0].hooks[0].command, runLine("radar", "hook"));
      assert.equal(mine[0].hooks[0].timeout, 5, `${event}: radar's hook keeps its 5 s timeout`);
    }
  });

  it("runs kanban's session-start on session start, within its 5 s budget", () => {
    const mine = (hooks.hooks?.SessionStart ?? []).filter((group) =>
      (group.hooks ?? []).some((entry) => entry.command.startsWith(runLine("kanban", ""))),
    );
    assert.equal(mine.length, 1, "kanban runs one hook, on SessionStart only");
    assert.equal(mine[0].matcher, "startup|resume|clear|compact");
    assert.equal(mine[0].hooks[0].command, runLine("kanban", "hook", "session-start"));
    assert.equal(mine[0].hooks[0].timeout, 5);
  });

  it("never runs radar or kanban per tool call: tool events come from the transcripts", () => {
    for (const event of TOOL_EVENTS) {
      for (const { entry } of allEntries().filter(({ event: served }) => served === event)) {
        const command = String(entry.command);
        assert.ok(
          !command.startsWith(runLine("radar", "")) && !command.startsWith(runLine("kanban", "")),
          `${event}: ${command} would start a process per tool call`,
        );
      }
    }
  });
});

describe("plugin/commands", () => {
  const files = readdirSync(join(plugin, "commands")).sort();

  it("holds one file per command of the three parts, every name unique", () => {
    const wanted = Object.values(COMMANDS)
      .flat()
      .map((name) => `${name}.md`)
      .sort();
    assert.deepEqual(files, wanted);
    assert.equal(new Set(files).size, files.length, "command names are unique");
  });

  it("huddle's commands drive the plugin's CLI through bin/huddle", () => {
    for (const name of COMMANDS.huddle) {
      const text = readFileSync(join(plugin, "commands", `${name}.md`), "utf8");
      assert.ok(text.startsWith("---\n"), `${name}: front matter first`);
      assert.match(text, /^description: \S/m, `${name}: needs a description`);
      assert.match(text, /^allowed-tools: Bash/m, `${name}: runs Bash`);
      assert.ok(text.includes('"${CLAUDE_PLUGIN_ROOT}/bin/huddle"'), `${name}: runs the plugin's CLI`);
    }
  });

  it("radar's commands run the launcher's radar word", () => {
    const wanted = {
      open: runLine("radar", "open"),
      start: runLine("radar", "start", "$ARGUMENTS"),
      status: runLine("radar", "status"),
      stop: runLine("radar", "stop"),
    };
    for (const [name, line] of Object.entries(wanted)) {
      const text = readFileSync(join(plugin, "commands", `${name}.md`), "utf8");
      assert.ok(text.includes(`!\`${line} 2>&1\``), `${name}: runs ${line}`);
      assert.ok(
        text.includes("Show the output above to the user as it is"),
        `${name}: shows its output as is`,
      );
    }
    assert.ok(
      readFileSync(join(plugin, "commands", "start.md"), "utf8").includes(runLine("radar", "watch")),
      "start's monitor follows the radar word too",
    );
  });

  it("kanban's commands run the launcher's kanban word", () => {
    for (const name of COMMANDS.kanban) {
      const word = name.replace(/^kanban-/, "");
      const text = readFileSync(join(plugin, "commands", `${name}.md`), "utf8");
      assert.ok(
        text.includes(`!\`${runLine("kanban", word, "$ARGUMENTS")} 2>&1\``),
        `${name}: runs the kanban ${word} word`,
      );
      assert.ok(
        text.includes("Show the output above to the user as it is"),
        `${name}: shows its output as is`,
      );
    }
    assert.ok(
      readFileSync(join(plugin, "commands", "init.md"), "utf8").includes(
        "write the first tickets with the kanban skill",
      ),
      "init offers the kanban skill",
    );
  });
});

describe("plugin/skills and plugin/agents", () => {
  it("ships the huddle and kanban skills, their names unique", () => {
    assert.deepEqual(readdirSync(join(plugin, "skills")).sort(), ["huddle", "kanban"]);
    for (const name of ["huddle", "kanban"]) {
      const text = readFileSync(join(plugin, "skills", name, "SKILL.md"), "utf8");
      assert.match(text, /^name: \S/m, `${name}: the skill names itself`);
      assert.match(text, /^description: \S/m, `${name}: the skill describes when to use it`);
    }
  });

  it("ships the kanban skill's references", () => {
    for (const reference of ["flow", "lead", "rundown", "tickets"]) {
      assert.equal(existsSync(join(plugin, "skills", "kanban", "references", `${reference}.md`)), true, `${reference}.md`);
    }
  });

  it("ships the huddle-worker agent", () => {
    assert.match(readFileSync(join(plugin, "agents", "huddle-worker.md"), "utf8"), /^name: huddle-worker/m);
  });
});

describe("plugin/bin", () => {
  it("wraps the launcher's entries, executable", () => {
    const wanted = { huddle: "cli", "huddle-mcp": "mcp", radar: "radar", kanban: "kanban" };
    for (const [name, entry] of Object.entries(wanted)) {
      const text = readFileSync(join(plugin, "bin", name), "utf8");
      assert.ok(text.startsWith("#!/bin/sh\n"), `${name}: a shell script`);
      assert.ok(text.includes(`exec sh "$d/dist/run" ${entry} "$@"`), `${name}: execs the launcher's ${entry} entry`);
      assert.notEqual(statSync(join(plugin, "bin", name)).mode & 0o111, 0, `${name}: needs the exec bit`);
    }
  });
});

describe("the plugin manifest, the marketplace and the committed launcher", () => {
  const manifest = readJSON(join(plugin, ".claude-plugin", "plugin.json"));

  it("names the plugin huddle at version 0.0.1", () => {
    assert.equal(manifest.name, "huddle");
    assert.equal(manifest.version, "0.0.1");
    assert.equal(manifest.author?.name, "Raouf");
    assert.equal(manifest.license, "GPL-3.0-or-later");
  });

  it("describes the hub, radar and kanban, and points at the huddle repository", () => {
    for (const word of ["hub", "radar", "kanban"]) {
      assert.ok(manifest.description?.includes(word), `the description names ${word}`);
    }
    assert.equal(manifest.repository, "https://github.com/MuhmdRaouf/huddle");
    assert.equal(manifest.homepage, "https://github.com/MuhmdRaouf/huddle");
    assert.ok((manifest.keywords ?? []).length > 0, "the manifest carries keywords");
  });

  it("is the one plugin of the huddle marketplace, installed from ./plugin", () => {
    const marketplace = readJSON(join(root, ".claude-plugin", "marketplace.json"));
    assert.equal(marketplace.name, "huddle");
    assert.equal(marketplace.owner?.name, "Raouf");
    assert.deepEqual(
      (marketplace.plugins ?? []).map((entry) => ({
        name: entry.name,
        source: entry.source,
        version: entry.version,
      })),
      [{ name: "huddle", source: "./plugin", version: manifest.version }],
    );
  });

  it("starts the MCP bridge through the plugin's bin/huddle-mcp", () => {
    const server = readJSON(join(plugin, ".mcp.json"))?.mcpServers?.huddle;
    assert.equal(server?.command, "sh");
    assert.deepEqual(server?.args, ["${CLAUDE_PLUGIN_ROOT}/bin/huddle-mcp"]);
  });

  it("commits the launcher every hook and command runs, and the licence", () => {
    assert.equal(statSync(join(plugin, "dist", "run")).isFile(), true);
    const run = readFileSync(join(plugin, "dist", "run"), "utf8");
    assert.ok(run.startsWith("#!/bin/sh\n"), "the launcher is a shell script");
    assert.ok(
      run.includes("base=${HUDDLE_DOWNLOAD_BASE:-https://github.com/MuhmdRaouf/huddle/releases/download}") &&
        run.includes("url=$base/v0.0.1/huddle-$os-$arch"),
      "the launcher pins the release asset",
    );
    assert.ok(readFileSync(join(plugin, "LICENSE"), "utf8").includes("GNU GENERAL PUBLIC LICENSE"));
  });
});
