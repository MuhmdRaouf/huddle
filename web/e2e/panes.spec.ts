// panes.spec.ts — one URL, one item, every layout: a session opens from #/sessions/<id> and from
// #/home/<id> with the same title at 1440, 820 and 390 px, and the shell draws two panes (the
// tab's list beside the item) on the desk and the tablet and one pane on the phone. An open
// channel follows the same pane rule.

import { expect, test } from "@playwright/test";
import { api, signIn } from "./helpers.ts";

// the authenticator set-up the first sign-in offers is not this page's subject
test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("huddle:totp-later", "true"));
});

const ID = "e2e-panes-ask";
const TITLE = "Panes ask";

// the session stopped, then its hook asked: the open item gives Home a row for it; the ask is
// dismissed after the tests so the other specs count the same needs-you items as before
let needId = 0;
test.beforeAll(async () => {
  await api("/api/registry/report", {
    session_id: ID,
    machine: "e2e-machine",
    project: "e2e",
    title: TITLE,
    last_event: "Stop",
    last_event_at: new Date().toISOString(),
  });
  needId = (
    await api<{ id: number }>("/api/needs", {
      session_id: ID,
      tool_name: "Bash",
      tool_input: { command: "bun run test" },
    })
  ).id;
});
test.afterAll(async () => {
  await api(`/api/needs/${needId}/dismiss`, {}).catch(() => null);
});

/** The three viewports: the desk, the tablet and the phone, with the panes each draws. */
const WIDTHS: readonly (readonly [width: number, height: number, panes: "one" | "two"])[] = [
  [1440, 900, "two"],
  [820, 1180, "two"],
  [390, 844, "one"],
];

test("a session opens with the same title from Sessions and from Home at every width", async ({
  page,
}, info) => {
  test.skip(info.project.name !== "desktop", "the test resizes the desktop project's page itself");
  await signIn(page);
  for (const [width, height, panes] of WIDTHS) {
    await page.setViewportSize({ width, height });
    for (const tab of ["sessions", "home"]) {
      await page.evaluate((h) => {
        location.hash = h;
      }, `#/${tab}/${ID}`);
      const at = `#/${tab}/${ID} at ${width}`;
      await expect(page.locator("[data-session-title]"), at).toHaveText(TITLE);
      await expect(page.locator("[data-panes]"), at).toHaveAttribute("data-panes", panes);
      const list = page.locator(tab === "home" ? "[data-home-pane]" : "[data-sessions-pane]");
      await expect(list, at).toHaveCount(panes === "two" ? 1 : 0);
    }
  }
});

// the boxes the canvases draw: the list pane is 360 px on the desk and 320 px on the tablet, and the
// item's content sits in a box of at most 760 px centred in its pane, between 24 px (desk) or 20 px
// (tablet) gutters; Sessions' desk keeps its 300 px rail, so its box is the 780 px pane less the gutters
const GEOMETRY: readonly (readonly [width: number, height: number, list: number, home: number, sessions: number])[] = [
  [1440, 900, 360, 760, 732],
  [820, 1180, 320, 460, 460],
];

test("the list and the item's content boxes match the canvases at the desk and the tablet", async ({
  page,
}, info) => {
  test.skip(info.project.name !== "desktop", "the test resizes the desktop project's page itself");
  await signIn(page);
  for (const tab of ["home", "sessions"]) {
    for (const [width, height, list, homeContent, sessionsContent] of GEOMETRY) {
      const content = tab === "home" ? homeContent : sessionsContent;
      await page.setViewportSize({ width, height });
      await page.evaluate((h) => {
        location.hash = h;
      }, `#/${tab}/${ID}`);
      const at = `${tab} at ${width}`;
      const pane = page.locator(tab === "home" ? "[data-home-pane]" : "[data-sessions-pane]");
      await expect(page.locator("[data-item-content]"), at).toBeVisible();
      const listBox = await pane.boundingBox();
      const contentBox = await page.locator("[data-item-content]").boundingBox();
      const mainBox = await page.locator("#main").boundingBox();
      expect(Math.abs((listBox?.width ?? 0) - list), `${at}: list width`).toBeLessThanOrEqual(1);
      expect(Math.abs((contentBox?.width ?? 0) - content), `${at}: content width`).toBeLessThanOrEqual(1);
      // centred in the item's pane
      const gap = (contentBox?.x ?? 0) - (mainBox?.x ?? 0);
      const rest = (mainBox?.x ?? 0) + (mainBox?.width ?? 0) - ((contentBox?.x ?? 0) + (contentBox?.width ?? 0));
      expect(Math.abs(gap - rest), `${at}: content centred`).toBeLessThanOrEqual(1);
      // a narrow card stacks the answers under the title; a roomy one keeps them beside it
      const title = await page.locator("[data-block='ask'] h2").boundingBox();
      const deny = await page.locator("[data-block='ask'] [data-act='deny']").boundingBox();
      if (width < 1200) {
        expect(deny?.y ?? 0, `${at}: answers under the title`).toBeGreaterThanOrEqual(
          (title?.y ?? 0) + (title?.height ?? 0) - 1,
        );
      } else {
        expect(deny?.y ?? 0, `${at}: answers beside the title`).toBeLessThan((title?.y ?? 0) + (title?.height ?? 0));
      }
      // the cards of the page fill the box: no inset of their own
      const ask = await page.locator("[data-block='ask']").boundingBox();
      expect(Math.abs((ask?.width ?? 0) - content), `${at}: ask width`).toBeLessThanOrEqual(1);
    }
  }
});

