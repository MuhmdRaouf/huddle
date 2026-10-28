// sessions-desk.spec.ts — the desk's three columns on #/sessions: at 1440 and 2000 px the tab
// lands on the row the list draws first (the address is replaced, so Back leaves Sessions), the
// 320 px list, the open session in the middle and the 300 px rail stand with no pane between and
// no vertical borders, the list and the rail paint the surface token while the middle keeps the
// background one, the chat column stays within 820 px, and no element reads "No session open".
// At phone width the list stands alone and Back from a session returns to it. Axe finds nothing
// serious on the landed desk.

import { expect, test } from "@playwright/test";
import { expectAccessible } from "./axe.ts";
import { api, openTab, signIn } from "./helpers.ts";

// the authenticator set-up the first sign-in offers is not this page's subject: the browser
// starts with its "Later" already kept
test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("huddle:totp-later", "true"));
});

// the desk needs a session that waits on the owner even when this file runs alone: one ask is
// reported before the tests and read back to idle after them
const ASK = "e2e-desk-ask";
test.beforeAll(async () => {
  await api("/api/registry/report", {
    session_id: ASK,
    machine: "e2e-machine",
    project: "e2e",
    title: "Desk ask",
    last_event: "PermissionRequest",
    last_event_at: new Date().toISOString(),
  });
});
test.afterAll(async () => {
  await api("/api/registry/report", {
    session_id: ASK,
    machine: "e2e-machine",
    project: "e2e",
    title: "Desk ask",
    last_event: "Stop",
    last_event_at: new Date().toISOString(),
  });
});

test("at desk widths Sessions lands on the list's first row in three columns, with no pane between", async ({
  page,
}, info) => {
  test.skip(info.project.name !== "desktop", "the desk is the desktop project's");
  await signIn(page);
  for (const width of [1440, 2000]) {
    await page.setViewportSize({ width, height: 900 });
    await page.evaluate(() => {
      location.hash = "#/sessions";
    });
    const pane = page.locator("[data-sessions-pane]");
    await expect(pane).toBeVisible();
    // a session is always open: no placeholder pane stands anywhere
    await expect(page.getByText("No session open")).toHaveCount(0);
    // the desk opens the row the list draws first, so the address carries that session
    const first = await pane.locator("a[data-row]").first().getAttribute("href");
    expect(first).not.toBeNull();
    expect(first).toMatch(/^#\/sessions\//);
    await expect(page.locator("[data-session]")).toBeVisible();
    expect(new URL(page.url()).hash).toBe(first);

    // the three columns: 320 px of list, the rest to the session, 300 px of rail
    await expect(page.locator("[data-now-context]")).toBeVisible();
    const list = await pane.boundingBox();
    const rail = await page.locator("[data-now-context]").boundingBox();
    const main = await page.locator("#main").boundingBox();
    expect(list).not.toBeNull();
    expect(rail).not.toBeNull();
    expect(main).not.toBeNull();
    expect(Math.abs(list!.width - 320)).toBeLessThanOrEqual(1);
    expect(Math.abs(rail!.width - 300)).toBeLessThanOrEqual(1);
    expect(Math.abs(list!.x + list!.width - main!.x)).toBeLessThanOrEqual(1);
    expect(Math.abs(main!.x + main!.width - rail!.x)).toBeLessThanOrEqual(1);

    // surfaces without borders: list and rail on the surface token, the middle on the background
    // one, no vertical border on any of the three
    const paint = await page.evaluate(() => {
      const probe = (v: string): string => {
        const el = document.createElement("div");
        el.style.background = v;
        document.body.appendChild(el);
        const c = getComputedStyle(el).backgroundColor;
        el.remove();
        return c;
      };
      const painted = (sel: string): string | null => {
        const el = document.querySelector(sel);
        return el === null ? null : getComputedStyle(el).backgroundColor;
      };
      const borders = (sel: string): [string, string] | null => {
        const el = document.querySelector(sel);
        if (el === null) return null;
        const s = getComputedStyle(el);
        return [s.borderLeftWidth, s.borderRightWidth];
      };
      return {
        surface: probe("var(--ui-surface)"),
        bg: probe("var(--ui-bg)"),
        list: painted("[data-sessions-pane]"),
        rail: painted("[data-now-context]"),
        main: painted("#main"),
        listBorders: borders("[data-sessions-pane]"),
        railBorders: borders("[data-now-context]"),
        mainBorders: borders("#main"),
      };
    });
    expect(paint.list).toBe(paint.surface);
    expect(paint.rail).toBe(paint.surface);
    expect(paint.main).toBe(paint.bg);
    const pairs: [string, [string, string] | null][] = [
      ["the list", paint.listBorders],
      ["the rail", paint.railBorders],
      ["the middle", paint.mainBorders],
    ];
    for (const [name, pair] of pairs) {
      expect(pair, `${name} stands without vertical borders`).toEqual(["0px", "0px"]);
    }

    // the chat column is at most 820 px wide and centred in the middle column
    const chat = await page.evaluate(() => {
      const stream = document.querySelector("[data-stream]");
      const inner = stream?.firstElementChild;
      if (!stream || !inner) return null;
      const s = stream.getBoundingClientRect();
      const i = inner.getBoundingClientRect();
      return { width: i.width, left: i.left - s.left, right: s.left + stream.clientWidth - i.right };
    });
    expect(chat).not.toBeNull();
    expect(chat!.width).toBeLessThanOrEqual(820);
    expect(Math.abs(chat!.left - chat!.right)).toBeLessThanOrEqual(2);
  }
});

test("at phone width the list stands alone and Back returns to it", async ({ page }, info) => {
  test.skip(info.project.name !== "desktop", "the phone width is set on the desktop project");
  await page.setViewportSize({ width: 393, height: 852 });
  await signIn(page);
  await openTab(page, "Sessions");
  await expect(page.locator("[data-sessions-pane]")).toBeVisible();
  expect(new URL(page.url()).hash).toBe("#/sessions");
  await page.locator("[data-sessions-pane] a[href^='#/sessions/']").first().click();
  await expect(page.locator("[data-session]")).toBeVisible();
  await page.goBack();
  await expect(page.locator("[data-sessions-pane]")).toBeVisible();
  expect(new URL(page.url()).hash).toBe("#/sessions");
});

test("the landed desk passes axe", async ({ page }, info) => {
  test.skip(info.project.name !== "desktop", "the desk is the desktop project's");
  await page.setViewportSize({ width: 1440, height: 900 });
  await signIn(page);
  await openTab(page, "Sessions");
  await expect(page.locator("[data-session]")).toBeVisible();
  await expectAccessible(page);
});
