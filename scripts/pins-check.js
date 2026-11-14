// Fails when a tool pin is a range, a branch or latest, or when a workflow, the Dockerfile or a hook names
// another version than mise.toml and package.json. PINS_ROOT=<dir> checks another tree.
// Run: bun scripts/pins-check.js   (mise run pins:check, and the pins job in CI)
const fs = require("fs");
const path = require("path");
const root = process.env.PINS_ROOT || ".";
const read = (f) => fs.readFileSync(path.join(root, f), "utf8");
const problems = [];
const exact = /^\d+\.\d+\.\d+$/;
const need = (ok, message) => { if (!ok) problems.push(message); };

const mise = read("mise.toml");
const tool = (name) => {
  const m = new RegExp(`^${name}\\s*=\\s*(?:"([^"]*)"|\\{[^}]*version\\s*=\\s*"([^"]*)")`, "m").exec(mise);
  return m ? m[1] || m[2] : undefined;
};
const toolsBlock = mise.slice(mise.indexOf("[tools]"), mise.indexOf("\n[tasks"));
for (const m of toolsBlock.matchAll(/^([\w-]+)\s*=\s*(?:"([^"]*)"|\{[^}]*version\s*=\s*"([^"]*)")/gm))
  need(exact.test(m[2] || m[3]), `mise.toml: ${m[1]} is "${m[2] || m[3]}", not an exact version`);

const pkg = JSON.parse(read("package.json"));
for (const [name, version] of Object.entries({ ...pkg.dependencies, ...pkg.devDependencies }))
  need(exact.test(version), `package.json: ${name} is "${version}", not an exact version`);

const workflows = fs.readdirSync(path.join(root, ".github/workflows")).filter((f) => /\.ya?ml$/.test(f));
const files = { Dockerfile: read("Dockerfile"), ".pre-commit-config.yaml": read(".pre-commit-config.yaml") };
for (const w of workflows) files[`.github/workflows/${w}`] = read(`.github/workflows/${w}`);

// Every action is a full commit sha with its release tag in a comment.
for (const [file, text] of Object.entries(files))
  for (const m of text.matchAll(/^\s*-?\s*uses:\s*(\S+)(.*)$/gm))
    if (!m[1].startsWith("./"))
      need(/^[\w.-]+\/[\w./-]+@[0-9a-f]{40}$/.test(m[1]) && /#\s*v?\d+(\.\d+)*/.test(m[2]), `${file}: action ${m[1]} is not pinned to a sha with a release tag comment`);

// One version everywhere a tool is named.
const agree = (label, want, file, regex) => {
  const found = [...files[file].matchAll(regex)].map((m) => m[1]);
  need(found.length > 0, `${file}: no ${label} pin found`);
  for (const v of found) need(v === want, `${file}: ${label} is ${v}, mise.toml and package.json say ${want}`);
};
const bun = tool("bun"), py = tool("python"), pc = tool("pre-commit"), gl = tool("gitleaks"), rust = tool("rust");
const docker = "Dockerfile", hooks = ".pre-commit-config.yaml";
agree("bun", bun, docker, /^ARG BUN_VERSION=(\S+)/gm);
agree("pre-commit", pc, docker, /pre-commit==(\S+?)(?:\s|\|$)/gm);
agree("gitleaks", gl, docker, /gitleaks\/releases\/download\/v([\d.]+)\//g);
agree("gitleaks", gl, hooks, /gitleaks\/gitleaks\n\s*rev: v([\d.]+)/g);
agree("rust", rust, docker, /^FROM rust:([\d.]+)-/gm);
const al = (/rhysd\/actionlint\n\s*rev: v([\d.]+)/.exec(files[hooks]) || [])[1];
agree("actionlint", al, docker, /actionlint\/releases\/download\/v([\d.]+)\//g);
// Each tool a workflow can pin must be named in at least one workflow, and every mention must agree.
const unquote = (v) => v.replace(/^["']|["']$/g, "");
for (const [label, want, regex] of [
  ["bun", bun, /bun-version:\s*(\S+)/g],
  ["python", py, /python-version:\s*(\S+)/g],
  ["pre-commit", pc, /pre-commit==(\S+)/g],
  ["gitleaks", gl, /gitleaks\/releases\/download\/v([\d.]+)\//g],
]) {
  const found = workflows.flatMap((w) =>
    [...files[`.github/workflows/${w}`].matchAll(regex)].map((m) => [`.github/workflows/${w}`, unquote(m[1])]));
  need(found.length > 0, `.github/workflows: no workflow pins ${label}`);
  for (const [file, v] of found) need(v === want, `${file}: ${label} is ${v}, mise.toml and package.json say ${want}`);
}
const cc = [...Object.values(files).join("\n").matchAll(/@anthropic-ai\/claude-code@(\S+)/g)].map((m) => m[1]);
need(cc.length > 0 && cc.every((v) => exact.test(v) && v === cc[0]), `claude-code is not one exact version: ${cc.join(", ")}`);
for (const m of files[docker].matchAll(/cargo install --locked \S+@(\S+)/g)) need(exact.test(m[1]), `Dockerfile: cargo install pin ${m[1]} is not exact`);
need(exact.test((/^ARG ZIG_VERSION=(\S+)/m.exec(files[docker]) || [])[1] || ""), "Dockerfile: ZIG_VERSION is not exact");
for (const [file, text] of Object.entries(files))
  need(!/\blatest\b/.test(text.replace(/^\s*#.*$/gm, "")), `${file}: the word latest appears outside a comment`);

if (problems.length) { console.error(problems.join("\n")); process.exit(1); }
console.log("pins ok");
