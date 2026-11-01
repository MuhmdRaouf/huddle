#!/usr/bin/env node
// Refuses while the last completed main CI run is not green: no deploy lands on a red main.
// It reads the run through `gh run list`, a read, and fails closed: a missing gh, no network, no completed run
// or an answer that is not a run each end in one line saying why and exit 1.
// Usage: node scripts/ci-green.mjs   (from any directory; gh reads the repository of this script)
import { spawnSync } from "node:child_process";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const REPO = join(dirname(fileURLToPath(import.meta.url)), "..");
const GH_ARGS = [
  "run",
  "list",
  "--branch",
  "main",
  "--workflow",
  "ci.yml",
  "--status",
  "completed",
  "-L",
  "1",
  "--json",
  "conclusion,databaseId,headSha",
];
const GH_TIMEOUT_MS = 30_000;

/** Prints the reason on one line and exits 1. */
function refuse(reason) {
  console.error(`ci-green: ${reason}`);
  process.exit(1);
}

const gh = spawnSync("gh", GH_ARGS, { cwd: REPO, encoding: "utf8", timeout: GH_TIMEOUT_MS });
if (gh.error?.code === "ENOENT") refuse("gh is not installed, so the last main CI run cannot be read");
if (gh.error?.code === "ETIMEDOUT") refuse(`gh did not answer within ${GH_TIMEOUT_MS / 1000} s, so the last main CI run cannot be read`);
if (gh.error) refuse(`gh did not run (${gh.error.message}), so the last main CI run cannot be read`);
if (gh.status !== 0) {
  const why = gh.stderr.trim().split("\n")[0] || `exit ${gh.status}`;
  refuse(`gh could not read the last main CI run: ${why}`);
}

let runs;
try {
  runs = JSON.parse(gh.stdout);
} catch {
  refuse("gh answered something that is not JSON, so the last main CI run cannot be read");
}
if (!Array.isArray(runs) || runs.length === 0) refuse("main has no completed ci.yml run to read");

const [last] = runs;
if (typeof last?.databaseId !== "number" || typeof last.conclusion !== "string" || typeof last.headSha !== "string") {
  refuse("gh answered a run without an id, a conclusion and a commit, so the last main CI run cannot be read");
}

const which = `run ${last.databaseId} on ${last.headSha.slice(0, 8)}`;
if (last.conclusion !== "success") {
  refuse(`the last main CI ${which} ended ${last.conclusion || "with no conclusion"}; nothing deploys while main is red`);
}
console.log(`ci-green: the last main CI ${which} passed`);
