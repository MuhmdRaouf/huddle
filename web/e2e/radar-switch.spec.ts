// radar-switch.spec.ts — switching radar views inside one document: the owner clicks the
// sub-tab row and the same document keeps exactly one app tree (one main#main) with the clicked
// view on screen. A second mount here doubles every id and live region the shell owns.

import { expect, test } from "@playwright/test";
import { openTab, signIn } from "./helpers.ts";

/** The views the run clicks through, each with the one thing only it shows once loaded. */
const SWITCHES: readonly (readonly [
  label: string,
  role: "heading" | "row",
  loaded: RegExp,
])[] = [
  ["Costs", "heading", /Cost attribution/],
  ["Requests", "row", /open details/],
];

test("switching radar views in one document keeps a single app tree", async ({ page }) => {
  await signIn(page);
  await openTab(page, "Radar");
  await expect(page.getByRole("heading", { name: "Top sessions by spend" })).toBeVisible();
  await expect(page.locator("main#main")).toHaveCount(1);

  for (const [label, role, loaded] of SWITCHES) {
    // a hash link: the same document repaints onto the view, no reload
    await page
      .getByRole("navigation", { name: "Radar views" })
      .getByRole("link", { name: label })
      .click();
    await expect(
      page.getByRole("navigation", { name: "Radar views" }).getByRole("link", { name: label }),
    ).toHaveAttribute("aria-current", "page");
    await expect(page.getByRole(role, { name: loaded }).first()).toBeVisible();
    await expect(page.locator("main#main")).toHaveCount(1);
  }
});
