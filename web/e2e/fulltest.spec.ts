// fulltest.spec.ts — the whole dashboard over the TLS hub, one signed-in walk: the browser
// pairs at https://hub.example.test behind the run's own CA (the listener refuses one-time
// links, so the passkey ceremony is the way in), then clicks through every tab — Sessions,
// Channels, each Radar view, Kanban, Settings — asserting each one's own heading, with the
// Usage tiles and the Costs series carrying the seeded totals, one screenshot per view under
// the scratch folder, and not one console error. The second test proves the other trust path:
// a hub home with no imported certificate serves a leaf its own local CA signs, which a client
// trusting only that home's root accepts — and one trusting its system roots alone refuses.

import { expect, test } from "@playwright/test";
import { spawn, type ChildProcess } from "node:child_process";
import fs from "node:fs";
import net from "node:net";
import path from "node:path";
import https from "node:https";
import {
  ConsoleErrors,
  USAGE,
  mintPairLink,
  n,
  openTab,
  pairThroughLink,
  theSetup,
  tlsHubPairHome,
  useVirtualAuthenticator,
} from "./helpers.ts";
import { HUB_HOST } from "./tls.ts";
import { repoRoot, runDir } from "./paths.ts";

/** The device this walk pairs as. */
const DEVICE = "TLS Walk";

/** Where the walk's screenshots land, one per view. */
const shots = path.join(repoRoot, ".agents", "scratch", "e2e");

/** The Radar views in their sub-tab order, each with the one thing only it shows once
 *  loaded — the same markers radar.spec walks the plain listener with. */
const VIEWS: readonly (readonly [view: string, label: string, role: "heading" | "row", loaded: RegExp])[] = [
  ["overview", "Overview", "heading", /Top sessions by spend/],
  ["costs", "Costs", "heading", /Cost attribution/],
  ["usage", "Usage", "heading", /Daily usage/],
  ["blocks", "Blocks", "heading", /Metering blocks/],
  ["models", "Models", "heading", /^Models$/],
  ["requests", "Requests", "row", /open details/],
  ["tools", "Tools", "heading", /Most used tools/],
  ["agents", "Agents", "row", /Status Agent Model/],
  ["routers", "Routers", "heading", /Router health/],
  ["alerts", "Alerts", "heading", /All clear|^Alerts$/],
  ["timeline", "Timeline", "heading", /^Timeline$/],
];

