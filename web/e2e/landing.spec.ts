// landing.spec.ts — the owner's first screen when not signed in, over the real binary: a fresh
// hub shows the first-time card with no warning, the tour's thumbnails and lightbox, and the
// start-a-session tabs whose commands copy; a signed-out visit logs no console error; a registry
// with nothing in it greets the Now tab with the welcome in the main pane, once; a browser that
// paired here lands on the biometric button Touch ID names; the first-run checklist on Now ticks
// its items from server state and stands down when all four hold; and `huddle open` on a
// hub-role home prints the hub's own https link, which a browser opens over TLS and lands
// signed in.

import { expect, test, type Page } from "@playwright/test";
import { execFileSync, spawn } from "node:child_process";
import { randomUUID } from "node:crypto";
import fs from "node:fs";
import net from "node:net";
import path from "node:path";
import {
  ConsoleErrors,
  MAC_UA,
  openTab,
  pairThroughBanner,
  pairThroughLink,
  signIn,
  theSetup,
  totpCode,
  totpSecret,
  typeCode,
  useVirtualAuthenticator,
} from "./helpers.ts";
import { repoRoot, runDir } from "./paths.ts";

/** One free loopback port: bound, read once the bind landed, and released for the server. */
function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const srv = net.createServer();
    srv.once("listening", () => {
      const addr = srv.address();
      const port = typeof addr === "object" && addr !== null ? addr.port : 0;
      srv.close(() => (port === 0 ? reject(new Error("no free port for the empty hub")) : resolve(port)));
    });
    srv.once("error", reject);
    srv.listen(0, "127.0.0.1");
  });
}

/** Serves a fresh solo hub with nothing in its registry — the one state the seeded hubs cannot
 *  show — on its own port and temp home under the run's folder; the caller stops it. */
async function emptyHub(): Promise<{ base: string; token: string; stop: () => void }> {
  const homeDir = path.join(runDir, "home-empty");
  fs.rmSync(homeDir, { recursive: true, force: true });
  fs.mkdirSync(homeDir, { recursive: true });
  const port = await freePort();
  const token = `huddle-e2e-empty-${randomUUID().replace(/-/g, "")}`;
  fs.writeFileSync(path.join(homeDir, "config.toml"), `role = "solo"\nport = ${port}\n`);
  const log = fs.openSync(path.join(runDir, "serve-empty.log"), "a");
  const child = spawn(path.join(runDir, "bin", "huddle"), ["serve"], {
    cwd: repoRoot,
    stdio: ["ignore", log, log],
    env: {
      ...process.env,
      HOME: homeDir,
      HUDDLE_HOME: homeDir,
      RADAR_HOME: path.join(homeDir, "radar"),
      CLAUDE_CONFIG_DIR: path.join(homeDir, "claude"),
      XDG_CONFIG_HOME: path.join(homeDir, "xdg", "config"),
      XDG_CACHE_HOME: path.join(homeDir, "xdg", "cache"),
      XDG_DATA_HOME: path.join(homeDir, "xdg", "data"),
      XDG_STATE_HOME: path.join(homeDir, "xdg", "state"),
      HUDDLE_TOKEN: token,
      HUDDLE_TRUST_STORE_OFF: "1",
    },
  });
  const base = `http://127.0.0.1:${port}`;
  const deadline = Date.now() + 20_000;
  for (;;) {
    if (child.exitCode !== null) throw new Error(`the empty hub exited ${child.exitCode}; see serve-empty.log`);
    try {
      const r = await fetch(`${base}/health`);
      if (r.ok) break;
    } catch {
      // not up yet
    }
    if (Date.now() > deadline) throw new Error("the empty hub never answered /health; see serve-empty.log");
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  return { base, token, stop: () => child.kill("SIGTERM") };
}

/** Signs a browser in on a hub the shared helpers do not know: the root token mints the
 *  one-time code, and the first-run authenticator gate takes its skip when it stands. */
async function signInAt(page: Page, base: string, token: string): Promise<void> {
  const r = await fetch(`${base}/api/login`, {
    method: "POST",
    headers: {
      "content-type": "application/json",
      "x-huddle-action": "/api/login",
      "x-huddle-token": token,
    },
    body: "{}",
  });
  if (!r.ok) throw new Error(`minting a code on the empty hub answered ${r.status}`);
  const { code } = (await r.json()) as { code: string };
  const settled = page.waitForURL((url) => !url.searchParams.has("code"));
  await page.goto(`${base}/?code=${code}`);
  await settled;
  const later = page.getByRole("button", { name: /Later/ });
  try {
    await later.waitFor({ state: "visible", timeout: 3_000 });
    await later.click();
  } catch {
    // no first-run gate stands on this hub
  }
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
}

test("a fresh hub shows the first-time landing, its tour and its copyable commands", async ({
  page,
}) => {
  const { rootUrl } = theSetup();
  await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
  await page.goto(`${rootUrl}/`);

  // the first-time card: the setup-link commands, and no warning on a first visit (the connect
  // card carries the same words in its own steps, so the match takes the first)
  await expect(page.getByText(/First time here\?/)).toBeVisible();
  await expect(page.getByText("In a Claude session").first()).toBeVisible();
  await expect(page.getByText("Or in a terminal").first()).toBeVisible();
  await expect(page.locator("[data-reason]")).toHaveCount(0);

  // the tour: four thumbnails, and the lightbox steps through them
  await expect(page.locator("[data-tour] img")).toHaveCount(4);
  await page.getByRole("button", { name: "Now: open the tour step" }).click();
  await expect(page.getByText("Now (1 of 4)")).toBeVisible();
  await page.getByRole("button", { name: "Next" }).click();
  await expect(page.getByText("Channels (2 of 4)")).toBeVisible();
  await page.getByRole("button", { name: "Previous" }).click();
  await expect(page.getByText("Now (1 of 4)")).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page.getByText("Now (1 of 4)")).toHaveCount(0);

  // the start-a-session tabs: each harness carries its own commands, and they copy (a command
  // shows in its label, its code block and a button's hidden name: the first stands for all)
  await page.getByRole("tab", { name: "Codex" }).click();
  await expect(page.getByText("huddle harness install codex").first()).toBeVisible();
  await page.getByRole("tab", { name: "Other MCP" }).click();
  await expect(page.getByText(/x-huddle-token/).first()).toBeVisible();
  await page.getByRole("tab", { name: "Claude Code" }).click();
  await page.getByRole("button", { name: "Copy Claude Code" }).click();
  await expect
    .poll(() => page.evaluate(() => navigator.clipboard.readText()))
    .toContain("/plugin marketplace add MuhmdRaouf/huddle");
});

