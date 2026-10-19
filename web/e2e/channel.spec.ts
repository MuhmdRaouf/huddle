// channel.spec.ts — the page a Channels card opens: the Messages card lists the run's seeded
// ping and its pong newest last, each with its sender, its recipient, the body and the age.

import { expect, test } from "@playwright/test";
import { openTab, signIn } from "./helpers.ts";

test("the channel page lists the seeded ping and pong with who told whom", async ({ page }) => {
  await signIn(page);
  await openTab(page, "Channels");
  await page.locator('a[data-ch="e2e"]').click();

  const messages = page.locator("[data-messages]");
  await expect(messages.getByRole("heading", { name: "Messages" })).toBeVisible();
  const rows = messages.locator("[data-seq]");
  await expect(rows).toHaveCount(2);

  // newest last: the older ping opens the list, the pong that answers it sits under it
  const ping = rows.nth(0);
  const pong = rows.nth(1);
  await expect(ping).toContainText("alpha");
  await expect(ping).toContainText("beta");
  await expect(ping).toContainText("ping");
  await expect(pong).toContainText("beta");
  await expect(pong).toContainText("alpha");
  await expect(pong).toContainText("pong");
  // each row carries its age as a time element
  await expect(ping.locator("time")).toBeVisible();
  await expect(pong.locator("time")).toBeVisible();
});
