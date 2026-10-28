// sessions-list.spec.ts — the desk's Sessions header: the title and the Select toggle stand on
// one line, and no checkbox shows on a row until Select is pressed; the search box and the
// machine chips sit above the list, the chips on one line, and a typed word narrows the rows;
// the set-up row shows while checklist steps are left and opens the checklist in its sheet;
// at phone width the list scrolls nothing sideways. Axe finds nothing serious on either width.

import { expect, test } from "@playwright/test";
import { expectAccessible } from "./axe.ts";
import { api, openTab, signIn } from "./helpers.ts";

// the authenticator set-up the first sign-in offers is not this page's subject: the browser
// starts with its "Later" already kept
test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("huddle:totp-later", "true"));
});

test("the desk header holds Sessions and Select on one line, and no checkbox shows until Select", async ({
  page,
}, info) => {
  test.skip(info.project.name !== "desktop", "the desk header is the desktop project's");
  await page.setViewportSize({ width: 1440, height: 900 });
  await signIn(page);
  await openTab(page, "Sessions");

  const header = page.locator("[data-sessions-pane] header");
  await expect(header.getByRole("heading", { name: "Sessions" })).toBeVisible();
  const toggle = header.getByRole("button", { name: "Select" });
  await expect(toggle).toBeVisible();
  // one line: the title's band and the button's band overlap
  const title = await header.getByRole("heading").boundingBox();
  const btn = await toggle.boundingBox();
  expect(title).not.toBeNull();
  expect(btn).not.toBeNull();
  const gap = Math.abs((title!.y + title!.height / 2) - (btn!.y + btn!.height / 2));
  expect(gap).toBeLessThanOrEqual(Math.max(title!.height, btn!.height) / 2);

  // no checkbox until Select is pressed; then one per row and a pick works
  expect(await page.locator("[data-sessions-pane] input[type='checkbox']").count()).toBe(0);
  await toggle.click();
  await expect(page.getByRole("button", { name: "Done" })).toBeVisible();
  const boxes = page.locator("[data-sessions-pane] [data-select-row] input[type='checkbox']");
  const rows = page.locator("[data-sessions-pane] [data-select-row]");
  expect(await boxes.count()).toBe(await rows.count());
  expect(await boxes.count()).toBeGreaterThan(0);
  await expectAccessible(page);
});

test("the machine chips hold one line at 1440, and the search narrows the rows", async ({
  page,
}, info) => {
  test.skip(info.project.name !== "desktop", "the desk's one-line chips are the desktop project's");
  await page.setViewportSize({ width: 1440, height: 900 });
  await signIn(page);
  await openTab(page, "Sessions");
  // the run starts on an empty registry: report one session so the list and the chips have a
  // machine to name
  await api("/api/registry/report", {
    session_id: "e2e-chips",
    machine: "e2e-chips-machine",
    project: "e2e",
    title: "Chips hold one line",
    last_event: "Stop",
    last_event_at: new Date().toISOString(),
  });
  await expect(page.locator("[data-row='e2e-chips']")).toBeVisible();

  // one line: the chips row is no taller than one of its chips
  const chipsRow = page.locator("[data-machine-chips]");
  await expect(chipsRow).toBeVisible();
  await expect(chipsRow.getByRole("button", { name: "All" })).toBeVisible();
  await expect(chipsRow.getByRole("button", { name: "e2e-chips-machine" })).toBeVisible();
  const rowBox = await chipsRow.boundingBox();
  const chipBox = await chipsRow.locator("button").first().boundingBox();
  expect(rowBox).not.toBeNull();
  expect(chipBox).not.toBeNull();
  expect(rowBox!.height - chipBox!.height).toBeLessThanOrEqual(1);

  // a typed word narrows the list to nothing, and clearing it gives the rows back
  const pane = page.locator("[data-sessions-pane]");
  const before = await pane.locator("[data-row]").count();
  expect(before).toBeGreaterThan(0);
  const search = page.getByRole("searchbox", { name: "Search sessions" });
  await search.fill("no session matches this");
  expect(await pane.locator("[data-row]").count()).toBe(0);
  await search.fill("");
  expect(await pane.locator("[data-row]").count()).toBe(before);
  await expectAccessible(page);
});

