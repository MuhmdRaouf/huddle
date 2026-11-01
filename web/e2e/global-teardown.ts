// global-teardown.ts — stops the reaper and the two hubs the global setup started (SIGTERM, then
// SIGKILL after five seconds each), then every other process still running from the run's bin
// folder, a hub or anything it started. A process that survives fails the run. It then removes the run folder with the temp homes in it, and the project dir the setup made
// outside the repository.

import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { listRunProcesses, stopRunProcesses } from "./leftovers.ts";
import { outsideProjectPath, runDir } from "./paths.ts";

const sleep = (ms: number): Promise<void> => new Promise((r) => setTimeout(r, ms));

/** One server's pid, from the state file the global setup wrote; null when it never got
 *  there. */
function serverPid(stateName: string): number | null {
  try {
    const saved = JSON.parse(fs.readFileSync(path.join(runDir, stateName), "utf8")) as {
      pid?: number;
    };
    return typeof saved.pid === "number" ? saved.pid : null;
  } catch {
    return null;
  }
}

/** Stops the reaper the setup started, so it cannot outlive this run and meet the next one's
 *  processes. The pid is signalled only when its command line is the reaper script. */
function stopReaper(): void {
  try {
    const saved = JSON.parse(fs.readFileSync(path.join(runDir, "reaper.json"), "utf8")) as {
      pid?: number;
    };
    if (typeof saved.pid !== "number") return;
    const command = execFileSync("ps", ["-o", "command=", "-p", String(saved.pid)], { encoding: "utf8" });
    if (command.includes("reaper.sh")) process.kill(saved.pid, "SIGTERM");
  } catch {
    // the setup never got that far, or the reaper is gone
  }
}

/** Stops one pid: SIGTERM, then SIGKILL once five seconds pass without an exit. */
async function stop(pid: number): Promise<void> {
  try {
    process.kill(pid, "SIGTERM");
  } catch {
    // already gone
    return;
  }
  const deadline = Date.now() + 5_000;
  for (;;) {
    try {
      process.kill(pid, 0);
    } catch {
      return; // exited
    }
    if (Date.now() > deadline) {
      try {
        process.kill(pid, "SIGKILL");
      } catch {
        // gone between the check and the kill
      }
      return;
    }
    await sleep(100);
  }
}

export default async function globalTeardown(): Promise<void> {
  stopReaper();
  for (const stateName of ["server.json", "server-hub.json"]) {
    const pid = serverPid(stateName);
    if (pid !== null) await stop(pid);
  }
  await stopRunProcesses(runDir);
  const survivors = listRunProcesses(runDir);
  try {
    const project = fs.readFileSync(outsideProjectPath, "utf8").trim();
    if (path.basename(project).startsWith("huddle-e2e-project-")) {
      fs.rmSync(project, { recursive: true, force: true });
    }
  } catch {
    // the setup never got that far
  }
  fs.rmSync(runDir, { recursive: true, force: true });
  if (survivors.length > 0) {
    throw new Error(
      `processes still run from the e2e run folder: ${survivors.map((p) => `${p.pid} ${p.command}`).join("; ")}`,
    );
  }
}
