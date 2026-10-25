// global-setup.ts — builds the real binary and starts two hubs, each on its own temp home:
// HOME, HUDDLE_HOME, RADAR_HOME, CLAUDE_CONFIG_DIR and the XDG_* dirs all point into the run
// folder, so nothing of the user's is ever read or written. The first hub stays solo on plain
// loopback HTTP (every localhost-origin spec pairs there); the second is a TLS hub serving
// https://hub.example.test under the run's own CA, its certificate imported before the server
// starts. Fixtures land as files in both homes (a transcript with three requests, the matching
// spool lines, a three-card board), the registry and one channel are seeded over each one's
// loopback API, and the setup file the specs read is written once radar has ingested. The
// teardown stops the two processes started here and removes the folder.

import { execFileSync, spawn, type ChildProcess } from "node:child_process";
import fs from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { randomUUID } from "node:crypto";
import https from "node:https";
import {
  DESK_QUIET_MS,
  here,
  outsideProjectPath,
  repoRoot,
  runDir,
  setupPath,
  type E2eSetup,
} from "./paths.ts";
import { LOAD_REQUESTS } from "./seed.ts";
import { HUB_HOST, hubPair } from "./tls.ts";

const SESSION_TITLE = "E2E seed session";
const BOARD_NAME = "e2e-demo";
const CHANNEL = "e2e";

const home = path.join(runDir, "home");
const huddleHome = path.join(runDir, "huddle-home");
const radarHome = path.join(runDir, "radar-home");
const claudeConfig = path.join(runDir, "claude-config");
const projectDir = path.join(runDir, "project", BOARD_NAME);
const bin = path.join(runDir, "bin", "huddle");

const hubHome = path.join(runDir, "home-hub");
const hubHuddleHome = path.join(runDir, "huddle-home-hub");
const hubRadarHome = path.join(runDir, "radar-home-hub");
const hubClaudeConfig = path.join(runDir, "claude-config-hub");
const hubProjectDir = path.join(runDir, "project-hub", BOARD_NAME);

/** The seeded session's id: the transcript file's name, the registry report's session_id. */
const SESSION_ID = "e2e-seed";

/** The seeded Codex session's id: the rollout file's own name and the session_meta's id. */
const CODEX_SESSION = "9a1f0c3e-2b4d-4e5f-8a6b-7c8d9e0f1a2b";

/** The root credential this one server run answers. */
const TOKEN = `huddle-e2e-${randomUUID().replace(/-/g, "")}`;

/** The TLS hub's own root credential. */
const HUB_TOKEN = `huddle-e2e-${randomUUID().replace(/-/g, "")}`;

let server: ChildProcess | null = null;
let serverHub: ChildProcess | null = null;

/** The project dir every command of the run gets as CLAUDE_PROJECT_DIR. huddle's identity lookup
 *  walks up from it, else from the cwd, to the nearest .agents/huddle/huddle.json; the run folder
 *  and the cwd both sit inside this repository, whose huddle.json is the owner's channel, so the
 *  project dir lives in the OS temp dir, outside it. The setup also puts it in its own
 *  process.env, which the test workers inherit, so a spec's child built from process.env gets it
 *  too. The teardown removes it. */
let outsideProject = "";

