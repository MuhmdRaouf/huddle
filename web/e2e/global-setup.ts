// global-setup.ts — builds the real binary and starts one hub on a temp home: HOME,
// HUDDLE_HOME, RADAR_HOME, CLAUDE_CONFIG_DIR and the XDG_* dirs all point into the run folder,
// so nothing of the user's is ever read or written. Fixtures land as files (a transcript with
// three requests, the matching spool lines, a three-card board), the registry and one channel
// are seeded over the API, and the setup file the specs read is written once radar has
// ingested. The teardown stops the one process started here and removes the folder.

import { execFileSync, spawn, type ChildProcess } from "node:child_process";
import fs from "node:fs";
import net from "node:net";
import path from "node:path";
import { randomUUID } from "node:crypto";
import { here, repoRoot, runDir, setupPath, type E2eSetup } from "./paths.ts";

const SESSION_TITLE = "E2E seed session";
const BOARD_NAME = "e2e-demo";
const CHANNEL = "e2e";

const home = path.join(runDir, "home");
const huddleHome = path.join(runDir, "huddle-home");
const radarHome = path.join(runDir, "radar-home");
const claudeConfig = path.join(runDir, "claude-config");
const projectDir = path.join(runDir, "project", BOARD_NAME);
const bin = path.join(runDir, "bin", "huddle");

/** The seeded session's id: the transcript file's name, the registry report's session_id. */
const SESSION_ID = "e2e-seed";

/** The seeded Codex session's id: the rollout file's own name and the session_meta's id. */
const CODEX_SESSION = "9a1f0c3e-2b4d-4e5f-8a6b-7c8d9e0f1a2b";

/** The root credential this one server run answers. */
const TOKEN = `huddle-e2e-${randomUUID().replace(/-/g, "")}`;

let server: ChildProcess | null = null;

export default async function globalSetup(): Promise<void> {
  fs.rmSync(runDir, { recursive: true, force: true });
  for (const dir of [
    home,
    huddleHome,
    path.join(radarHome, "spool"),
    path.join(claudeConfig, "projects", BOARD_NAME),
    projectDir,
    path.join(runDir, "bin"),
  ]) {
    fs.mkdirSync(dir, { recursive: true });
  }

  buildBinary();
  const port = await freePort();

  writeFixtures(port);
  startServer(port);
  await waitHealthy(port);
  await seedRegistry();
  await waitIngested();

  const setup: E2eSetup = {
    rootUrl: `http://127.0.0.1:${port}`,
    host: `127.0.0.1:${port}`,
    pairOrigin: `http://localhost:${port}`,
    token: TOKEN,
    sessionTitle: SESSION_TITLE,
    boardName: BOARD_NAME,
  };
  fs.writeFileSync(setupPath, `${JSON.stringify(setup, null, 2)}\n`);
}

/** Builds the binary the run serves and pairs with. */
function buildBinary(): void {
  execFileSync("go", ["build", "-o", bin, "./cmd/huddle"], { cwd: repoRoot, stdio: "inherit" });
}

/** One free loopback port: bound, read once the bind landed, and released for the server. */
function freePort(): Promise<number> {
  return new Promise<number>((resolve, reject) => {
    const srv = net.createServer();
    srv.once("listening", () => {
      const addr = srv.address();
      const port = typeof addr === "object" && addr !== null ? addr.port : 0;
      srv.close(() => (port === 0 ? reject(new Error("no free port for the e2e server")) : resolve(port)));
    });
    srv.once("error", reject);
    srv.listen(0, "127.0.0.1");
  });
}

/** Copies the fixtures into the temp home, stamping the timestamps the fixtures carry as
 *  @@NOW[+ms]@@ placeholders with the run's own clock, so radar's windows always cover them. */
function writeFixtures(port: number): void {
  const t0 = Date.now() - 60_000;
  const stamp = (raw: string): string =>
    raw.replace(/@@NOW([+-]\d+)?@@/g, (_, ms: string | undefined) =>
      new Date(t0 + Number(ms ?? "0")).toISOString(),
    );
  const fill = (raw: string): string => stamp(raw.split("@@PROJECT@@").join(projectDir));

  fs.writeFileSync(
    path.join(huddleHome, "config.toml"),
    `role = "solo"\nport = ${port}\n`,
  );
  fs.writeFileSync(
    path.join(claudeConfig, "projects", BOARD_NAME, `${SESSION_ID}.jsonl`),
    fill(fs.readFileSync(path.join(here, "fixtures", "transcript.jsonl"), "utf8")),
  );
  const day = new Date(t0).toISOString().slice(0, 10);
  fs.writeFileSync(
    path.join(radarHome, "spool", `${day}.jsonl`),
    fill(fs.readFileSync(path.join(here, "fixtures", "spool.jsonl"), "utf8")),
  );
  // the codex rollout: one turn twenty-five hours back, in the usage windows and outside the Day one
  const codexAt = new Date(t0 - 90_000_000);
  const [year, month, codexDay] = codexAt.toISOString().split("-");
  const codexDir = path.join(home, ".codex", "sessions", year, month, codexDay.slice(0, 2));
  fs.mkdirSync(codexDir, { recursive: true });
  fs.writeFileSync(
    path.join(codexDir, `rollout-${CODEX_SESSION}.jsonl`),
    fill(fs.readFileSync(path.join(here, "fixtures", "codex-rollout.jsonl"), "utf8")),
  );
  const board = path.join(projectDir, ".agents", "kanban");
  fs.cpSync(path.join(here, "fixtures", "kanban"), board, { recursive: true });
}

