// shell.spec.ts — the app shell on both projects. On the phone the five tabs sit in a bar pinned
// to the viewport's bottom, whatever scrolls, on the iPhone's own height and on a short one where
// every page runs longer than the screen; every page's content ends above the bar, nothing
// scrolls the page sideways, a code block scrolls inside its own box, and every control on every
// page is a target of at least 44 px. On the desktop the same five items stand in a left sidebar
// and no bottom bar is drawn. axe checks the shell's chrome on both (the pages' contents are their
// own specs'), and the page names a theme colour per scheme beside a manifest with a maskable icon.
// A button styled by Tailwind utilities alone keeps its fill and its border over Radar's legacy
// button reset.

import { expect, test, type Page } from "@playwright/test";
import { expectAccessible } from "./axe.ts";
import { api, expectTapTargets, SEEDED, signIn, theSetup } from "./helpers.ts";

/** The five tabs, in the order the shell draws them; Now may carry its needs-you count after it. */
const TABS = [/^Now\d*$/, /^Channels$/, /^Radar$/, /^Kanban$/, /^Settings$/];

/** Every page the shell frames: the five tabs and a channel's own page under Channels. */
const PAGES: readonly (readonly [name: string, hash: string])[] = [
  ["Now", "#/now"],
  ["Channels", "#/channels"],
  ["Channel overview", "#/c/e2e/overview"],
  ["Radar", "#/radar/overview"],
  ["Kanban", "#/kanban"],
  ["Settings", "#/settings"],
];

/** The pages the tap check walks: the five tabs, a channel's page and a session's own page under
 *  Now (its Back to Now sits there). */
const TAP_PAGES: readonly (readonly [name: string, hash: string])[] = [
  ["Now", "#/now"],
  ["A session", `#/now/${SEEDED.session}`],
  ["Channels", "#/channels"],
  ["Channel overview", "#/c/e2e/overview"],
  ["Radar", "#/radar/overview"],
  ["Kanban", "#/kanban"],
  ["Settings", "#/settings"],
];

/** The parts axe leaves to the pages' own specs: the main pane and the Now list beside it. */
const CONTENT = ["main", "[data-now-pane]"] as const;

/** The viewport heights the phone runs at: the iPhone 15's own, and a short screen (the keyboard
 *  up, a small phone) that makes every page longer than the view. */
const HEIGHTS = [659, 420] as const;

/** Opens one page in a signed-in browser. The authenticator set-up the first sign-in offers is
 *  not the shell's subject, so the browser starts with its "Later" already kept. */
async function open(page: Page, hash: string): Promise<void> {
  await page.addInitScript(() => localStorage.setItem("huddle:totp-later", "true"));
  await signIn(page);
  await page.goto(`${theSetup().rootUrl}/${hash}`);
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
  // the panels settle: their skeletons are gone, or three seconds passed
  await page
    .waitForFunction(() => document.querySelectorAll(".skeleton").length === 0, undefined, { timeout: 3000 })
    .catch(() => {});
}

/** Scrolls the window and every scroll box on the page to its end, the way a thumb reaches the
 *  bottom of a long page. */
async function scrollToEnd(page: Page): Promise<void> {
  await page.evaluate(() => {
    window.scrollTo(0, document.documentElement.scrollHeight);
    for (const el of document.querySelectorAll<HTMLElement>("*")) {
      if (el.scrollHeight > el.clientHeight) el.scrollTop = el.scrollHeight;
    }
  });
  await page.waitForTimeout(100);
}

/** The bar's box and the viewport's height. */
function barBox(page: Page): Promise<{ top: number; bottom: number; height: number; view: number }> {
  return page.evaluate(() => {
    const bar = document.querySelector("[data-nav='bottom']");
    const r = bar?.getBoundingClientRect();
    return { top: r?.top ?? -1, bottom: r?.bottom ?? -1, height: r?.height ?? 0, view: window.innerHeight };
  });
}

/** Every element of the main pane that still paints below the bar's top edge, once each box it
 *  sits in clips it: a painted part under the bar is a part the owner cannot read. */
function underBar(page: Page, barTop: number): Promise<string[]> {
  return page.evaluate((top) => {
    const clipped = (el: Element): { top: number; bottom: number } => {
      const r = el.getBoundingClientRect();
      let t = r.top;
      let b = r.bottom;
      for (let p = el.parentElement; p; p = p.parentElement) {
        const s = getComputedStyle(p);
        if (s.overflowY === "visible" && s.overflowX === "visible") continue;
        const pr = p.getBoundingClientRect();
        t = Math.max(t, pr.top);
        b = Math.min(b, pr.bottom);
      }
      return { top: t, bottom: b };
    };
    return [...document.querySelectorAll("main *")]
      .filter((el) => el.children.length === 0)
      .filter((el) => {
        const r = el.getBoundingClientRect();
        if (r.width < 1 || r.height < 1) return false;
        if (getComputedStyle(el).visibility === "hidden") return false;
        const c = clipped(el);
        return c.bottom - c.top >= 1 && c.bottom > top + 0.5;
      })
      .map((el) => `${el.tagName.toLowerCase()} "${(el.textContent ?? "").trim().slice(0, 40)}"`);
  }, barTop);
}