export default async function globalSetup(): Promise<void> {
  fs.rmSync(runDir, { recursive: true, force: true });
  fs.mkdirSync(runDir, { recursive: true });
  outsideProject = fs.mkdtempSync(path.join(os.tmpdir(), "huddle-e2e-project-"));
  fs.writeFileSync(outsideProjectPath, outsideProject);
  process.env.CLAUDE_PROJECT_DIR = outsideProject;
  for (const dir of [
    home,
    huddleHome,
    path.join(radarHome, "spool"),
    path.join(claudeConfig, "projects", BOARD_NAME),
    projectDir,
    path.join(runDir, "bin"),
    hubHome,
    hubHuddleHome,
    path.join(hubRadarHome, "spool"),
    path.join(hubClaudeConfig, "projects", BOARD_NAME),
    hubProjectDir,
  ]) {
    fs.mkdirSync(dir, { recursive: true });
  }

  buildBinary();
  const port = await freePort();
  const routerPort = await freePort();

  writeFixtures(port);
  startServer(port, routerPort);
  await waitHealthy(port);
  await seedRegistry(port, TOKEN, projectDir);
  await waitIngested(port, TOKEN);

  const tlsPort = await freePort();
  const hubPort = await freePort();
  const hubRouterPort = await freePort();
  writeHubFixtures(hubPort, tlsPort);
  importCertificate(hubRouterPort);
  startHubServer(hubPort, hubRouterPort, tlsPort);
  await waitHealthy(hubPort);
  await seedRegistry(hubPort, HUB_TOKEN, hubProjectDir);
  await waitIngested(hubPort, HUB_TOKEN);
  await tlsAnswers(tlsPort);

  const setup: E2eSetup = {
    rootUrl: `http://127.0.0.1:${port}`,
    host: `127.0.0.1:${port}`,
    pairOrigin: `http://localhost:${port}`,
    token: TOKEN,
    sessionTitle: SESSION_TITLE,
    boardName: BOARD_NAME,
    hubUrl: `https://${HUB_HOST}:${tlsPort}`,
    hubRootUrl: `http://127.0.0.1:${hubPort}`,
    hubToken: HUB_TOKEN,
    routerPort,
    hubRouterPort,
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

/** Copies the fixtures into one temp home, stamping the timestamps the fixtures carry as
 *  @@NOW[+ms]@@ placeholders with the run's own clock, so radar's windows always cover them. */
function writeFixtureData(target: {
  home: string;
  claudeConfig: string;
  radarHome: string;
  projectDir: string;
}): void {
  const t0 = Date.now() - 60_000;
  const stamp = (raw: string): string =>
    raw.replace(/@@NOW([+-]\d+)?@@/g, (_, ms: string | undefined) =>
      new Date(t0 + Number(ms ?? "0")).toISOString(),
    );
  const fill = (raw: string): string => stamp(raw.split("@@PROJECT@@").join(target.projectDir));

  fs.writeFileSync(
    path.join(target.claudeConfig, "projects", BOARD_NAME, `${SESSION_ID}.jsonl`),
    fill(fs.readFileSync(path.join(here, "fixtures", "transcript.jsonl"), "utf8")),
  );
  const day = new Date(t0).toISOString().slice(0, 10);
  fs.writeFileSync(
    path.join(target.radarHome, "spool", `${day}.jsonl`),
    fill(fs.readFileSync(path.join(here, "fixtures", "spool.jsonl"), "utf8")),
  );
  // the codex rollout: one turn twenty-five hours back, in the usage windows and outside the Day one
  const codexAt = new Date(t0 - 90_000_000);
  const [year, month, codexDay] = codexAt.toISOString().split("-");
  const codexDir = path.join(target.home, ".codex", "sessions", year, month, codexDay.slice(0, 2));
  fs.mkdirSync(codexDir, { recursive: true });
  fs.writeFileSync(
    path.join(codexDir, `rollout-${CODEX_SESSION}.jsonl`),
    fill(fs.readFileSync(path.join(here, "fixtures", "codex-rollout.jsonl"), "utf8")),
  );
  const board = path.join(target.projectDir, ".agents", "kanban");
  fs.cpSync(path.join(here, "fixtures", "kanban"), board, { recursive: true });
}

/** The solo hub's config: plain loopback HTTP, the shape every localhost-origin spec pairs on. */
function writeFixtures(port: number): void {
  fs.writeFileSync(path.join(huddleHome, "config.toml"), `role = "solo"\nport = ${port}\n`);
  writeFixtureData({ home, claudeConfig, radarHome, projectDir });
  // one provider set up and no huddle-router started: the server's health names zai and says
  // the router is down, which is what the Radar overview's routers tile and list show
  const routerHome = path.join(home, ".agents", "huddle-router");
  fs.mkdirSync(routerHome, { recursive: true });
  fs.writeFileSync(path.join(routerHome, "providers.json"), '{"providers":["zai"]}\n');
}

/** The TLS hub's config: its own loopback port for the API, and the listener the reserved name
 *  serves https on behind the imported certificate. */
function writeHubFixtures(port: number, tlsPort: number): void {
  fs.writeFileSync(
    path.join(hubHuddleHome, "config.toml"),
    `role = "hub"\nport = ${port}\n\n[hub]\nlisten = "127.0.0.1:${tlsPort}"\nhost = "${HUB_HOST}"\n`,
  );
  writeFixtureData({
    home: hubHome,
    claudeConfig: hubClaudeConfig,
    radarHome: hubRadarHome,
    projectDir: hubProjectDir,
  });
}

/** Imports the run's certificate into the TLS hub's home, before its server starts, with the
 *  same temp homes the server itself gets so nothing of the user's is read. */
function importCertificate(routerPort: number): void {
  const pair = hubPair();
  execFileSync(bin, ["cert", "--import", pair.chain, pair.key], {
    cwd: repoRoot,
    stdio: "inherit",
    env: envFor(tlsHubHomeDirs, null, null, routerPort),
  });
}

/** Proves the TLS listener before any test runs: Node, trusting only the run's CA, gets the
 *  shell's page over the address the reserved name maps to. */
function tlsAnswers(tlsPort: number): Promise<void> {
  return new Promise<void>((resolve, reject) => {
    const req = https.request(
      {
        host: "127.0.0.1",
        port: tlsPort,
        path: "/",
        method: "GET",
        ca: fs.readFileSync(hubPair().ca),
      },
      (res) => {
        if (res.statusCode === 200) {
          res.resume();
          resolve();
          return;
        }
        reject(new Error(`the TLS listener answered ${res.statusCode}; see serve-hub.log`));
      },
    );
    req.once("error", reject);
    req.end();
  });
}

/** One temp home's dirs, the set every env the run builds a home from. */
type TempHome = {
  home: string;
  huddleHome: string;
  radarHome: string;
  claudeConfig: string;
  xdg: string;
};

const soloHome: TempHome = {
  home,
  huddleHome,
  radarHome,
  claudeConfig,
  xdg: path.join(runDir, "xdg"),
};

const tlsHubHomeDirs: TempHome = {
  home: hubHome,
  huddleHome: hubHuddleHome,
  radarHome: hubRadarHome,
  claudeConfig: hubClaudeConfig,
  xdg: path.join(runDir, "xdg-hub"),
};

/** The process environment one temp home's commands run with: every dir the binary honours
 *  points into the run folder, and the project dir outside the repository, so nothing of the
 *  user's is read or written. */
function envFor(target: TempHome, port: number | null, token: string | null, routerPort: number) {
  return {
    ...process.env,
    HOME: target.home,
    HUDDLE_HOME: target.huddleHome,
    RADAR_HOME: target.radarHome,
    CLAUDE_CONFIG_DIR: target.claudeConfig,
    CLAUDE_PROJECT_DIR: outsideProject,
    XDG_CONFIG_HOME: `${target.xdg}-config`,
    XDG_CACHE_HOME: `${target.xdg}-cache`,
    XDG_DATA_HOME: `${target.xdg}-data`,
    XDG_STATE_HOME: `${target.xdg}-state`,
    HOST: "127.0.0.1",
    // every command of the run reads the router port: one of this run's own, never the
    // installed daemon's 18786, so the run never finds, probes or hands over the user's router
    HUDDLE_ROUTER_PORT: String(routerPort),
    // a closed page's focus report keeps the phone quiet this long; a page Playwright closes
    // never reports itself hidden, so the run holds it for 2 s, not 60
    HUDDLE_DESK_QUIET_MS: String(DESK_QUIET_MS),
    ...(port === null ? {} : { PORT: String(port) }),
    ...(token === null ? {} : { HUDDLE_TOKEN: token }),
  };
}

/** Starts `huddle serve` on one temp home, its output in the run folder and its pid and ports
 *  in the state file the teardown reads. */
function serve(
  target: TempHome,
  port: number,
  routerPort: number,
  token: string,
  logName: string,
  stateName: string,
  tlsPort: number | null,
): ChildProcess {
  const log = fs.openSync(path.join(runDir, logName), "a");
  const child = spawn(bin, ["serve"], {
    cwd: repoRoot,
    stdio: ["ignore", log, log],
    env: envFor(target, port, token, routerPort),
  });
  fs.writeFileSync(
    path.join(runDir, stateName),
    `${JSON.stringify({ pid: child.pid, port, ...(tlsPort === null ? {} : { tlsPort }) }, null, 2)}\n`,
  );
  child.once("exit", (code, signal) => {
    if (code !== 0 && code !== null && !shuttingDown) {
      throw new Error(`huddle serve exited early: code ${code}, see ${logName}`);
    }
    void signal;
  });
  return child;
}

/** Starts the solo hub, plain loopback HTTP. */
function startServer(port: number, routerPort: number): void {
  server = serve(soloHome, port, routerPort, TOKEN, "serve.log", "server.json", null);
}

/** Starts the TLS hub: its loopback API port plus the listener the reserved name serves on. */
function startHubServer(port: number, routerPort: number, tlsPort: number): void {
  serverHub = serve(tlsHubHomeDirs, port, routerPort, HUB_TOKEN, "serve-hub.log", "server-hub.json", tlsPort);
}

let shuttingDown = false;

/** One API call as the root: JSON in, the action header the guard demands, the token. */
async function rootCall<T>(route: string, body: unknown, port: number, token: string): Promise<T> {
  const r = await fetch(`http://127.0.0.1:${port}${route}`, {
    method: "POST",
    headers: {
      "content-type": "application/json",
      "x-huddle-action": route.split("?", 1)[0] ?? route,
      "x-huddle-token": token,
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
 *  the board's working dir for the Now list and the Kanban tab, and two sessions in the channel
 *  whose ping and pong the channel page's Messages card shows. */
async function seedRegistry(port: number, token: string, boardDir: string): Promise<void> {
  await rootCall(`/api/channels`, { name: CHANNEL }, port, token);
  await rootCall(
    "/api/registry/report",
    {
      session_id: SESSION_ID,
      machine: "e2e-machine",
      project: BOARD_NAME,
      title: SESSION_TITLE,
      model: "glm-5.3",
      cwd: boardDir,
      last_event: "UserPromptSubmit",
      last_event_at: new Date().toISOString(),
    },
    port,
    token,
  );
  await channelOp(port, token, "join", "alpha", {});
  await channelOp(port, token, "join", "beta", {});
  await channelOp(port, token, "send", "alpha", { to: "beta", msg: "ping" });
  await channelOp(port, token, "send", "beta", { to: "alpha", msg: "pong" });
}

/** One channel operation as one named session: the same op endpoint the agents' clients call. */
async function channelOp(
  port: number,
  token: string,
  op: string,
  as: string,
  body: Record<string, unknown>,
): Promise<void> {
  await rootCall(`/api/c/${CHANNEL}/op/${op}?as=${as}`, body, port, token);
}

/** Waits until radar's ingest has landed the six seeded requests (five Claude, one Codex), so
 *  the first view a test opens already carries them. */
async function waitIngested(port: number, token: string): Promise<void> {
  const deadline = Date.now() + 30_000;
  for (;;) {
    try {
      const r = await fetch(`http://127.0.0.1:${port}/api/radar/api/requests`, {
        headers: { "x-huddle-token": token },
      });
      if (r.ok) {
        const body = (await r.json()) as { requests?: unknown[] };
        // the seeded six plus the usage load, clamped to the requests list's own 200-row cap
        if ((body.requests?.length ?? 0) >= Math.min(6 + LOAD_REQUESTS, 200)) return;
      }
    } catch {
      // not ingested yet
    }
    if (Date.now() > deadline) {
      throw new Error("radar never ingested the seeded requests; see serve.log and serve-hub.log");
    }
    await sleep(250);
  }
}

/** Anything that throws after the start still stops both servers. */
process.on("exit", () => {
  shuttingDown = true;
  for (const child of [server, serverHub]) {
    if (child?.pid !== undefined) {
      try {
        process.kill(child.pid, "SIGTERM");
      } catch {
        // already gone
      }
    }
  }
});
