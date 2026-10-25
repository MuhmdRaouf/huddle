// totp.spec.ts — the authenticator's whole path over the real binary: a link sign-in on an
// un-enrolled hub offers the set-up before the shell (skippable with Later), Settings enrols it
// (the QR and the secret the page shows, the code computed here with RFC 6238 over node:crypto,
// the recovery codes the answer carries), the landing page signs a cookie-less browser in with
// a code, a recovery code works the same way, and a phone browser with a platform authenticator
// is offered its lock as a passkey whose Turn on makes the next sign-in one touch.

import { devices, expect, test, type Page } from "@playwright/test";
import {
  loginCode,
  signIn,
  theSetup,
  totpCode,
  totpSecret,
  typeCode,
  useVirtualAuthenticator,
} from "./helpers.ts";

/** What this run enrolled: the secret and the recovery codes, read off the set-up page the only
 *  time they show. One worker runs this file's tests in order, so the later tests read what the
 *  earlier ones captured. */
let secret = "";
let recovery: string[] = [];

/** Enrols the authenticator from the open Settings › Sign-in, capturing the secret and the codes. */
async function enrolFromSettings(page: Page): Promise<void> {
  await expect(page.getByText("Authenticator app")).toBeVisible();
  await page.getByRole("button", { name: "Set up" }).click();
  await expect(page.locator("[data-qr]")).toBeVisible();
  secret = await totpSecret(page);
  await typeCode(page, totpCode(secret));
  await expect(page.getByText("Save your recovery codes")).toBeVisible();
  recovery = (await page.locator("[data-recovery] li").allTextContents()).map((c) => c.trim());
  expect(recovery).toHaveLength(10);
  await page.getByRole("button", { name: "I saved them" }).click();
  await expect(page.getByText("A code from your app signs any browser in.")).toBeVisible();
}

test("a link sign-in offers the set-up, Settings enrols it, and a code signs the landing page in", async ({
  page,
}) => {
  const { pairOrigin } = theSetup();

  // the gate: no enrolment yet, so the link lands on the set-up before the shell; Later skips
  const code = await loginCode();
  await page.goto(`${pairOrigin}/?code=${code}`);
  await expect(page.getByText("Set up your authenticator app")).toBeVisible();
  await page.getByRole("button", { name: /Later/ }).click();
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();

  // Settings › Sign-in enrols, and the recovery codes show this one time
  await page.getByRole("navigation", { name: "Main" }).getByRole("link", { name: "Settings" }).click();
  await page.getByRole("link", { name: "Sign-in" }).click();
  await enrolFromSettings(page);

  // the landing page: cookies gone, the code field is the way in, the shell lands
  await page.context().clearCookies();
  await page.goto(`${pairOrigin}/`);
  await expect(page.getByText("Enter the code from your authenticator app")).toBeVisible();
  await typeCode(page, totpCode(secret));
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
  // a code sign-in may earn the biometrics offer on this desktop too: close it if it stood
  const notNow = page.getByRole("button", { name: "Not now" });
  if (await notNow.isVisible().catch(() => false)) await notNow.click();

  // a recovery code works the same way, once
  await page.context().clearCookies();
  await page.goto(`${pairOrigin}/`);
  await page.getByRole("button", { name: "Use a recovery code instead" }).click();
  await page.getByLabel("Recovery code").fill(recovery[0] ?? "");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
});

test("a phone browser is offered its lock as a passkey after a code sign-in", async ({ browser }) => {
  const { pairOrigin } = theSetup();

  // the phone: its own context with the iPhone's user agent and screen, and its own
  // authenticator — a second browser beside the desktop one, not a reconfigured first
  const phone = await browser.newContext({ ...devices["iPhone 15"] });
  const page = await phone.newPage();
  await useVirtualAuthenticator(page);

  // replace the enrolment so this test reads its own secret: remove (the link session's own
  // machine waives the step-up on loopback), then set up again
  await signIn(page, pairOrigin);
  await page.getByRole("navigation", { name: "Main" }).getByRole("link", { name: "Settings" }).click();
  await page.getByRole("link", { name: "Sign-in" }).click();
  await page.locator("[data-signin]").getByRole("button", { name: "Remove" }).click();
  await expect(page.getByText(/Not set up/)).toBeVisible();
  await enrolFromSettings(page);

  // the landing page on the phone: the code field, then the shell, then the offer
  await page.context().clearCookies();
  await page.goto(`${pairOrigin}/`);
  await expect(page.getByText("Enter the code from your authenticator app")).toBeVisible();
  await expect(page.locator("[data-landing]")).toBeVisible();
  await typeCode(page, totpCode(secret));
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();

  // the sheet: this browser holds no passkey and the platform has one to give
  await expect(page.getByText("Use Face ID next time?")).toBeVisible();
  await page.getByRole("button", { name: "Turn on" }).click();
  await expect(page.getByText("Use Face ID next time?")).toHaveCount(0);

  // the landing leads with the lock the platform names, and the code under it stays the fallback
  await page.context().clearCookies();
  await page.goto(`${pairOrigin}/`);
  await expect(page.getByRole("button", { name: "Sign in with Face ID" })).toBeVisible();
  await expect(page.getByText("Or enter your authenticator code")).toBeVisible();

  // the fallback runs in a browser that holds no passkey of its own here (the field's autofill
  // offer is what a saved passkey earns): the code field is the way in, and a code signs in — the
  // step past the one the first code spent, a code being one use. The hub's one-step skew accepts
  // that code the moment the first is spent, so no step boundary is waited out
  await page.evaluate(() => localStorage.removeItem("huddle:passkey-here"));
  await page.context().clearCookies();
  await page.goto(`${pairOrigin}/`);
  await expect(page.getByText("Enter the code from your authenticator app")).toBeVisible();
  await typeCode(page, totpCode(secret, 1));
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();

  // and one touch is the next sign-in, the mark its own sign-in leaves in this browser
  await page.evaluate(() => localStorage.setItem("huddle:passkey-here", "1"));
  await page.context().clearCookies();
  await page.goto(`${pairOrigin}/`);
  await page.getByRole("button", { name: "Sign in with Face ID" }).click();
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
  await phone.close();
});