test("one signed-in walk over TLS covers every tab with no console error", async ({ page }) => {
  test.setTimeout(120_000);
  await useVirtualAuthenticator(page);
  await pairThroughLink(page, DEVICE, mintPairLink(DEVICE, tlsHubPairHome));

  // the console watch starts once the shell is up: the pairing pages answer 401s before the
  // cookie exists, and those are the guard doing its job, not errors of the dashboard
  const errors = new ConsoleErrors(page);
  fs.mkdirSync(shots, { recursive: true });
  const shot = (view: string): Promise<unknown> => page.screenshot({ path: path.join(shots, `${view}.png`) });

  // Sessions: the seeded session sits in the Working group
  await openTab(page, "Sessions");
  await expect(page.getByRole("heading", { name: "Working · 1" })).toBeVisible();
  await shot("sessions");

  // Channels: the connect card names the seeded channel and its invite works over TLS
  await openTab(page, "Channels");
  const invite = page.getByRole("button", { name: "Invite a session" });
  await expect(invite).toBeVisible();
  await invite.click();
  await expect(page.getByRole("button", { name: /Copy Join line for e2e/ })).toBeVisible();
  await shot("channels");

  // Radar: every view through its sub-tab row, each with its own heading
  await openTab(page, "Radar");
  for (const [view, label, role, loaded] of VIEWS) {
    const link = page
      .getByRole("navigation", { name: "Radar views" })
      .getByRole("link", { name: label, exact: true });
    await link.click();
    await expect(page.getByRole(role, { name: loaded }).first()).toBeVisible();

    // the Costs series carries the seeded window: the seeded calls plus the load, not an empty chart
    if (view === "costs") {
      await page.getByRole("button", { name: "30 days" }).click();
      await expect(page.locator("[data-key='attribution-table']")).toBeVisible();
      await expect(page.locator(".bar-seg").first()).toBeVisible();
      await expect(
        page.getByText(new RegExp(`30 days: .+, ${n(USAGE.requests)} requests, `)),
      ).toBeVisible();
    }
    // the Usage tiles carry the seeded totals exactly, the chart one stacked column per day
    if (view === "usage") {
      const tiles = page.locator("[data-usage-tiles]");
      await expect(tiles).toContainText(USAGE.processed.toLocaleString("en-US"));
      await expect(tiles).toContainText(USAGE.cached.toLocaleString("en-US"));
      await expect(tiles).toContainText(USAGE.uncached.toLocaleString("en-US"));
      await expect(tiles).toContainText(USAGE.output.toLocaleString("en-US"));
      await expect(page.locator("[data-usage-plot] rect").first()).toBeVisible();
      await expect(page.locator("[data-usage-plot] path.chart-line")).toHaveCount(0);
    }
    await shot(`radar-${view}`);
  }

  // Kanban: the seeded board and its three columns
  await openTab(page, "Kanban");
  await expect(page.getByLabel("Board", { exact: true })).toContainText(theSetup().boardName);
  await expect(page.getByRole("heading", { name: "Backlog 1" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Ready 1" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "In Progress 1" })).toBeVisible();
  await expect(page.getByRole("button", { name: "E2E-1: First card" })).toBeVisible();
  await shot("kanban");

  // the walk clicks through to the Spending group for its budgets
  await openTab(page, "Settings");
  await page.getByRole("navigation", { name: "Settings sections" }).getByRole("link", { name: "Spending" }).click();
  await expect(page.getByRole("heading", { name: "Budgets" })).toBeVisible();
  await shot("settings");

  expect(errors.errors()).toEqual([]);
});

/** One free loopback port: bound, read once the bind landed, released for the listener. */
function freePort(): Promise<number> {
  return new Promise<number>((resolve, reject) => {
    const srv = net.createServer();
    srv.once("listening", () => {
      const addr = srv.address();
      const port = typeof addr === "object" && addr !== null ? addr.port : 0;
      srv.close(() => (port === 0 ? reject(new Error("no free port")) : resolve(port)));
    });
    srv.once("error", reject);
    srv.listen(0, "127.0.0.1");
  });
}

/** One GET / over TLS, trusting only the roots given (none means the system's alone). */
function tlsGet(port: number, ca: Buffer): Promise<number> {
  return new Promise<number>((resolve, reject) => {
    const req = https.request(
      { host: "127.0.0.1", port, path: "/", method: "GET", ca },
      (res) => {
        res.resume();
        resolve(res.statusCode ?? 0);
      },
    );
    req.once("error", reject);
    req.end();
  });
}

test("a hub home with no import serves a leaf its own root signs", async () => {
  const home = path.join(runDir, "home-ca");
  fs.rmSync(home, { recursive: true, force: true });
  fs.mkdirSync(path.join(home, "pki"), { recursive: true });
  const port = await freePort();
  fs.writeFileSync(
    path.join(home, "config.toml"),
    `role = "hub"\n\n[hub]\nlisten = "127.0.0.1:${port}"\nhost = "${HUB_HOST}"\n`,
  );
  const log = fs.openSync(path.join(runDir, "serve-ca.log"), "a");
  const child: ChildProcess = spawn(path.join(runDir, "bin", "huddle"), ["serve"], {
    cwd: repoRoot,
    stdio: ["ignore", log, log],
    env: {
      ...process.env,
      HOME: home,
      HUDDLE_HOME: home,
      RADAR_HOME: path.join(home, "radar"),
      CLAUDE_CONFIG_DIR: path.join(home, "claude"),
      XDG_CONFIG_HOME: path.join(home, "xdg", "config"),
      XDG_CACHE_HOME: path.join(home, "xdg", "cache"),
      XDG_DATA_HOME: path.join(home, "xdg", "data"),
      XDG_STATE_HOME: path.join(home, "xdg", "state"),
      HUDDLE_TRUST_STORE_OFF: "1",
    },
  });
  try {
    // the listener is up once the server says so itself; the root it persists must already sit
    // in the home a client is told to trust
    const deadline = Date.now() + 20_000;
    for (;;) {
      const served = /hub listener on https:\/\/127\.0\.0\.1:\d+/.test(
        fs.readFileSync(path.join(runDir, "serve-ca.log"), "utf8"),
      );
      if (served && fs.existsSync(path.join(home, "pki", "root.crt"))) break;
      if (child.exitCode !== null) throw new Error(`huddle serve exited ${child.exitCode}; see serve-ca.log`);
      if (Date.now() > deadline) throw new Error("the local-CA listener never came up; see serve-ca.log");
      await new Promise<void>((resolve) => setTimeout(resolve, 100));
    }

    const root = fs.readFileSync(path.join(home, "pki", "root.crt"));
    // a client trusting only the home's root gets the shell
    await expect(tlsGet(port, root)).resolves.toBe(200);
    // a client trusting its system roots alone refuses the leaf the local CA signed
    await expect(tlsGet(port, Buffer.alloc(0))).rejects.toThrow(
      /local issuer certificate|self.signed/i,
    );
  } finally {
    child.kill("SIGTERM");
  }
});