test("a signed-out visit to the landing logs no console error", async ({ page }) => {
  const errors = new ConsoleErrors(page);
  await page.goto(`${theSetup().rootUrl}/`);
  await expect(page.locator("[data-landing]")).toBeVisible();
  // the boot asks the one question that answers 200 in both states, and none of the shell's
  // reads run signed out: nothing fails, so the console stays empty
  await page.waitForTimeout(500);
  expect(errors.errors()).toEqual([]);
});

test("an empty registry greets the Now tab with the welcome in the main pane, once", async ({
  browser,
}) => {
  test.setTimeout(60_000);
  const hub = await emptyHub();
  try {
    for (const [kind, width, height] of [
      ["desk", 1440, 900],
      ["phone", 390, 844],
    ] as const) {
      const context = await browser.newContext({ viewport: { width, height } });
      const page = await context.newPage();
      await signInAt(page, hub.base, hub.token);
      await openTab(page, "Now");

      // the welcome stands in the main pane, and nowhere else: one heading, one sentence, one
      // start-a-session card, and no dashed box pointing at a list that holds nothing
      await expect(page.locator("#main [data-no-sessions]")).toBeVisible();
      await expect(page.locator("[data-no-sessions]")).toHaveCount(1);
      await expect(page.getByRole("heading", { name: "No sessions yet" })).toBeVisible();
      await expect(
        page.getByText("Bring a coding agent in: install, join, and it reports here with everything it does."),
      ).toBeVisible();
      await expect(page.locator("[data-connect-card]")).toHaveCount(1);
      await expect(page.getByText("No session open")).toHaveCount(0);
      await context.close();
    }
  } finally {
    hub.stop();
  }
});

// a Mac desktop browser: the landing leads with the name the platform gives its own lock
test.describe("the Mac wording", () => {
  test.use({ userAgent: MAC_UA });

  test("a browser that paired here lands on the Touch ID button and one touch signs in", async ({
    page,
  }) => {
    const { pairOrigin } = theSetup();
    await useVirtualAuthenticator(page);
    await pairThroughLink(page, "E2E Landing Pad");

    await page.context().clearCookies();
    await page.goto(`${pairOrigin}/`);
    await expect(page.getByRole("button", { name: "Sign in with Touch ID" })).toBeVisible();
    await page.getByRole("button", { name: "Sign in with Touch ID" }).click();
    await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
  });
});

