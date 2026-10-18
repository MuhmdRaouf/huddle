// landing.spec.ts — the owner's first screen when not signed in, over the real binary: a fresh
// hub shows the first-time card with no warning, the tour's thumbnails and lightbox, and the
// start-a-session tabs whose commands copy; a browser that paired here lands on the biometric
// button Touch ID names; the first-run checklist on Now ticks its items from server state and
// stands down when all four hold; and `huddle open` on a hub-role home prints the hub's own
// https link, which a browser opens over TLS and lands signed in.

import { expect, test } from "@playwright/test";
import { execFileSync } from "node:child_process";
import {
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
    .toContain("/plugin marketplace add MuhmdRaouf/claude-code-plugins");
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