test("a desk row ellipsizes a 30-character title and no row holds a Review button", async ({
  page,
}, info) => {
  test.skip(info.project.name !== "desktop", "the desk rows are the desktop project's");
  await page.setViewportSize({ width: 1440, height: 900 });
  await signIn(page);
  await openTab(page, "Sessions");
  const title = "Kanban board export queue jobs"; // exactly 30 characters
  expect(title.length).toBe(30);
  await api("/api/registry/report", {
    session_id: "e2e-title-30",
    machine: "e2e-machine",
    project: "e2e",
    title,
    last_event: "Stop",
    last_event_at: new Date().toISOString(),
  });
  // one row holds an open permission ask: on the desk it too is a plain row, so no row
  // carries the Review button the card kept off the list. The session stopped, so the
  // denial at the end moves it to Idle and the other cases' counts stay as they were —
  // the hub serves every project of the run, this ask included.
  await api("/api/registry/report", {
    session_id: "e2e-title-ask",
    machine: "e2e-machine",
    project: "e2e",
    title: "Ask holds",
    last_event: "Stop",
    last_event_at: new Date().toISOString(),
  });
  const ask = await api<{ id: number }>("/api/needs", {
    session_id: "e2e-title-ask",
    tool_name: "Bash",
    tool_input: { command: "bun run test" },
  });

  const row = page.locator("[data-row='e2e-title-30']");
  await expect(row).toBeVisible();
  const name = row.getByText(title, { exact: true });
  await expect(name).toBeVisible();
  // the mock's rows clip: the title ellipsizes inside its line beside the harness chip instead
  // of widening the row, so the clip is the assert and the row scrolls nothing sideways
  const clip = await name.evaluate((el) => {
    const s = getComputedStyle(el);
    const host = el.closest("[data-row]") as HTMLElement;
    return {
      overflow: s.overflowX,
      ellipsis: s.textOverflow,
      inside: el.getBoundingClientRect().right <= host.getBoundingClientRect().right + 0.5,
      rowScroll: host.scrollWidth - host.clientWidth,
    };
  });
  expect(clip.overflow, "the title clips").toBe("hidden");
  expect(clip.ellipsis, "the title draws an ellipsis").toBe("ellipsis");
  expect(clip.inside, "the title stays inside its row").toBe(true);
  expect(clip.rowScroll, "the row scrolls nothing sideways").toBeLessThanOrEqual(0);
  // every row opens its session; no row answers or reviews an ask itself
  expect(await page.locator("[data-sessions-pane] [data-row] [data-needs-actions]").count()).toBe(0);
  expect(await page.locator("[data-sessions-pane] [data-row] [data-act='review']").count()).toBe(0);
  await expectAccessible(page);

  // the denial settles the ask's session back among the idle ones
  await api(`/api/needs/${ask.id}/decide`, { decision: "deny" });
  await expect(page.locator("[data-row='e2e-title-ask'] [data-state='idle']")).toHaveText("Idle");
});

test("at phone width the Sessions list scrolls nothing sideways", async ({ page }) => {
  await page.setViewportSize({ width: 393, height: 852 });
  await signIn(page);
  await openTab(page, "Sessions");
  const over = await page.evaluate(
    () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
  );
  expect(over).toBeLessThanOrEqual(0);
  await expectAccessible(page);
});

test("the set-up row shows while checklist steps are left and opens the checklist in a sheet", async ({
  page,
}) => {
  await page.setViewportSize({ width: 393, height: 852 });
  await signIn(page);
  await openTab(page, "Sessions");
  // steps are left on a fresh browser: the row carries the count and opens the sheet
  const setup = page.getByRole("button", { name: /^Set up · \d+ steps? left$/ });
  await expect(setup).toBeVisible();
  await setup.click();
  const sheet = page.locator("dialog[open].ui-sheet");
  await expect(sheet.locator("[data-first-run]")).toBeVisible();
  // closing the sheet gives the row back: the steps are still left
  await sheet.getByRole("button", { name: "Close" }).click();
  await expect(sheet).toHaveCount(0);
  await expect(setup).toBeVisible();
  await expectAccessible(page);
});

test("at phone width the needs card's preview wraps its command and never shows the raw JSON", async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await signIn(page);
  await openTab(page, "Sessions");
  // a stopped session whose hook asked to run a ten-line command
  await api("/api/registry/report", {
    session_id: "e2e-card-preview",
    machine: "e2e-machine",
    project: "e2e",
    title: "Card preview",
    last_event: "Stop",
    last_event_at: new Date().toISOString(),
  });
  const command = Array.from({ length: 10 }, (_, i) => `echo step-${i + 1}`).join("\n");
  const ask = await api<{ id: number }>("/api/needs", {
    session_id: "e2e-card-preview",
    tool_name: "Bash",
    tool_input: { command, description: "Run the ten steps" },
  });
  const card = page.locator("[data-row='e2e-card-preview']");
  await expect(card).toBeVisible();
  const preview = card.locator("[data-ask-preview]");
  await expect(preview).toBeVisible();
  const fit = await preview.evaluate((el) => ({
    scroll: el.scrollWidth,
    client: el.clientWidth,
    text: el.textContent ?? "",
  }));
  expect(fit.scroll, "the preview scrolls sideways").toBeLessThanOrEqual(fit.client);
  expect(fit.text, "the card shows the raw JSON").not.toContain('{"');
  expect(fit.text).toContain("+6 lines");
  // this project's ask leaves with the run: the other specs count the needs-you items
  await api(`/api/needs/${ask.id}/decide`, { decision: "deny" });
  await expect(card.locator("[data-ask-preview]")).toHaveCount(0);
});
