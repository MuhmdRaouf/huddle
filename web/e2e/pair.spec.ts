// pair.spec.ts — pairing a device the way the owner does it: the link `huddle pair` prints
// opens on the device, the name is confirmed, the code on the screen matches the terminal's,
// and the passkey is created against a virtual authenticator. The paired device then shows in
// Settings, and a browser that never paired stays on the landing page.

import { expect, test } from "@playwright/test";
import { openTab, pairThroughLink, theSetup, useVirtualAuthenticator } from "./helpers.ts";

const DEVICE = "E2E Phone";

test("a pair link pairs the device with a passkey and it appears in Settings", async ({
  page,
}) => {
  await useVirtualAuthenticator(page);
  await pairThroughLink(page, DEVICE);

  // the device is listed for removal; the count is whatever this run's earlier specs paired
  await openTab(page, "Settings");
  await page
    .getByRole("navigation", { name: "Settings sections" })
    .getByRole("link", { name: "Security" })
    .click();
  await expect(page.getByRole("heading", { name: /^Devices · \d+$/ })).toBeVisible();
  await expect(page.getByText(DEVICE, { exact: true })).toBeVisible();

  // the session the pairing earned survives a fresh load, no link needed
  await page.reload();
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
});

test("an unpaired browser stays signed out", async ({ page }) => {
  await theSetup(); // the server is up; this browser simply holds no credential
  await page.goto(`${theSetup().rootUrl}/`);
  await expect(page.locator("[data-landing]")).toBeVisible();
  await expect(page.getByRole("navigation", { name: "Main" })).toHaveCount(0);
  // the tour stands beside the sign-in card whether the home holds a passkey or not
  await expect(page.getByRole("heading", { name: "See how it works" })).toBeVisible();
});
