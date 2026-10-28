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

test("the list's rows draw the mock's geometry at the desk and at phone width", async ({ page }, info) => {
  test.skip(info.project.name !== "desktop", "the phone width is set on the desktop project");
  await page.setViewportSize({ width: 1440, height: 900 });
  await signIn(page);
  await page.evaluate(() => {
    location.hash = "#/sessions";
  });
  const pane = page.locator("[data-sessions-pane]");
  await expect(pane).toBeVisible();
  // every row: 56 px tall or more, its title at the row's left edge, its chips inside the column
  const desk = await page.evaluate(() => {
    const list = document.querySelector("[data-sessions-pane]");
    if (!list) return ["no list"];
    const bad: string[] = [];
    for (const row of list.querySelectorAll("a[data-row]")) {
      const r = row.getBoundingClientRect();
      const id = row.getAttribute("data-row");
      if (r.height < 56) bad.push(`${id} is ${r.height}px tall`);
      const title = [...row.children].find((el) => el.tagName === "SPAN");
      if (title) {
        const t = title.getBoundingClientRect();
        if (t.left - r.left > 20) bad.push(`${id}'s title indents ${Math.round(t.left - r.left)}px`);
      }
      for (const chip of row.querySelectorAll("[data-harness], [data-state], [data-hooks-stale]")) {
        if (chip.getBoundingClientRect().right > r.right + 0.5) bad.push(`${id}'s chip passes the row's edge`);
      }
    }
    const paneRight = list.getBoundingClientRect().right;
    for (const chip of list.querySelectorAll("[data-machine-chip]")) {
      if (chip.getBoundingClientRect().right > paneRight + 0.5) {
        bad.push(`machine chip ${chip.textContent} passes the column`);
      }
    }
    return bad;
  });
  expect(desk).toEqual([]);
  // the row the desk lands on wears the mock's accent-soft fill: the utility must beat the
  // unlayered .ui-list-row background, in either scheme
  const landedFill = await page.evaluate(
    () => getComputedStyle(document.querySelector(".ui-list-row[aria-current='page']")!).backgroundColor,
  );
  expect(landedFill, "the landed row wears the accent fill").not.toBe("rgba(0, 0, 0, 0)");
  // the machine chips are the mock's 28 px tall
  const chipHeights = await page.evaluate(() =>
    [...document.querySelectorAll("[data-machine-chip]")].map((c) => c.getBoundingClientRect().height),
  );
  expect(chipHeights.length).toBeGreaterThan(0);
  for (const h of chipHeights) expect(Math.abs(h - 28), "machine chip height").toBeLessThanOrEqual(1);
  // the search field carries the mock's leading magnifier and stands the desk's 40 px
  const deskSearch = await page.evaluate(() => {
    const box = document.querySelector("[data-session-search]");
    const input = box?.querySelector("input");
    const icon = box?.querySelector("svg");
    return {
      icon: icon !== null,
      clear: icon !== null && getComputedStyle(icon).pointerEvents === "none",
      height: input?.getBoundingClientRect().height ?? 0,
      padLeft: input ? getComputedStyle(input).paddingLeft : "",
    };
  });
  expect(deskSearch.icon, "the desk search wears the magnifier").toBe(true);
  expect(deskSearch.clear, "the magnifier never takes the pointer").toBe(true);
  expect(Math.abs(deskSearch.height - 40), "desk search height").toBeLessThanOrEqual(1);
  expect(parseFloat(deskSearch.padLeft)).toBeGreaterThanOrEqual(36);

  // at phone width the same list shows no horizontal overflow and its titles align; the desk
  // landed on a session's address, and at phone width that address is the detail view, so the
  // run goes back to the list first — after the phone layout has taken over, or the desk's
  // landing effect replaces the address while the reset is still in flight
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(page.locator("[data-session]")).toBeVisible();
  await page.evaluate(() => {
    location.hash = "#/sessions";
  });
  await expect(pane.locator("a[data-row]").first()).toBeVisible();
  const phone = await page.evaluate(() => {
    const doc = document.documentElement;
    const titles = [...document.querySelectorAll("[data-sessions-pane] a[data-row]")].map((row) => {
      const r = row.getBoundingClientRect();
      const kid = [...row.children].find((el) => el.tagName === "SPAN");
      return kid ? kid.getBoundingClientRect().left - r.left : 0;
    });
    return { overflow: doc.scrollWidth - doc.clientWidth, spread: Math.max(...titles) - Math.min(...titles) };
  });
  expect(phone.overflow, "no horizontal overflow").toBe(0);
  expect(phone.spread, "titles align").toBeLessThanOrEqual(2);
  // the phone's search stands the mock's 48 px
  const phoneSearch = await page.evaluate(() =>
    document.querySelector("[data-session-search] input")?.getBoundingClientRect().height ?? 0,
  );
  expect(Math.abs(phoneSearch - 48), "phone search height").toBeLessThanOrEqual(1);
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
