// now.spec.ts — the owner's first screen: the sign-in link lands the browser on the hub, and
// the Now list shows the session the run seeded into the registry with the main pane's dashed
// pointer beside it while none is picked, one whose last event asks for permission waits in
// Needs you with its state word, behind the filter too, and an agent-team teammate that went idle
// shows under its lead's row with the word idle.

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

test("a teammate that went idle shows under its lead's row", async ({ page }) => {
  await signIn(page);
  await openTab(page, "Now");

  // the lead's TeammateIdle hook reports the teammate under the lead's session, then the lead's
  // own turn ends: the teammate stays listed under the idle lead (and the Working count the
  // other cases read stays as it was)
  const lead = { session_id: "e2e-lead", machine: "e2e-machine", project: "e2e", title: "Leads a team" };
  await api("/api/registry/report", {
    ...lead,
    teammate: { name: "researcher", state: "idle" },
    last_event: "TeammateIdle",
    last_event_at: new Date().toISOString(),
  });
  await api("/api/registry/report", { ...lead, last_event: "Stop", last_event_at: new Date().toISOString() });
  const row = page.locator("[data-select-row='e2e-lead'], [data-hold-row='e2e-lead']");
  await expect(row.getByText("Leads a team")).toBeVisible();
  const mate = row.locator("[data-teammate='researcher']");
  await expect(mate).toBeVisible();
  await expect(mate).toContainText("researcher");
  // the teammate's state rides as a word
  await expect(mate.locator("[data-state='idle']")).toHaveText("idle");
});
