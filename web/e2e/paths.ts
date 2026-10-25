// paths.ts — where the e2e run lives and what the tests read from it: the setup file the
// global setup writes once the server answers, and the scratch folder everything temp goes
// into (gitignored, removed by the teardown).

import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

/** The web/e2e folder these files sit in. */
export const here = path.dirname(fileURLToPath(import.meta.url));

/** The repository root, two levels up. */
export const repoRoot = path.resolve(here, "..", "..");

/** The run's own folder under the repo's gitignored scratch: temp homes, the built binary,
 *  the setup file. Every run starts by emptying it. */
export const runDir = path.join(repoRoot, ".agents", "scratch", "e2e", "run");

/** The file naming the project dir the global setup made in the OS temp dir, for the teardown. */
export const outsideProjectPath = path.join(runDir, "project-dir");

/** The file the global setup writes and every test reads. */
export const setupPath = path.join(runDir, "setup.json");

/** What the setup file carries: where the server answers, the root credential the tests mint
 *  sign-in codes with, and the seeded names the specs assert on. */
export type E2eSetup = {
  /** The hub's address the tests sign in at: http://127.0.0.1:<port>. */
  rootUrl: string;
  /** The host part of rootUrl: 127.0.0.1:<port>, as the invite line spells it. */
  host: string;
  /** The pairing origin: http://localhost:<port> — the relying party is the name, so a
   *  passkey ceremony only runs there. */
  pairOrigin: string;
  /** The server's root credential (HUDDLE_TOKEN of this run). */
  token: string;
  /** The seeded registry session's title, shown on the Now list. */
  sessionTitle: string;
  /** The seeded board's name: the project folder the board sits in. */
  boardName: string;
  /** The port the solo hub's huddle-router listens on: the run's own, never the daemon's 18786. */
  routerPort: number;
  /** The port the TLS hub's huddle-router listens on, its own as well. */
  hubRouterPort: number;
  /** The TLS hub's address: https://hub.example.test:<port>, the run's own CA behind it. */
  hubUrl: string;
  /** The TLS hub's loopback API port: where its own machine's CLI mints and joins. */
  hubRootUrl: string;
  /** The TLS hub's root credential (HUDDLE_TOKEN of its own run). */
  hubToken: string;
};

/** Reads the setup file the global setup wrote; a missing file says the run is broken. */
export function readSetup(): E2eSetup {
  return JSON.parse(fs.readFileSync(setupPath, "utf8")) as E2eSetup;
}
