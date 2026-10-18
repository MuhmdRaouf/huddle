// global-teardown.ts — stops the two processes the global setup started (SIGTERM, then SIGKILL
// after five seconds each) and removes the run folder with the temp homes in it.

import fs from "node:fs";
import path from "node:path";
import { runDir } from "./paths.ts";

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
  for (const stateName of ["server.json", "server-hub.json"]) {
    const pid = serverPid(stateName);
    if (pid !== null) await stop(pid);
  }
  fs.rmSync(runDir, { recursive: true, force: true });
}
