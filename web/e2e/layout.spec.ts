// layout.spec.ts — the screenshot tour as a permanent gate: every tab and every Radar view, in
// both colour schemes at the desk and the phone width, opens with no console error, no page-level
// horizontal scroll, and no two Kanban toolbar controls overlapping. Each view also lands a
// screenshot under the test's own output dir, so a layout report is one run away.

import { expect, type Locator, type Page, test } from "@playwright/test";
import { ConsoleErrors, openTab, signIn } from "./helpers.ts";

/** The two viewports the tour shoots: the desk and the phone. */
const SIZES: readonly (readonly [kind: string, width: number, height: number])[] = [
  ["desk", 1440, 900],
  ["phone", 390, 844],
];

/** The colour schemes a fresh browser can carry; the default theme follows the system. */
const SCHEMES: readonly ("dark" | "light")[] = ["dark", "light"];

/** Radar's sub-tabs, in sub-tab order; the current one and its settled panels are the wait. */
const RADAR: readonly string[] = [
  "Overview",
  "Costs",
  "Models",
  "Usage",
  "Blocks",
  "Requests",
  "Tools",
  "Agents",
  "Routers",
  "Alerts",
  "Timeline",
];

/** Waits until the view's panels stopped loading (their skeletons are gone) and the numbers
 *  settled, so the screenshot shows the view, not its arrival. A view whose fetch runs longer
 *  still shoots: its skeletons hold the panels' final height. */
async function settled(page: Page): Promise<void> {
  await page
    .waitForFunction(() => document.querySelectorAll(".skeleton").length === 0, undefined, { timeout: 3000 })
    .catch(() => {});
  await page.waitForTimeout(300);
}

/** Asserts the page itself does not scroll sideways, whatever its panels do internally. */
async function expectNoPageScroll(page: Page, name: string): Promise<void> {
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
  );
  expect(overflow, `${name} scrolls the page sideways by ${overflow}px`).toBeLessThanOrEqual(0);
}

/** The bounding boxes of a locator's elements, empty ones left out. */
function boxesOf(locator: Locator): Promise<{ x: number; y: number; w: number; h: number }[]> {
  return locator.evaluateAll((elements) =>
    elements
      .map((element) => {
        const box = element.getBoundingClientRect();
        return { x: box.x, y: box.y, w: box.width, h: box.height };
      })
      .filter((box) => box.w > 0 && box.h > 0),
  );
}

/** Asserts no two of the Kanban toolbar's controls overlap, box against box. */
async function expectToolbarClear(page: Page, name: string): Promise<void> {
  const boxes = await boxesOf(page.locator(".kanban-tab .toolbar > *"));
  expect(boxes.length, `${name}: the toolbar holds no controls`).toBeGreaterThan(0);
  for (let i = 0; i < boxes.length; i += 1) {
    for (let j = i + 1; j < boxes.length; j += 1) {
      const a = boxes[i] as { x: number; y: number; w: number; h: number };
      const b = boxes[j] as { x: number; y: number; w: number; h: number };
      const overlap = a.x < b.x + b.w - 1 && b.x < a.x + a.w - 1 && a.y < b.y + b.h - 1 && b.y < a.y + a.h - 1;
      expect(overlap, `${name}: toolbar controls ${i} and ${j} overlap`).toBe(false);
    }
  }
}

for (const scheme of SCHEMES) {
  for (const [kind, width, height] of SIZES) {
    test(`every view lays out clean: ${scheme} ${kind}`, async ({ browser }, testInfo) => {
      test.setTimeout(150_000);
      const context = await browser.newContext({ viewport: { width, height }, colorScheme: scheme });
      const page = await context.newPage();
      const errors = new ConsoleErrors(page);
      const shot = async (view: string): Promise<void> => {
        await page.screenshot({ path: testInfo.outputPath(`${scheme}-${kind}-${view}.png`) });
      };

      await signIn(page);

      /** Opens one of the shell's tabs, waits for its panes to settle, and shoots it. */
      const openAndShoot = async (
        tab: string,
        extra: () => Promise<void> = () => Promise.resolve(),
        view = tab.toLowerCase(),
      ) => {
        const link = page.getByRole("navigation", { name: "Main" }).getByRole("link", { name: tab });
        await link.click();
        await expect(link).toHaveAttribute("aria-current", "page");
        await settled(page);
        await extra();
        await expectNoPageScroll(page, view);
        await shot(view);
      };

      await openAndShoot("Now");
      await openAndShoot("Channels");

      // Radar: one screenshot per view, the sub-tab's own currency the wait
      await openAndShoot("Radar", () => Promise.resolve(), "radar-overview");
      for (const label of RADAR.slice(1)) {
        const link = page
          .getByRole("navigation", { name: "Radar views" })
          .getByRole("link", { name: label, exact: true });
        await link.click();
        await expect(link).toHaveAttribute("aria-current", "page");
        await settled(page);
        await expectNoPageScroll(page, `radar ${label}`);
        await shot(`radar-${label.toLowerCase()}`);
      }

      // Kanban: the toolbar is the one whose controls must not overlap
      await openAndShoot("Kanban", async () => {
        await page.locator(".kanban-tab .card").first().waitFor();
        await expectToolbarClear(page, `kanban ${scheme} ${kind}`);
      });

      // Settings, and its Radar section
      await openAndShoot("Settings");
      await page.evaluate(() => {
        location.hash = "#/settings/radar";
      });
      await page.getByRole("heading", { name: "Budgets" }).waitFor();
      await settled(page);
      await expectNoPageScroll(page, "settings radar");
      await shot("settings-radar");

      expect(errors.errors(), `console errors on ${scheme} ${kind}`).toEqual([]);
      await context.close();
    });
  }
}
