// The installed plugin's surface, plugin/ as Claude Code loads it: one hooks.json running one
// hook process per event, commands and skills with unique names, each file running the launcher
// with the right word, and the manifest and marketplace naming the one plugin. hooks.json is
// load-bearing: Claude Code runs it in every session, and a malformed file (hook objects listed
// directly under an event instead of matcher groups) makes hooks silently not run — or worse, a
// bad entry can break the session. Run with `node --test scripts/plugin.test.mjs`.
import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { PROVIDERS } from "./launcher.mjs";

const root = fileURLToPath(new URL("..", import.meta.url));
const plugin = join(root, "plugin");

const readJSON = (path) => JSON.parse(readFileSync(path, "utf8"));

/** The launcher line one hook runs, with the words after dist/run. */
const runLine = (...words) => `sh "\${CLAUDE_PLUGIN_ROOT}/dist/run" ${words.join(" ")}`;

/** One hooks.json line: the event it serves, its matcher, the hook word and the timeout. One line
 *  per event — the launcher's hook word names the event and one process runs every part of it.
 *  PreToolUse keeps `hook approve` for Bash and PermissionRequest keeps `hook permit` as their
 *  own lines: the two hooks that answer Claude Code, not only observe it. Each event line keeps
 *  the largest timeout its parts had, each decision line its own. */
const HOOK_LINES = [
  { event: "SessionStart", matcher: "startup|resume|clear|compact", name: "session-start", timeout: 15 },
  { event: "UserPromptSubmit", name: "user-prompt-submit", timeout: 5 },
  { event: "PreToolUse", matcher: "Bash", name: "approve", timeout: 5 },
  { event: "PreToolUse", name: "pre-tool-use", timeout: 5 },
  { event: "PostToolUse", name: "post-tool-use", timeout: 5 },
  { event: "Notification", name: "notification", timeout: 5 },
  { event: "PermissionRequest", name: "permit", timeout: 600 },
  { event: "PermissionRequest", name: "permission-request", timeout: 5 },
  { event: "Stop", name: "stop", timeout: 15 },
  { event: "SubagentStart", name: "subagent-start", timeout: 5 },
  { event: "SubagentStop", name: "subagent-stop", timeout: 7 },
  { event: "SessionEnd", name: "session-end", timeout: 5 },
  { event: "PreCompact", name: "pre-compact", timeout: 5 },
  { event: "TaskCreated", name: "task-created", timeout: 5 },
  { event: "TeammateIdle", name: "teammate-idle", timeout: 5 },
];

/** The twelve command files, by part. Hooks start the servers, so radar's start/stop/status/open and
 *  kanban's four open/start/status/stop are gone; open, status and stop cover the hub, radar and
 *  the board server together, domain names the hub behind Caddy, and kanban's own words carry the
 *  kanban- prefix. */
const COMMANDS = {
  huddle: ["invite", "join", "setup", "statusline"],
  hub: ["domain", "open", "status", "stop"],
  kanban: ["kanban-export", "kanban-init", "kanban-lint", "kanban-refresh"],
};

/** The one-line description each hub command's front matter carries. */
const HUB_DESCRIPTIONS = {
  domain: "Name the hub for a Caddy reverse proxy and print the site block; off goes back to localhost only",
  open: "Ensure the hub is running and print its dashboard URL (Hub, Radar and Kanban tabs)",
  status: "One table: hub, radar, gateway; running or not, address",
  stop: "Stop the hub; radar and the boards are served by it",
};

