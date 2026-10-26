// signin.spec.ts — the ways back in without a terminal: a paired browser signs the shell back
// in with one passkey touch from the landing page, and a browser signed in by link is offered a
// passkey of its own — the checklist's row mints the link, the passkey it saves signs the shell
// back in once its cookies are gone. The ceremonies run at the localhost origin, the name the
// relying party is.

import { expect, test, type Page } from "@playwright/test";
import {
  MAC_UA,
  openTab,
  pairThroughBanner,
  pairThroughLink,
  signIn,
  theSetup,
  useVirtualAuthenticator,
} from "./helpers.ts";

// a Mac desktop browser: the landing leads with the name the platform gives its own lock
test.use({ userAgent: MAC_UA });

/** The landing page a browser with no session lands on. */
async function expectSignedOut(page: Page): Promise<void> {
  await expect(page.locator("[data-landing]")).toBeVisible();
}

/** One passkey touch from the landing page: the discoverable credential, the finish, the
 *  reload that lands the shell. This browser paired here, so the landing leads with the
 *  biometric button the platform names. */
async function signInWithPasskey(page: Page): Promise<void> {
  await page.getByRole("button", { name: "Sign in with Touch ID" }).click();
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
}

test("a paired browser signs back in with one passkey touch", async ({ page }) => {
  const { pairOrigin } = theSetup();
  await useVirtualAuthenticator(page);
  // every spec and project pairs under its own name: the home keeps each device this run paired
  await pairThroughLink(page, `E2E Pad ${test.info().project.name}`);

  // the session cookie gone: the next visit is the landing page, and with a passkey saved in
  // this browser its first control is the biometric button
  await page.context().clearCookies();
  await page.goto(`${pairOrigin}/`);
  await expectSignedOut(page);
  await signInWithPasskey(page);

  // the renewed session survives a reload, still with no link
  await page.reload();
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
});

test("a link sign-in offers a passkey, and the saved one signs in after cookies are cleared", async ({
  page,
}) => {
  const { pairOrigin } = theSetup();
  await useVirtualAuthenticator(page);

  // the owner signs in by link: the Now list carries the checklist's passkey row
  await signIn(page, pairOrigin);
  await openTab(page, "Sessions");
  await expect(page.getByText("Save a passkey")).toBeVisible();

  // the banner mints its own link and runs the ceremony: no terminal, no pasted code
  await pairThroughBanner(page, `E2E Mac ${test.info().project.name}`);
  await expect(page.getByRole("button", { name: "Save passkey" })).toHaveCount(0);

  // cookies gone: the saved passkey is the way back in
  await page.context().clearCookies();
  await page.goto(`${pairOrigin}/`);
  await expectSignedOut(page);
  await signInWithPasskey(page);
});
