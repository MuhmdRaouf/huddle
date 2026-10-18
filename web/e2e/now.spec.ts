// now.spec.ts — the owner's first screen: the sign-in link lands the browser on the hub, and
// the Now list shows the session the run seeded into the registry with the main pane's dashed
// pointer beside it while none is picked, and one whose last event asks for permission waits in
// Needs you with its state word, behind the filter too.

import { expect, test } from "@playwright/test";
import { api, openTab, signIn, theSetup } from "./helpers.ts";

test("the sign-in link opens the hub and the Now list shows the seeded session", async ({
  page,
}) => {
  await signIn(page);
  await openTab(page, "Now");

  // the seeded session sits in the Working group, one row strong
  await expect(page.getByRole("heading", { name: "Working · 1" })).toBeVisible();
  await expect(page.getByText(theSetup().sessionTitle)).toBeVisible();
  // sessions stand and none is picked: the main pane keeps its pointer at the list
  await expect(page.getByText("No session open")).toBeVisible();
});

test("a session whose last event asks for permission waits in Needs you first", async ({
  page,
}) => {
  await signIn(page);
  await openTab(page, "Now");

  // a session reports a permission request and nothing after: the registry reads needs-you
  await api("/api/registry/report", {
    session_id: "e2e-permit",
    machine: "e2e-machine",
    project: "e2e",
    title: "Waits on a permission",
    last_event: "PermissionRequest",
    last_event_at: new Date().toISOString(),
  });
  await expect(page.getByRole("heading", { name: "Needs you · 1" })).toBeVisible();
  const card = page.locator("article[data-row='e2e-permit']");
  await expect(card).toBeVisible();
  await expect(card.getByRole("link", { name: /Waits on a permission/ })).toBeVisible();
  // the state rides as a word, not colour alone
  await expect(card.locator("[data-state='needs-you']")).toHaveText("needs you");

  // the filter holds the needs-you list alone, then gives the whole list back
  await page.getByRole("button", { name: "Needs you" }).click();
  await expect(page.getByRole("heading", { name: "Needs you · 1" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Working · 1" })).toBeHidden();
  await page.getByRole("button", { name: "Needs you" }).click();
  await expect(page.getByRole("heading", { name: "Working · 1" })).toBeVisible();
});
