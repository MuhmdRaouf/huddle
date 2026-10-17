// kanban.spec.ts — the Kanban tab over the seeded board: the picker lists it, and the three
// seeded cards sit in the columns the folders put them in.

import { expect, test } from "@playwright/test";
import { openTab, signIn, theSetup } from "./helpers.ts";

/** One column: its heading text and the card the fixture put in it. */
const COLUMNS: readonly (readonly [heading: string, id: string, title: string])[] = [
  ["Backlog 1", "E2E-1", "First card"],
  ["Ready 1", "E2E-2", "Second card"],
  ["In Progress 1", "E2E-3", "Third card"],
];

test("the kanban tab lists the board and its three cards in the right columns", async ({
  page,
}) => {
  await signIn(page);
  await openTab(page, "Kanban");

  // the picker names the seeded board (the project folder the registry session ran in)
  const picker = page.getByLabel("Board", { exact: true });
  await expect(picker).toBeVisible();
  await expect(picker).toContainText(theSetup().boardName);

  // each column holds exactly its own card
  for (const [heading, id, title] of COLUMNS) {
    const column = page.locator("section").filter({
      has: page.getByRole("heading", { name: heading }),
    });
    await expect(column).toHaveCount(1);
    await expect(column.getByRole("button", { name: `${id}: ${title}` })).toBeVisible();
  }
});

test("the Holder menu filters the board to a pick, with a pill and a Clear all", async ({ page }) => {
  await signIn(page);
  await openTab(page, "Kanban");
  await expect(page.getByRole("button", { name: "E2E-1: First card" })).toBeVisible();

  // open the Holder menu and pick the seeded holder
  await page.getByRole("button", { name: "Holder", exact: true }).click();
  await page.getByRole("menuitemcheckbox", { name: "e2e-agent 1" }).click();

  // the pick shows as a pill, and only the held card stays on the board
  const pill = page.getByRole("button", { name: "Remove Holder e2e-agent" });
  await expect(pill).toBeVisible();
  await expect(page.getByRole("button", { name: "E2E-3: Third card" })).toBeVisible();
  await expect(page.getByRole("button", { name: "E2E-1: First card" })).toHaveCount(0);

  // Clear all drops the pill and the board comes back whole
  await page.getByRole("button", { name: "Clear all" }).click();
  await expect(pill).toHaveCount(0);
  await expect(page.getByRole("button", { name: "E2E-1: First card" })).toBeVisible();
  await expect(page.getByRole("button", { name: "E2E-3: Third card" })).toBeVisible();
});
