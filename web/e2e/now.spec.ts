// now.spec.ts — the owner's first screen: the sign-in link lands the browser on the hub, and
// the Now list shows the session the run seeded into the registry.

import { expect, test } from "@playwright/test";
import { openTab, signIn, theSetup } from "./helpers.ts";

test("the sign-in link opens the hub and the Now list shows the seeded session", async ({
  page,
}) => {
  await signIn(page);
  await openTab(page, "Now");

  // the seeded session sits in the Working group, one row strong
  await expect(page.getByRole("heading", { name: "Working · 1" })).toBeVisible();
  await expect(page.getByText(theSetup().sessionTitle)).toBeVisible();
});