/** The launcher lines one hub command runs, in order. */
const HUB_LINES = {
  domain: [runLine("domain", "$ARGUMENTS")],
  open: [runLine("up"), runLine("open", "--print")],
  status: [runLine("status"), runLine("radar", "status"), runLine("kanban", "status"), runLine("gateway", "status")],
  stop: [runLine("down"), runLine("radar", "stop"), runLine("kanban", "stop", "$ARGUMENTS")],
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
  it("carries every event a session fires", () => {
    assert.deepEqual(
      Object.keys(hooks.hooks ?? {}).sort(),
      [...new Set(HOOK_LINES.map((hook) => hook.event))].sort(),
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

  it("runs one launcher line per event, with the old matchers and the largest old timeout", () => {
    const lines = allEntries().map(({ event, group, entry }) => ({
      event,
      matcher: group.matcher,
      command: entry.command,
      timeout: entry.timeout,
    }));
    assert.deepEqual(
      lines,
      HOOK_LINES.map(({ event, matcher, name, timeout }) => ({
        event,
        matcher,
        command: runLine("hook", name),
        timeout,
      })),
    );
  });

  it("keeps every non-decision line inside a 15 s budget", () => {
    for (const { event, entry } of allEntries()) {
      if (["approve", "permit"].some((name) => entry.command.endsWith(`hook ${name}`))) continue;
      assert.ok(entry.timeout <= 15, `${event}: timeout ${entry.timeout} exceeds 15 s`);
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

  it("statusline installs, removes and samples the status bar line", () => {
    const text = readFileSync(join(plugin, "commands", "statusline.md"), "utf8");
    assert.ok(text.startsWith("---\n"), "statusline: front matter first");
    assert.match(text, /^description: \S/m, "statusline: needs a description");
    assert.match(text, /^allowed-tools: Bash$/m, "statusline: runs Bash only");
    assert.ok(
      text.includes('"${CLAUDE_PLUGIN_ROOT}/bin/huddle"'),
      "statusline: runs the plugin's CLI",
    );
    for (const word of ["--install", "--remove"]) {
      assert.ok(text.includes(`huddle statusline ${word}`), `statusline: runs ${word}`);
    }
    assert.ok(text.includes("| huddle statusline"), "statusline: samples a render");
  });

  it("each hub command names in one line what it does for the user", () => {
    for (const name of COMMANDS.hub) {
      const text = readFileSync(join(plugin, "commands", `${name}.md`), "utf8");
      assert.ok(text.startsWith("---\n"), `${name}: front matter first`);
      assert.ok(
        text.includes(`description: ${HUB_DESCRIPTIONS[name]}`),
        `${name}: its description must be "${HUB_DESCRIPTIONS[name]}"`,
      );
      assert.match(text, /^allowed-tools: Bash$/m, `${name}: runs Bash only`);
    }
  });

  it("open ensures the hub and prints its dashboard URL", () => {
    const text = readFileSync(join(plugin, "commands", "open.md"), "utf8");
    for (const line of HUB_LINES.open) {
      assert.ok(text.includes(`!\`${line} 2>&1\``), `open runs ${line}`);
    }
    assert.ok(
      text.includes("Show the output above to the user as it is"),
      "open shows its output as is",
    );
  });

  it("domain passes its arguments to the domain word and shows the block as is", () => {
    const text = readFileSync(join(plugin, "commands", "domain.md"), "utf8");
    for (const line of HUB_LINES.domain) {
      assert.ok(text.includes(`!\`${line} 2>&1\``), `domain runs ${line}`);
    }
    assert.match(text, /^argument-hint: \S/m, "domain: names its arguments");
    assert.ok(
      text.includes("Show the output above to the user as it is"),
      "domain shows its output as is",
    );
  });

  it("status gathers hub, radar, the board server and the gateway into one table", () => {
    const text = readFileSync(join(plugin, "commands", "status.md"), "utf8");
    for (const line of HUB_LINES.status) {
      assert.ok(text.includes(`!\`${line} 2>&1\``), `status runs ${line}`);
    }
    assert.ok(
      text.includes("Show the output above to the user as it is"),
      "status shows its output as is",
    );
  });

  it("stop stops the hub, radar and the board server", () => {
    const text = readFileSync(join(plugin, "commands", "stop.md"), "utf8");
    for (const line of HUB_LINES.stop) {
      assert.ok(text.includes(`!\`${line} 2>&1\``), `stop runs ${line}`);
    }
    assert.ok(
      text.includes("Show the output above to the user as it is"),
      "stop shows its output as is",
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
      readFileSync(join(plugin, "commands", "kanban-init.md"), "utf8").includes(
        "write the first tickets with the kanban skill",
      ),
      "kanban-init offers the kanban skill",
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

  it("names only commands the plugin ships", () => {
    const shipped = new Set(Object.values(COMMANDS).flat());
    for (const name of ["huddle", "kanban"]) {
      const text = readFileSync(join(plugin, "skills", name, "SKILL.md"), "utf8");
      for (const match of text.matchAll(/\/huddle:([a-z-]+)/g)) {
        assert.ok(shipped.has(match[1]), `${name}: names /huddle:${match[1]}, which the plugin does not ship`);
      }
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

  it("names the plugin huddle, carrying no version: an unchanged version would stop updates", () => {
    assert.equal(manifest.name, "huddle");
    assert.equal(manifest.version, undefined);
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

  it("is the huddle plugin of the muhmdraouf marketplace, installed from ./plugin beside the five providers, none of them versioned", () => {
    const marketplace = readJSON(join(root, ".claude-plugin", "marketplace.json"));
    assert.equal(marketplace.name, "muhmdraouf");
    assert.equal(marketplace.owner?.name, "Raouf");
    const entries = (marketplace.plugins ?? []).map((entry) => ({
      name: entry.name,
      source: entry.source,
      dependencies: entry.dependencies,
    }));
    assert.equal(entries.length, 6, "the marketplace lists huddle and the five providers");
    for (const entry of marketplace.plugins ?? []) {
      assert.equal(entry.version, undefined, `${entry.name}: an unchanged version would stop updates`);
    }
    assert.deepEqual(
      entries.filter((entry) => entry.name === "huddle"),
      [{ name: "huddle", source: "./plugin", dependencies: undefined }],
    );
    for (const provider of PROVIDERS) {
      const manifestOf = readJSON(join(root, "plugins", `${provider}-plugin-cc`, ".claude-plugin", "plugin.json"));
      assert.equal(manifestOf.version, undefined, `${provider}: an unchanged version would stop updates`);
      assert.deepEqual(
        entries.filter((entry) => entry.name === `${provider}-plugin-cc`),
        [
          {
            name: `${provider}-plugin-cc`,
            source: `./plugins/${provider}-plugin-cc`,
            dependencies: ["huddle"],
          },
        ],
      );
    }
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
