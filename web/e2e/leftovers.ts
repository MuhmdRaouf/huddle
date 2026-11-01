// leftovers.ts — finds and stops the processes that run from an e2e run folder's bin directory:
// the hubs the setup starts, and anything those hubs start.
// Only node builtins and erasable TypeScript, so a plain `node` runs it in the unit test.

import { execFileSync } from "node:child_process";
import path from "node:path";

/** One process of a run: its pid and its full command line. */
export type RunProcess = { pid: number; command: string };

const sleep = (ms: number): Promise<void> => new Promise((r) => setTimeout(r, ms));

/** The processes among `ps -axo pid=,command=` lines whose executable sits under `<runDir>/bin/`.
 *  Only the first word of the command counts, so an argument that names the folder (the reaper's
 *  own line), a sibling folder, a zombie's `(name)` and the caller's own pid never match. */
export function runProcessesOf(psLines: string[], runDir: string, selfPid: number): RunProcess[] {
  const prefix = path.join(runDir, "bin") + path.sep;
  const found: RunProcess[] = [];
  for (const line of psLines) {
    const match = /^\s*(\d+)\s+(\S.*)$/.exec(line);
    if (match === null) continue;
    const pid = Number(match[1]);
    const command = match[2] ?? "";
    const executable = command.split(/\s+/, 1)[0] ?? "";
    if (pid !== selfPid && executable.startsWith(prefix)) found.push({ pid, command });
  }
  return found;
}

/** The processes running from `<runDir>/bin/` right now, this process excluded. */
export function listRunProcesses(runDir: string): RunProcess[] {
  const out = execFileSync("ps", ["-axo", "pid=,command="], { encoding: "utf8" });
  return runProcessesOf(out.split("\n"), runDir, process.pid);
}

/** Stops every process running from `<runDir>/bin/`: SIGTERM, then SIGKILL for each one still alive
 *  after `graceMs`. Returns what it found. */
export async function stopRunProcesses(runDir: string, graceMs = 5_000): Promise<RunProcess[]> {
  const found = listRunProcesses(runDir);
  for (const { pid } of found) signal(pid, "SIGTERM");
  const deadline = Date.now() + graceMs;
  let pending = found.filter(({ pid }) => alive(pid));
  while (pending.length > 0 && Date.now() < deadline) {
    await sleep(100);
    pending = pending.filter(({ pid }) => alive(pid));
  }
  for (const { pid } of pending) signal(pid, "SIGKILL");
  return found;
}

function signal(pid: number, sig: NodeJS.Signals): void {
  try {
    process.kill(pid, sig);
  } catch {
    // already gone
  }
}

function alive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}