test("the first-run checklist ticks from server state and stands down when all four hold", async ({
  page,
}) => {
  await useVirtualAuthenticator(page);
  // localhost, not 127.0.0.1: the checklist's passkey ceremony follows, and the relying party is
  // the name
  await signIn(page, theSetup().pairOrigin);
  await openTab(page, "Now");

  // the seeded channel and session already tick two items
  const checklist = page.locator("[data-first-run]");
  await expect(checklist).toBeVisible();
  await expect(checklist.locator("[data-item='channel']")).toHaveAttribute("data-done", "true");
  await expect(checklist.locator("[data-item='session']")).toHaveAttribute("data-done", "true");
  await expect(checklist.locator("[data-item='totp']")).toHaveAttribute("data-done", "false");
  await expect(checklist.locator("[data-item='budget']")).toHaveAttribute("data-done", "false");

  // the passkey row stands down once this browser saved its own passkey
  await pairThroughBanner(page, "E2E Landing Mac");
  await expect(page.getByRole("button", { name: "Save passkey" })).toHaveCount(0);

  // an authenticator enrolment ticks item one (the boot's whoami reads it after a reload)
  await openTab(page, "Settings");
  await page.getByRole("link", { name: "Sign-in" }).click();
  await page.getByRole("button", { name: "Set up" }).click();
  await expect(page.locator("[data-qr]")).toBeVisible();
  const secret = await totpSecret(page);
  await typeCode(page, totpCode(secret));
  await expect(page.getByText("Save your recovery codes")).toBeVisible();
  await page.getByRole("button", { name: "I saved them" }).click();
  await page.reload();
  await openTab(page, "Now");
  await expect(checklist.locator("[data-item='totp']")).toHaveAttribute("data-done", "true");

  // a saved budget ticks the last one, and the checklist stands down — and stays down
  const budgeted = await fetch(`${theSetup().rootUrl}/api/radar/api/budgets`, {
    method: "PUT",
    headers: {
      "content-type": "application/json",
      "x-huddle-action": "/api/radar/api/budgets",
      "x-huddle-token": theSetup().token,
    },
    body: JSON.stringify({
      budgets: [{ id: "day-cap", scope: "total", period: "day", limitUsd: 2, action: "warn" }],
    }),
  });
  if (!budgeted.ok) throw new Error(`saving a budget answered ${budgeted.status}`);
  await page.reload();
  await expect(page.locator("[data-first-run]")).toHaveCount(0);

  // the enrolment and the budget leave with the test, so the later specs meet the seeded hub
  await openTab(page, "Settings");
  await page.getByRole("link", { name: "Sign-in" }).click();
  await page.getByRole("button", { name: "Remove" }).click();
  await expect(page.getByText(/Not set up/)).toBeVisible();
  const cleared = await fetch(`${theSetup().rootUrl}/api/radar/api/budgets`, {
    method: "PUT",
    headers: {
      "content-type": "application/json",
      "x-huddle-action": "/api/radar/api/budgets",
      "x-huddle-token": theSetup().token,
    },
    body: JSON.stringify({ budgets: [] }),
  });
  if (!cleared.ok) throw new Error(`clearing the budgets answered ${cleared.status}`);
});

test("huddle open on a hub prints the hub's own link, and a browser opens it over TLS", async ({
  page,
}) => {
  const { hubRootUrl, hubUrl, hubToken } = theSetup();
  // --print: a terminal with no browser to open, the one an e2e run may run
  const out = execFileSync(`${runDir}/bin/huddle`, ["open", "--print"], {
    cwd: repoRoot,
    encoding: "utf8",
    env: {
      ...process.env,
      HOME: `${runDir}/home-hub`,
      HUDDLE_HOME: `${runDir}/huddle-home-hub`,
      RADAR_HOME: `${runDir}/radar-home-hub`,
      CLAUDE_CONFIG_DIR: `${runDir}/claude-config-hub`,
      XDG_CONFIG_HOME: `${runDir}/xdg-hub-config`,
      XDG_CACHE_HOME: `${runDir}/xdg-hub-cache`,
      XDG_DATA_HOME: `${runDir}/xdg-hub-data`,
      XDG_STATE_HOME: `${runDir}/xdg-hub-state`,
      HUDDLE_URL: hubRootUrl,
      HUDDLE_TOKEN: hubToken,
    },
  });
  // the printed link names the hub's own origin, with its port, never the loopback one
  const url = new RegExp(`^${hubUrl}/\\?code=\\S+`, "m").exec(out)?.[0];
  if (url === undefined) {
    throw new Error(`huddle open on the hub home printed no hub link:\n${out}`);
  }

  // over TLS the one-time code signs this browser straight in; a hub with no authenticator yet
  // offers its set-up first, the skip every sign-in test takes
  await page.goto(url);
  const later = page.getByRole("button", { name: /Later/ });
  try {
    await later.waitFor({ state: "visible", timeout: 3_000 });
    await later.click();
  } catch {
    // an enrolment stands on this hub: no gate to skip
  }
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
});
