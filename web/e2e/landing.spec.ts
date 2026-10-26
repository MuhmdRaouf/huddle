// landing.spec.ts — the owner's first screen when not signed in, over the real binary: a fresh
// hub shows the first-time card with no warning, the tour's thumbnails and lightbox, and the
// start-a-session tabs whose commands copy; a signed-out visit logs no console error; a registry
// with nothing in it greets the Sessions tab with the welcome in the main pane, once; a browser that
// paired here lands on the biometric button Touch ID names; the first-run checklist on Sessions ticks
// its items from server state and stands down when all four hold; and `huddle open` on a
// hub-role home prints the hub's own https link, which a browser opens over TLS and lands
// signed in. On both projects the landing is one sign-in card that fits 390 px with every
// control a 44 px target and no axe issue, the start-a-session guide opens in a sheet whose code
// blocks scroll inside their own box and pass axe in both schemes, and the authenticator set-up a fresh hub's first sign-in
// lands on fits 390 px with its Later button a full tap target.

import { expect, test, type Page } from "@playwright/test";
import { type ChildProcess, execFileSync, spawn } from "node:child_process";
import { randomUUID } from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { expectAccessible } from "./axe.ts";
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

/** A started empty hub: where it answers, its root credential, and the stop that waits for its exit. */
type EmptyHub = { base: string; token: string; stop: () => Promise<void> };

/** The line `huddle serve` prints once its listener is bound, naming the port it took. */
const LISTENING = /^Huddle \S+ on http:\/\/127\.0\.0\.1:(\d+) /m;

/** Serves a fresh solo hub with nothing in its registry — the one state the seeded hubs cannot
 *  show — on a temp home of the running project's own under the run's folder; the caller stops
 *  it. PORT=0 makes the hub bind a port the OS hands it and print it, so no other process can
 *  take the port between a probe and the bind. A start that fails attaches its log to the
 *  report, since the run folder goes with the teardown. */
async function emptyHub(): Promise<EmptyHub> {
  const info = test.info();
  const name = `empty-${info.project.name}`;
  const homeDir = path.join(runDir, `home-${name}`);
  const logPath = path.join(runDir, `serve-${name}.log`);
  fs.rmSync(homeDir, { recursive: true, force: true });
  fs.mkdirSync(homeDir, { recursive: true });
  const token = `huddle-e2e-empty-${randomUUID().replace(/-/g, "")}`;
  fs.writeFileSync(path.join(homeDir, "config.toml"), `role = "solo"\n`);
  const log = fs.openSync(logPath, "w");
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
      PORT: "0",
      HUDDLE_TOKEN: token,
      HUDDLE_TRUST_STORE_OFF: "1",
    },
  });
  fs.closeSync(log);
  const exited = new Promise<void>((resolve) => child.once("exit", () => resolve()));
  const stop = async (): Promise<void> => {
    if (child.exitCode === null && child.signalCode === null) child.kill("SIGTERM");
    await exited;
  };
  try {
    return { base: await answering(child, logPath), token, stop };
  } catch (err) {
    await stop();
    await info.attach(path.basename(logPath), { path: logPath, contentType: "text/plain" });
    throw err;
  }
}

