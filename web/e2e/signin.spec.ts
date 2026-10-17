// signin.spec.ts — the ways back in without a terminal: a paired browser signs the shell back
// in with one passkey touch from the signed-out page, and a browser signed in by link is
// offered a passkey of its own — the banner mints the link, the passkey it saves signs the
// shell back in once its cookies are gone. The ceremonies run at the localhost origin, the name
// the relying party is.

import { expect, test, type Page } from "@playwright/test";
import { pairThroughBanner, pairThroughLink, signIn, theSetup, useVirtualAuthenticator } from "./helpers.ts";

/** The signed-out page a browser with no session lands on. */
async function expectSignedOut(page: Page): Promise<void> {
  await expect(page.getByText("Sign in to see your channels and sessions")).toBeVisible();
}

/** One passkey touch from the signed-out page: the discoverable credential, the finish, the
 *  reload that lands the shell. */
async function signInWithPasskey(page: Page): Promise<void> {
  await page.getByRole("button", { name: "Sign in with a passkey" }).click();
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
}

test("a paired browser signs back in with one passkey touch", async ({ page }) => {
  const { pairOrigin } = theSetup();
  await useVirtualAuthenticator(page);
  // every spec pairs under its own name: the home keeps each device this run paired
  await pairThroughLink(page, "E2E Pad");

  // the session cookie gone: the next visit is the signed-out page, and with a device paired
  // its first control is the passkey button
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

  // the owner signs in by link: the shell carries the banner
  await signIn(page, pairOrigin);
  await expect(
    page.getByText("Save a passkey for this browser — next time Touch ID signs you in."),
  ).toBeVisible();

  // the banner mints its own link and runs the ceremony: no terminal, no pasted code
  await pairThroughBanner(page, "E2E Mac");
  await expect(page.getByRole("button", { name: "Save passkey" })).toHaveCount(0);

  // cookies gone: the saved passkey is the way back in
  await page.context().clearCookies();
  await page.goto(`${pairOrigin}/`);
  await expectSignedOut(page);
  await signInWithPasskey(page);
});