test("the phone draws one pane with the session alone", async ({ page }, info) => {
  test.skip(info.project.name !== "desktop", "the test resizes the desktop project's page itself");
  await signIn(page);
  await page.setViewportSize({ width: 393, height: 852 });
  await page.evaluate((h) => {
    location.hash = h;
  }, `#/home/${ID}`);
  await expect(page.locator("[data-panes]")).toHaveAttribute("data-panes", "one");
  await expect(page.locator("[data-home-pane]")).toHaveCount(0);
  await expect(page.locator("[data-session]")).toBeVisible();
});

test("a click on a Home row keeps the Home tab and opens the session beside the list", async ({ page }, info) => {
  test.skip(info.project.name !== "desktop", "the test resizes the desktop project's page itself");
  await signIn(page);
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.evaluate(() => {
    location.hash = "#/home";
  });
  const row = page.locator(`[data-home-pane] a[href="#/home/${ID}"]`);
  await expect(row).toBeVisible();
  await row.click();
  await expect(page).toHaveURL(new RegExp(`#/home/${ID}$`));
  await expect(page.locator("[data-session-title]")).toHaveText(TITLE);
  await expect(page.locator("[data-panes]")).toHaveAttribute("data-panes", "two");
  await expect(page.locator("[data-tab='home']").first()).toHaveAttribute("aria-current", "page");
  await page.screenshot({ path: info.outputPath("home-row-click-desk.png") });
});

test("Home opens its first row beside the list on the desk and the tablet", async ({ page }, info) => {
  test.skip(info.project.name !== "desktop", "the test resizes the desktop project's page itself");
  await signIn(page);
  for (const [width, height] of [
    [1440, 900],
    [820, 1180],
  ] as const) {
    await page.setViewportSize({ width, height });
    await page.evaluate(() => {
      location.hash = "#/home";
    });
    await expect(page.locator("[data-session]"), `home at ${width}`).toBeVisible();
    expect(new URL(page.url()).hash, `home at ${width}`).toMatch(/^#\/home\/.+/);
  }
});

test("on the phone a Home row pushes the session and Back returns to Home", async ({ page }, info) => {
  test.skip(info.project.name !== "desktop", "the test resizes the desktop project's page itself");
  await signIn(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.evaluate(() => {
    location.hash = "#/home";
  });
  await expect(page.locator("[data-session]")).toHaveCount(0);
  await page.locator(`a[href="#/home/${ID}"]`).click();
  await expect(page.locator("[data-session-title]")).toHaveText(TITLE);
  await page.getByRole("link", { name: "Back to Home" }).click();
  await expect(page).toHaveURL(/#\/home$/);
  await expect(page.locator("[data-session]")).toHaveCount(0);
});

test("an open channel draws its rail beside the page on the desk and the tablet, the page alone on the phone", async ({
  page,
}, info) => {
  test.skip(info.project.name !== "desktop", "the test resizes the desktop project's page itself");
  await signIn(page);
  for (const [width, height, panes] of WIDTHS) {
    await page.setViewportSize({ width, height });
    await page.evaluate(() => {
      location.hash = "#/channels/e2e";
    });
    await expect(page.locator("[data-panes]"), `channel at ${width}`).toHaveAttribute("data-panes", panes);
    await expect(page.locator("aside#side"), `channel at ${width}`).toHaveCount(panes === "two" ? 1 : 0);
  }
});