test.describe("the phone shell", () => {
  test.skip(({ isMobile }) => !isMobile, "the bottom bar is the phone's");

  for (const [name, hash] of PAGES) {
    for (const height of HEIGHTS) {
      test(`${name} at ${height} px: the bar stays pinned and nothing sits under it`, async ({ page }) => {
        await page.setViewportSize({ width: 393, height });
        await open(page, hash);
        const before = await barBox(page);
        expect(before.bottom, `${name}: the bar's bottom`).toBeCloseTo(before.view, 0);
        expect(before.height, `${name}: the bar is 56 px plus the inset`).toBeGreaterThanOrEqual(56);
        await scrollToEnd(page);
        const after = await barBox(page);
        expect(after.bottom, `${name}: the bar's bottom after scrolling`).toBeCloseTo(after.view, 0);
        expect(await underBar(page, after.top), `${name}: content under the bar`).toEqual([]);
        const over = await page.evaluate(
          () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
        );
        expect(over, `${name} scrolls the page sideways by ${over}px`).toBeLessThanOrEqual(0);
        const cut = await page.evaluate(() =>
          [...document.querySelectorAll<HTMLElement>("main pre")]
            .filter((pre) => pre.scrollWidth > pre.clientWidth)
            .filter((pre) => !["auto", "scroll"].includes(getComputedStyle(pre).overflowX))
            .map((pre) => (pre.textContent ?? "").slice(0, 40)),
        );
        expect(cut, `${name}: code blocks cut off instead of scrolling`).toEqual([]);
      });
    }
  }

  test("the five tabs are targets of at least 44 px", async ({ page }) => {
    await open(page, "#/now");
    const links = page.getByRole("navigation", { name: "Main" }).getByRole("link");
    await expect(links).toHaveText(TABS);
    for (const box of await links.evaluateAll((els) =>
      els.map((el) => {
        const r = el.getBoundingClientRect();
        return { w: r.width, h: r.height };
      }),
    )) {
      expect(box.w).toBeGreaterThanOrEqual(44);
      expect(box.h).toBeGreaterThanOrEqual(44);
    }
  });

  for (const [name, hash] of TAP_PAGES) {
    test(`${name}: every control is a target of at least 44 px`, async ({ page }) => {
      await open(page, hash);
      await expectTapTargets(page);
    });
  }

  test("a tab opens its page and marks itself current", async ({ page }) => {
    await open(page, "#/now");
    const nav = page.getByRole("navigation", { name: "Main" });
    await nav.getByRole("link", { name: "Radar" }).click();
    await expect(page).toHaveURL(/#\/radar\//);
    await expect(nav.getByRole("link", { name: "Radar" })).toHaveAttribute("aria-current", "page");
  });

  test("passes axe", async ({ page }) => {
    await open(page, "#/now");
    await expectAccessible(page, CONTENT);
  });
});

test.describe("the desktop shell", () => {
  test.skip(({ isMobile }) => isMobile, "the sidebar is the desktop's");

  test("the five tabs stand in a left sidebar and no bottom bar is drawn", async ({ page }) => {
    await open(page, "#/now");
    const side = page.locator("[data-nav='side']");
    await expect(side).toBeVisible();
    await expect(side.getByRole("link")).toHaveText(TABS);
    await expect(page.locator("[data-nav='bottom']")).toHaveCount(0);
    const box = await page.locator("[data-sidebar]").boundingBox();
    const view = page.viewportSize();
    expect(box?.x).toBe(0);
    expect(box && view && box.height).toBeCloseTo(view?.height ?? 0, 0);
    await side.getByRole("link", { name: "Kanban" }).click();
    await expect(page).toHaveURL(/#\/kanban/);
    await expect(side.getByRole("link", { name: "Kanban" })).toHaveAttribute("aria-current", "page");
  });

  test("passes axe", async ({ page }) => {
    await open(page, "#/now");
    await expectAccessible(page, CONTENT);
  });
});

test.describe("the installed app", () => {
  test("names a theme colour per scheme and a manifest with a maskable icon", async ({ page }) => {
    await open(page, "#/now");
    const colours = await page.evaluate(() =>
      [...document.querySelectorAll<HTMLMetaElement>("meta[name='theme-color']")].map((m) => m.media),
    );
    expect(colours).toEqual(["(prefers-color-scheme: dark)", "(prefers-color-scheme: light)"]);
    const manifest = (await (await page.request.get(`${theSetup().rootUrl}/manifest.webmanifest`)).json()) as {
      display: string;
      icons: { purpose?: string }[];
    };
    expect(manifest.display).toBe("standalone");
    expect(manifest.icons.some((i) => i.purpose === "maskable")).toBe(true);
  });
});

test.describe("the utility layer", () => {
  test("a permission ask's Allow once keeps its fill and Deny its border", async ({ page }, info) => {
    const session = `e2e-layer-${info.project.name}`;
    await api("/api/registry/report", {
      session_id: session,
      machine: "e2e-machine",
      project: "e2e",
      title: "Asks to build the bundle",
      last_event: "Stop",
      last_event_at: new Date().toISOString(),
    });
    await api("/api/needs", { session_id: session, tool_name: "Bash", tool_input: { command: "bun run build" } });
    await open(page, `#/now/${session}`);
    // the session screen's ask: the phone's bottom sheet, or the wide layouts' inline box
    const bar = page.locator("[data-needs-sheet], [data-needs-bar]");
    const allow = bar.locator("[data-act='allow']");
    const deny = bar.locator("[data-act='deny']");
    await expect(allow).toBeVisible();
    expect(await allow.evaluate((el) => getComputedStyle(el).backgroundColor)).not.toBe("rgba(0, 0, 0, 0)");
    const border = await deny.evaluate((el) => {
      const s = getComputedStyle(el);
      return `${s.borderTopWidth} ${s.borderTopStyle}`;
    });
    expect(border).toBe("1px solid");
  });
});
