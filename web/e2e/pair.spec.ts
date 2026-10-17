// pair.spec.ts — pairing a device the way the owner does it: the link `huddle pair` prints
// opens on the device, the name is confirmed, the code on the screen matches the terminal's,
// and the passkey is created against a virtual authenticator. The paired device then shows in
// Settings.

import { expect, test, type CDPSession, type Page } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { openTab, theSetup } from "./helpers.ts";
import { repoRoot, runDir } from "./paths.ts";

const DEVICE = "E2E Phone";

/** The run's environment: the temp homes the server runs on, so `huddle pair` mints its link
 *  into the same hub database the server spends it from. */
function pairEnv(): NodeJS.ProcessEnv {
  return {
    ...process.env,
    HOME: `${runDir}/home`,
    HUDDLE_HOME: `${runDir}/huddle-home`,
    RADAR_HOME: `${runDir}/radar-home`,
    CLAUDE_CONFIG_DIR: `${runDir}/claude-config`,
  };
}

/** Runs `huddle pair` and answers the link and the code it printed. */
function mintPairLink(): { link: string; code: string } {
  const out = execFileSync(`${runDir}/bin/huddle`, ["pair", "--name", DEVICE], {
    cwd: repoRoot,
    env: pairEnv(),
    encoding: "utf8",
  });
  const link = /http:\/\/localhost:\d+\/pair#[A-Z2-7]{26}/.exec(out)?.[0];
  const code = /code ([A-Z2-7]{3}-[A-Z2-7]{3}) /.exec(out)?.[1];
  if (link === undefined || code === undefined) {
    throw new Error(`huddle pair printed no link or code:\n${out}`);
  }
  return { link, code };
}

/** Arms a virtual platform authenticator on the page, the way a phone's passkey works. */
async function useVirtualAuthenticator(page: Page): Promise<CDPSession> {
  const cdp = await page.context().newCDPSession(page);
  await cdp.send("WebAuthn.enable");
  await cdp.send("WebAuthn.addVirtualAuthenticator", {
    options: {
      protocol: "ctap2",
      transport: "internal",
      hasResidentKey: true,
      hasUserVerification: true,
      isUserVerified: true,
    },
  });
  return cdp;
}

test("a pair link pairs the device with a passkey and it appears in Settings", async ({
  page,
}) => {
  const { link, code } = mintPairLink();
  await useVirtualAuthenticator(page);

  // the name stage: the link's token rode the fragment, the device names itself
  await page.goto(link);
  const name = page.getByLabel("Device name");
  await expect(name).toBeVisible();
  await name.fill(DEVICE);
  await page.getByRole("button", { name: "Continue" }).click();

  // the code stage: both sides show one code, then the passkey is created
  await expect(page.getByText("Pairing code")).toBeVisible();
  await expect(page.getByText(code)).toBeVisible();
  await page.getByRole("button", { name: "It matches" }).click();

  // the passkey is in: the pairing screen hands the shell over, on the Now tab
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();

  // the device is listed for removal
  await openTab(page, "Settings");
  await page
    .getByRole("navigation", { name: "Settings sections" })
    .getByRole("link", { name: "Devices" })
    .click();
  await expect(page.getByRole("heading", { name: "Devices · 1" })).toBeVisible();
  await expect(page.getByText(DEVICE)).toBeVisible();

  // the session the pairing earned survives a fresh load, no link needed
  await page.reload();
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
});

test("an unpaired browser stays signed out", async ({ page }) => {
  await theSetup(); // the server is up; this browser simply holds no credential
  await page.goto(`${theSetup().rootUrl}/`);
  await expect(page.getByText("Sign in to see your channels and sessions")).toBeVisible();
});
