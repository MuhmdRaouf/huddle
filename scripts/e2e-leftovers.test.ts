// The e2e harness stops every process under its run folder: the pure matcher over `ps` lines, the stop
// that ends a detached process, and the reaper script that does the same after the runner is killed.
// Run with `node --test scripts/e2e-leftovers.test.ts`.
import { spawn, type ChildProcess } from "node:child_process";
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { after, test } from "node:test";
import { fileURLToPath } from "node:url";
import { runProcessesOf, stopRunProcesses } from "../web/e2e/leftovers.ts";

const reaper = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "web", "e2e", "reaper.sh");

const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "e2e-leftovers-"));
const runDir = path.join(tmp, "run");
const binDir = path.join(runDir, "bin");
fs.mkdirSync(binDir, { recursive: true });
// A multicall binary such as uutils coreutils (Ubuntu 26.04) refuses to run under a name it does not know, so the
// stand-in for a hub is node itself: it runs under any name and shows that name as its command.
fs.symlinkSync(process.execPath, path.join(binDir, "sleeper"));
const sleeperArgs = ["-e", "setTimeout(() => {}, 300_000)"];

const started: ChildProcess[] = [];

/** A process the test owns: killed in the after hook when it is still alive. */
function launch(command: string, args: string[], detached: boolean): ChildProcess {
  const child = spawn(command, args, { detached, stdio: "ignore" });
  child.unref();
  started.push(child);
  return child;
}

function alive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

const sleep = (ms: number): Promise<void> => new Promise((r) => setTimeout(r, ms));

after(() => {
  for (const child of started) {
    if (child.pid !== undefined && alive(child.pid)) child.kill("SIGKILL");
  }
  fs.rmSync(tmp, { recursive: true, force: true });
});

test("a run's processes are the executables under its bin folder, and nothing else", () => {
  const run = "/work/.agents/scratch/e2e/run";
  const lines = [
    `  101 ${run}/bin/huddle serve`,
    `  102 ${run}/bin/huddle router serve --takeover`,
    `31337 ${run}/bin/huddle serve`,
    `  103 /work/.agents/scratch/e2e/run2/bin/huddle serve`,
    `  104 /bin/sh /x/web/e2e/reaper.sh 123 ${run}/bin`,
    `  105 (huddle)`,
    `  106 /usr/bin/sleep 300`,
    `  107 /usr/bin/grep ${run}/bin`,
    `  108 [huddle] <defunct>`,
  ];
  const found = runProcessesOf(lines, run, 31337);
  assert.deepEqual(
    found.map((p) => p.pid),
    [101, 102],
  );
  assert.equal(found[1]?.command, `${run}/bin/huddle router serve --takeover`);
});

test("stopping a run's processes ends a detached one under its bin folder", async () => {
  const sleeper = launch(path.join(binDir, "sleeper"), sleeperArgs, true);
  const pid = sleeper.pid;
  assert.ok(pid !== undefined);
  assert.ok(alive(pid));
  const stopped = await stopRunProcesses(runDir);
  assert.ok(stopped.some((p) => p.pid === pid));
  assert.equal(alive(pid), false);
});

test("the reaper ends a killed run's processes", async () => {
  const runner = launch("/bin/sleep", ["300"], false);
  const runnerPid = runner.pid;
  assert.ok(runnerPid !== undefined);
  launch("/bin/sh", [reaper, String(runnerPid), binDir], true);
  const sleeper = launch(path.join(binDir, "sleeper"), sleeperArgs, true);
  const pid = sleeper.pid;
  assert.ok(pid !== undefined);
  assert.ok(alive(pid));

  runner.kill("SIGKILL");
  const deadline = Date.now() + 15_000;
  while (alive(pid) && Date.now() < deadline) await sleep(200);
  assert.equal(alive(pid), false);
});
