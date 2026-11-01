// session-transcript.spec.ts — the session page paints the session's own transcript: its three
// prompts and replies as text, the tool calls between them as folded runs that open to their
// calls, on the desktop and the phone. A line the session adds to its transcript while the page
// is open sits under a New pill and moves nothing: the scroll position stays, and a tap on the
// pill goes to the new line.

import { expect, test } from "@playwright/test";
import { expectAccessible } from "./axe.ts";
import { api, openTab, replyLine, seedTranscriptSession, signIn } from "./helpers.ts";

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("huddle:totp-later", "true"));
});

test("the page shows the transcript's prompts and folded runs, and a new line waits under a New pill", async ({
  page,
}, info) => {
  const session = `e2e-transcript-${info.project.name}`;
  const transcript = await seedTranscriptSession(session, "Moves the nightly backup");
  // a short window, so the transcript is taller than the page and scrolling means something
  await page.setViewportSize(
    info.project.name === "desktop" ? { width: 1440, height: 520 } : { width: 390, height: 520 },
  );
  await signIn(page);
  await openTab(page, "Sessions");
  await page.locator(`a[href='#/sessions/${session}']`).first().click();

  const pane = page.locator("[data-block='transcript']");
  await expect(pane).toBeVisible();
  // the three prompts, as the owner wrote them
  await expect(pane.getByText("Move the nightly backup to pve2", { exact: true })).toBeAttached();
  await expect(pane.getByText("Check how big the first copy is", { exact: true })).toBeAttached();
  await expect(pane.getByText("Keep 14 days of copies", { exact: true })).toBeAttached();
  // the page opened at its end: the newest reply is in view
  const newest = pane.getByText(/Done\. The job keeps 14 daily copies/);
  await expect(newest).toBeInViewport();
  expect(await pane.evaluate((el) => el.scrollHeight - el.scrollTop - el.clientHeight)).toBeLessThanOrEqual(8);

  // nine calls fold into two runs, each one row
  const runs = pane.locator("[data-run]");
  await expect(runs).toHaveCount(2);
  const first = runs.first();
  await expect(first).toContainText("Ran 5 reads and searches, 2 commands");
  await expect(first).toContainText("last: grep -r backup /etc/cron.d/");
  await expect(first.locator("[data-call]")).toHaveCount(0);
  // the row's words are never clipped: they wrap before they cut, and only the target gives way
  const words = first.getByText("Ran 5 reads and searches, 2 commands", { exact: true });
  const fit = await words.evaluate((el) => {
    const row = el.closest("[data-run]")?.getBoundingClientRect();
    const box = el.getBoundingClientRect();
    return { clipped: el.scrollWidth > el.clientWidth, spill: row ? box.right - row.right : 0 };
  });
  expect(fit.clipped, "the run row's words are clipped").toBe(false);
  expect(fit.spill, "the run row's words reach past the row").toBeLessThanOrEqual(0);
  const toggle = first.getByRole("button");
  expect((await toggle.boundingBox())?.height).toBeGreaterThanOrEqual(44);
  await toggle.click();
  await expect(first.locator("[data-call]")).toHaveCount(7);
  await expect(first.locator("[data-call]").first()).toContainText("/etc/cron.d/backup-nightly");
  await toggle.click();
  await expect(first.locator("[data-call]")).toHaveCount(0);
  await expect(runs.nth(1)).toContainText("Ran 2 commands");

  // the reader scrolls up; a line the session adds waits under a New pill and moves nothing
  await pane.evaluate((el) => {
    el.scrollTop = 0;
  });
  const before = await pane.evaluate((el) => el.scrollTop);
  transcript.append(replyLine(session, "t-900", "The next run starts at 02:00."));
  const pill = pane.getByRole("button", { name: "New" });
  await expect(pill).toBeAttached();
  await expect(pane.getByText("The next run starts at 02:00.")).toBeAttached();
  expect(await pane.evaluate((el) => el.scrollTop)).toBe(before);
  await page.waitForTimeout(500);
  expect(await pane.evaluate((el) => el.scrollTop)).toBe(before);

  // a tap goes to the new line, and reading it clears the pill
  await pill.evaluate((el) => (el as HTMLElement).click());
  await expect(pane.getByText("The next run starts at 02:00.")).toBeInViewport();
  await expect(pane.locator("[data-new]")).toHaveCount(0);
  await expectAccessible(page);

  // the session ends so the Now list's counts stay as the other cases read them
  await api("/api/registry/report", {
    session_id: session,
    machine: "e2e-machine",
    project: "e2e",
    title: "Moves the nightly backup",
    last_event: "SessionEnd",
    last_event_at: new Date().toISOString(),
  });
});
