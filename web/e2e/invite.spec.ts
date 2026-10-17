// invite.spec.ts — the connect card's invite: the button creates a token over POST /api/tokens
// and the card shows the join line the other session runs, naming this host and the new token.

import { expect, test } from "@playwright/test";
import { openTab, signIn, theSetup } from "./helpers.ts";

test("the invite card creates a token and shows its join line", async ({ page }) => {
  await signIn(page);
  await openTab(page, "Channels");

  const invite = page.getByRole("button", { name: "Invite a session" });
  await expect(invite).toBeVisible();
  await invite.click();

  // the answer names the channel, its expiry, and the join line with a fresh token
  await expect(page.getByRole("button", { name: /Copy Join line for e2e/ })).toBeVisible();
  const line = page.locator("pre").filter({ hasText: "/huddle:join " });
  await expect(line).toHaveCount(1);
  const text = (await line.textContent()) ?? "";
  expect(text).toMatch(
    new RegExp(`/huddle:join ${theSetup().host} --token [a-z0-9]{6}\\.[a-z0-9]{16}`),
  );
});