/** Waits for the hub to print its port and answer /health there; its address. */
async function answering(child: ChildProcess, logPath: string): Promise<string> {
  const logName = path.basename(logPath);
  const deadline = Date.now() + 20_000;
  let base = "";
  for (;;) {
    if (child.exitCode !== null) throw new Error(`the empty hub exited ${child.exitCode}; see ${logName}`);
    if (base === "") {
      const port = LISTENING.exec(fs.readFileSync(logPath, "utf8"))?.[1];
      if (port !== undefined) base = `http://127.0.0.1:${port}`;
    }
    if (base !== "") {
      try {
        const r = await fetch(`${base}/health`);
        if (r.ok) return base;
      } catch {
        // not answering yet
      }
    }
    if (Date.now() > deadline) throw new Error(`the empty hub never answered /health; see ${logName}`);
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
}

/** One one-time sign-in link on a hub the shared helpers do not know, minted with its root token. */
async function codeLink(base: string, token: string): Promise<string> {
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
  return `${base}/?code=${code}`;
}

/** Signs a browser in on a hub the shared helpers do not know: the one-time link, and the
 *  first-run authenticator gate takes its skip when it stands. */
async function signInAt(page: Page, base: string, token: string): Promise<void> {
  const link = await codeLink(base, token);
  const settled = page.waitForURL((url) => !url.searchParams.has("code"));
  await page.goto(link);
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

/** How far the page scrolls sideways: above zero is a page the thumb drags left and right. */
function sideways(page: Page): Promise<number> {
  return page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
}

/** Every visible control inside `scope` smaller than a 44 px tap target on either side, named
 *  for the failure message. */
function smallTargets(page: Page, scope: string): Promise<string[]> {
  return page.evaluate(
    (sel) =>
      [...document.querySelectorAll<HTMLElement>(`${sel} :is(button, a, input, summary, [role=tab])`)]
        .map((el) => ({ el, r: el.getBoundingClientRect() }))
        .filter(({ el, r }) => r.width > 0 && r.height > 0 && getComputedStyle(el).visibility !== "hidden")
        .filter(({ r }) => r.width < 44 || r.height < 44)
        .map(({ el, r }) => {
          const name = el.getAttribute("aria-label") || el.textContent?.trim() || el.tagName;
          const size = `${Math.round(r.width)}x${Math.round(r.height)}`;
          return `${el.tagName.toLowerCase()} "${name.slice(0, 40)}" ${size}`;
        }),
    scope,
  );
}

test("the landing is one sign-in card that fits 390 px, every control a tap target, axe clean", async ({
  page,
  isMobile,
}) => {
  if (isMobile) await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(`${theSetup().rootUrl}/`);
  await expect(page.locator("[data-landing]")).toBeVisible();
  // the card's method landed: the first-time steps, the code field or a passkey button
  const method = "[data-first-time], [data-signin-code], [data-passkey-signin], [data-bio-signin]";
  await expect(page.locator(`[data-landing-signin] :is(${method})`).first()).toBeVisible();
  await expect(page.locator("[data-landing-signin]")).toHaveCount(1);
  await expect(page.getByRole("region", { name: "Sign in" })).toBeVisible();
  // the guide waits behind one button, not as a second card beside sign-in
  await expect(page.locator("[data-landing] [data-connect-card]")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Start a session" })).toBeVisible();

  expect(await sideways(page), "the landing scrolls sideways").toBeLessThanOrEqual(0);
  expect(await smallTargets(page, "[data-landing]"), "controls under 44 px").toEqual([]);
  await expectAccessible(page);
});

test("the start-a-session guide opens in a sheet whose code blocks scroll inside their box", async ({
  page,
  isMobile,
}) => {
  if (isMobile) await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(`${theSetup().rootUrl}/`);
  await page.getByRole("button", { name: "Start a session" }).click();
  const sheet = page.getByRole("dialog", { name: "Start a session" });
  await expect(sheet).toBeVisible();
  await expect(sheet.locator("[data-connect-card]")).toBeVisible();

  // the MCP line is the longest command the guide carries: on a phone it runs past its box
  await sheet.getByRole("tab", { name: "Other MCP" }).click();
  const pre = sheet.locator("pre").first();
  await expect(pre).toContainText("claude mcp add");
  const box = await pre.evaluate((el) => {
    const r = el.getBoundingClientRect();
    return {
      over: el.scrollWidth > el.clientWidth,
      scrolls: ["auto", "scroll"].includes(getComputedStyle(el).overflowX),
      right: r.right,
      view: window.innerWidth,
    };
  });
  if (isMobile) expect(box.over, "the MCP line overflows its box on the phone").toBe(true);
  if (box.over) expect(box.scrolls, "an overflowing code block scrolls inside its box").toBe(true);
  expect(box.right, "the code block ends inside the screen").toBeLessThanOrEqual(box.view);
  expect(await sideways(page), "the sheet scrolls the page sideways").toBeLessThanOrEqual(0);
  expect(await smallTargets(page, ".ui-sheet[open]"), "the sheet's controls under 44 px").toEqual([]);
  await expectAccessible(page);
  // the code tokens keep their contrast in the dark scheme too
  await page.emulateMedia({ colorScheme: "dark" });
  await page.reload();
  await page.getByRole("button", { name: "Start a session" }).click();
  await sheet.getByRole("tab", { name: "Other MCP" }).click();
  await expect(pre).toContainText("claude mcp add");
  await expectAccessible(page);

  await page.keyboard.press("Escape");
  await expect(sheet).toBeHidden();
});

test("the authenticator set-up a fresh hub's first sign-in lands on fits 390 px", async ({ page }) => {
  const hub = await emptyHub();
  try {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto(await codeLink(hub.base, hub.token));
    await expect(page.locator("[data-totp-first-run]")).toBeVisible();
    await expect(page.locator("[data-qr]")).toBeVisible();
    await expect(page.locator("[data-secret]")).toBeVisible();

    expect(await sideways(page), "the set-up scrolls sideways").toBeLessThanOrEqual(0);
    const secret = await page.locator("[data-secret]").boundingBox();
    const secretEnd = (secret?.x ?? 0) + (secret?.width ?? 0);
    expect(secretEnd, "the secret row ends inside the screen").toBeLessThanOrEqual(390);
    const later = await page.getByRole("button", { name: /Later/ }).boundingBox();
    expect(later?.height ?? 0, "the Later button's height").toBeGreaterThanOrEqual(44);
    expect(await smallTargets(page, "main"), "controls under 44 px").toEqual([]);
    await expectAccessible(page);

    await page.getByRole("button", { name: /Later/ }).click();
    await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
  } finally {
    await hub.stop();
  }
});

test("a fresh hub shows the first-time landing, its tour and its copyable commands", async ({
  page,
}) => {
  // a hub of its own: the shared hub holds the passkeys the other project paired, and a fresh hub
  // is the one state the first-time card shows in
  const hub = await emptyHub();
  try {
    await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
    await page.goto(`${hub.base}/`);

    // the first-time card: the setup-link commands, and no warning on a first visit (the connect
    // card carries the same words in its own steps, so the match takes the first)
    await expect(page.getByText(/First time here\?/)).toBeVisible();
    await expect(page.getByText("In a Claude session").first()).toBeVisible();
    await expect(page.getByText("Or in a terminal").first()).toBeVisible();
    await expect(page.locator("[data-reason]")).toHaveCount(0);

    // the tour: four thumbnails, and the lightbox steps through them
    await expect(page.locator("[data-tour] img")).toHaveCount(4);
    await page.getByRole("button", { name: "Sessions: open the tour step" }).click();
    await expect(page.getByText("Sessions (1 of 4)")).toBeVisible();
    await page.getByRole("button", { name: "Next" }).click();
    await expect(page.getByText("Channels (2 of 4)")).toBeVisible();
    await page.getByRole("button", { name: "Previous" }).click();
    await expect(page.getByText("Sessions (1 of 4)")).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(page.getByText("Sessions (1 of 4)")).toHaveCount(0);

    // the start-a-session tabs, in their sheet: each harness carries its own commands, and they
    // copy (a command shows in its label, its code block and a button's hidden name: the first
    // stands for all)
    await page.getByRole("button", { name: "Start a session" }).click();
    await page.getByRole("tab", { name: "Codex" }).click();
    await expect(page.getByText("huddle harness install codex").first()).toBeVisible();
    await page.getByRole("tab", { name: "Other MCP" }).click();
    await expect(page.getByText(/x-huddle-token/).first()).toBeVisible();
    await page.getByRole("tab", { name: "Claude Code" }).click();
    await page.getByRole("button", { name: "Copy Claude Code" }).click();
    await expect
      .poll(() => page.evaluate(() => navigator.clipboard.readText()))
      .toContain("/plugin marketplace add MuhmdRaouf/huddle");
  } finally {
    await hub.stop();
  }
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

test("an empty registry greets the Sessions tab with the welcome in the main pane, once", async ({
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
      await openTab(page, "Sessions");

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
    await hub.stop();
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
    await pairThroughLink(page, `E2E Landing Pad ${test.info().project.name}`);

    await page.context().clearCookies();
    await page.goto(`${pairOrigin}/`);
    await expect(page.getByRole("button", { name: "Sign in with Touch ID" })).toBeVisible();
    await page.getByRole("button", { name: "Sign in with Touch ID" }).click();
    await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
  });
});

test("the first-run checklist ticks from server state and stands down when all four hold", async ({
  page,
}, testInfo) => {
  // a state story on the shared hub: it enrols the authenticator and saves a budget, so it runs
  // once, on the desktop; the phone project proves the landing's layout and keeps the hub as it was
  test.skip(testInfo.project.name !== "desktop", "the checklist flow runs on the desktop project only");
  await useVirtualAuthenticator(page);
  // localhost, not 127.0.0.1: the checklist's passkey ceremony follows, and the relying party is
  // the name
  await signIn(page, theSetup().pairOrigin);
  await openTab(page, "Sessions");

  // the seeded channel and session already tick two items
  const checklist = page.locator("[data-first-run]");
  await expect(checklist).toBeVisible();
  await expect(checklist.locator("[data-item='channel']")).toHaveAttribute("data-done", "true");
  await expect(checklist.locator("[data-item='session']")).toHaveAttribute("data-done", "true");
  await expect(checklist.locator("[data-item='totp']")).toHaveAttribute("data-done", "false");
  await expect(checklist.locator("[data-item='budget']")).toHaveAttribute("data-done", "false");

  // the passkey row stands down once this browser saved its own passkey
  await pairThroughBanner(page, `E2E Landing Mac ${test.info().project.name}`);
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
  await openTab(page, "Sessions");
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
  await page.locator("[data-signin]").getByRole("button", { name: "Remove" }).click();
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