/** Starts `huddle serve` on the temp home, its output in the run folder. */
function startServer(port: number): void {
  const log = fs.openSync(path.join(runDir, "serve.log"), "a");
  server = spawn(bin, ["serve"], {
    cwd: repoRoot,
    stdio: ["ignore", log, log],
    env: {
      ...process.env,
      HOME: home,
      HUDDLE_HOME: huddleHome,
      RADAR_HOME: radarHome,
      CLAUDE_CONFIG_DIR: claudeConfig,
      XDG_CONFIG_HOME: path.join(runDir, "xdg-config"),
      XDG_CACHE_HOME: path.join(runDir, "xdg-cache"),
      XDG_DATA_HOME: path.join(runDir, "xdg-data"),
      XDG_STATE_HOME: path.join(runDir, "xdg-state"),
      HOST: "127.0.0.1",
      PORT: String(port),
      HUDDLE_TOKEN: TOKEN,
    },
  });
  fs.writeFileSync(
    path.join(runDir, "server.json"),
    `${JSON.stringify({ pid: server.pid, port }, null, 2)}\n`,
  );
  server.once("exit", (code, signal) => {
    if (code !== 0 && code !== null && !shuttingDown) {
      throw new Error(`huddle serve exited early: code ${code}, see serve.log`);
    }
    void signal;
  });
}

let shuttingDown = false;

/** One API call as the root: JSON in, the action header the guard demands, the token. */
async function rootCall<T>(route: string, body: unknown): Promise<T> {
  const r = await fetch(`http://127.0.0.1:${readPort()}${route}`, {
    method: "POST",
    headers: {
      "content-type": "application/json",
      "x-huddle-action": route.split("?", 1)[0] ?? route,
      "x-huddle-token": TOKEN,
    },
    body: JSON.stringify(body),
  });
  if (!r.ok) throw new Error(`${route} answered ${r.status}: ${await r.text()}`);
  return (await r.json()) as T;
}

const sleep = (ms: number): Promise<void> => new Promise((r) => setTimeout(r, ms));

/** Waits until the server answers /health with ok. */
async function waitHealthy(port: number): Promise<void> {
  const deadline = Date.now() + 20_000;
  for (;;) {
    try {
      const r = await fetch(`http://127.0.0.1:${port}/health`);
      if (r.ok && ((await r.json()) as { ok?: boolean }).ok === true) return;
    } catch {
      // not up yet
    }
    if (Date.now() > deadline) throw new Error("the hub never answered /health; see serve.log");
    await sleep(250);
  }
}

/** Seeds what the specs read: one channel for the invite card, one registry session carrying
 *  the board's working dir for the Now list and the Kanban tab. */
async function seedRegistry(): Promise<void> {
  await rootCall(`/api/channels`, { name: CHANNEL });
  await rootCall("/api/registry/report", {
    session_id: SESSION_ID,
    machine: "e2e-machine",
    project: BOARD_NAME,
    title: SESSION_TITLE,
    model: "glm-5.3",
    cwd: projectDir,
    last_event: "UserPromptSubmit",
    last_event_at: new Date().toISOString(),
  });
}

/** Waits until radar's ingest has landed the five seeded requests (four Claude, one Codex), so
 *  the first view a test opens already carries them. */
async function waitIngested(): Promise<void> {
  const port = readPort();
  const deadline = Date.now() + 30_000;
  for (;;) {
    try {
      const r = await fetch(`http://127.0.0.1:${port}/api/radar/api/requests`, {
        headers: { "x-huddle-token": TOKEN },
      });
      if (r.ok) {
        const body = (await r.json()) as { requests?: unknown[] };
        if ((body.requests?.length ?? 0) >= 5) return;
      }
    } catch {
      // not ingested yet
    }
    if (Date.now() > deadline) {
      throw new Error("radar never ingested the seeded requests; see serve.log");
    }
    await sleep(250);
  }
}

/** The port the run serves on, from the file startServer wrote. */
function readPort(): number {
  const saved = JSON.parse(fs.readFileSync(path.join(runDir, "server.json"), "utf8")) as {
    port: number;
  };
  return saved.port;
}

/** Anything that throws after the start still stops the server. */
process.on("exit", () => {
  shuttingDown = true;
  if (server?.pid !== undefined) {
    try {
      process.kill(server.pid, "SIGTERM");
    } catch {
      // already gone
    }
  }
});
