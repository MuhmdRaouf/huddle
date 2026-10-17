// global-teardown.ts — stops the one process the global setup started (SIGTERM, then SIGKILL
// after five seconds) and removes the run folder with the temp homes in it.

import fs from "node:fs";
import path from "node:path";
import { runDir } from "./paths.ts";

const sleep = (ms: number): Promise<void> => new Promise((r) => setTimeout(r, ms));

/** The server's pid, from the file the global setup wrote; null when it never got there. */
function serverPid(): number | null {
  try {
    const saved = JSON.parse(fs.readFileSync(path.join(runDir, "server.json"), "utf8")) as {
      pid?: number;
    };
    return typeof saved.pid === "number" ? saved.pid : null;
  } catch {
    return null;
  }
}

export default async function globalTeardown(): Promise<void> {
  const pid = serverPid();
  if (pid !== null) {
    try {
      process.kill(pid, "SIGTERM");
    } catch {
      // already gone
    }
    const deadline = Date.now() + 5_000;
    for (;;) {
      try {
        process.kill(pid, 0);
      } catch {
        break; // exited
      }
      if (Date.now() > deadline) {
        try {
          process.kill(pid, "SIGKILL");
        } catch {
          // gone between the check and the kill
        }
        break;
      }
      await sleep(100);
    }
  }
  fs.rmSync(runDir, { recursive: true, force: true });
}
