// kanban-panel.spec.ts — the card detail panel over the seeded board: it docks beside the board
// at desktop width, its resize handle is remembered across a reload, a Waits on row swaps it to
// that card, and on a phone it is a full-screen sheet whose Back returns to the board. The two
// screenshots the card asks for land in web/e2e/screenshots (git-ignored).

import { mkdirSync } from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { openTab, signIn, theSetup } from "./helpers.ts";
import { repoRoot } from "./paths.ts";

const SHOTS = path.join(repoRoot, "web", "e2e", "screenshots");

test.describe("the card detail panel on a desktop", () => {
  test.use({ viewport: { width: 1440, height: 900 } });

  /** Signs in, opens the Kanban tab, opens the seeded backlog card's panel. */
  async function openPanel(page: Page, id: string): Promise<void> {
    await signIn(page);
    await openTab(page, "Kanban");
    const picker = page.getByLabel("Board", { exact: true });
    await expect(picker).toContainText(theSetup().boardName);
    await page.getByRole("button", { name: `${id}: First card` }).click();
    await expect(page.getByRole("dialog", { name: `${id}: First card` })).toBeVisible();
  }

  test("the panel docks at the right edge and the board stays beside it", async ({ page }) => {
    await test.step("Given the board at 1440x900 with the backlog card open", async () => {
      await openPanel(page, "E2E-1");
      await page.waitForTimeout(300);
    });

    await test.step("Then the panel is docked, not floating", async () => {
      const panel = page.getByRole("dialog", { name: "E2E-1: First card" });
      await expect(panel).toHaveClass(/docked/);
      const box = (await panel.boundingBox()) ?? expect.fail("the panel has no box");
      expect(box.x + box.width).toBeGreaterThan(1420);
      expect(box.width).toBeGreaterThanOrEqual(439);
      expect(box.width).toBeLessThanOrEqual(441);
    });

    await test.step("And the board shrank beside it, its columns still on screen", async () => {
      const columns = page.locator(".kanban-tab .columns");
      await expect(columns).toBeVisible();
      const board = (await columns.boundingBox()) ?? expect.fail("the board has no box");
      // 1440 minus the default 440 panel: the board gave the panel its column, and it starts
      // right of the shell's sidebar
      const sidebar = (await page.locator("[data-sidebar]").boundingBox()) ?? expect.fail("no sidebar");
      expect(board.width).toBeLessThanOrEqual(1001);
      expect(board.x).toBeGreaterThanOrEqual(sidebar.x + sidebar.width - 1);
      expect(board.x).toBeLessThan(sidebar.x + sidebar.width + 100);
      const panel = page.getByRole("dialog", { name: "E2E-1: First card" });
      const side = (await panel.boundingBox()) ?? expect.fail("the panel has no box");
      expect(side.x).toBeGreaterThanOrEqual(board.x + board.width - 1);
    });

    await test.step("And the header reads the card", async () => {
      const panel = page.getByRole("dialog", { name: "E2E-1: First card" });
      await expect(panel.getByText("First card")).toBeVisible();
      await expect(panel.getByText("Backlog").first()).toBeVisible();
      await expect(panel.getByRole("button", { name: "Copy E2E-1" })).toBeVisible();
    });

    await test.step("And the screenshot is saved", async () => {
      mkdirSync(SHOTS, { recursive: true });
      await page.screenshot({ path: path.join(SHOTS, "kanban-panel-1440.png") });
    });
  });

  test("the handle drags the panel to 600 px and a reload keeps it", async ({ page }) => {
    await test.step("Given the docked panel at its default width", async () => {
      await openPanel(page, "E2E-1");
      const panel = page.getByRole("dialog", { name: "E2E-1: First card" });
      await expect(panel).toHaveClass(/docked/);
      await expect(panel).toHaveCSS("width", "440px");
    });

    await test.step("When the handle drags 160 px to the left", async () => {
      // the open slide moves the handle up to 24 px for 150 ms: let it settle before the drag
      await page.evaluate(() =>
        Promise.all(document.getAnimations().map((animation) => animation.finished.catch(() => undefined))),
      );
      const handle = page.getByRole("separator", { name: "Panel width" });
      const box = (await handle.boundingBox()) ?? expect.fail("the handle has no box");
      const x = box.x + box.width / 2;
      const y = box.y + 40;
      await page.mouse.move(x, y);
      await page.mouse.down();
      await page.mouse.move(x - 160, y, { steps: 8 });
      await page.mouse.up();
      const panel = page.getByRole("dialog", { name: "E2E-1: First card" });
      await expect(panel).toHaveCSS("width", "600px");
    });

    await test.step("Then a reload reopens it at 600 px", async () => {
      await page.reload();
      await signIn(page);
      await openTab(page, "Kanban");
      await page.getByRole("button", { name: "E2E-1: First card" }).click();
      const panel = page.getByRole("dialog", { name: "E2E-1: First card" });
      await expect(panel).toHaveCSS("width", "600px");
    });
  });

  test("a Waits on row swaps the panel to that card", async ({ page }) => {
    await test.step("Given the panel on E2E-1, which waits on E2E-2", async () => {
      await openPanel(page, "E2E-1");
      await expect(page.getByText("Waits on", { exact: true })).toBeVisible();
    });

    await test.step("When the reader clicks the E2E-2 row", async () => {
      await page.getByRole("button", { name: "E2E-2", exact: true }).click();
    });

    await test.step("Then the panel shows E2E-2 without closing", async () => {
      const swapped = page.getByRole("dialog", { name: "E2E-2: Second card" });
      await expect(swapped).toBeVisible();
      await expect(swapped).toHaveClass(/docked/);
      await expect(page.getByText("Unblocks")).toBeVisible();
    });
  });
});

test.describe("the card detail panel on a phone", () => {
  test.use({ viewport: { width: 390, height: 844 } });

  test("the panel is full screen and Back returns to the board", async ({ page }) => {
    await test.step("Given the board at 390x844 with the backlog card open", async () => {
      await signIn(page);
      await openTab(page, "Kanban");
      await page.getByRole("button", { name: "E2E-1: First card" }).click();
      const panel = page.getByRole("dialog", { name: "E2E-1: First card" });
      await expect(panel).toBeVisible();
      await expect(panel).toHaveClass(/full/);
      await page.waitForTimeout(300);
    });

    await test.step("Then the panel covers the screen and the header holds Back", async () => {
      const panel = page.getByRole("dialog", { name: "E2E-1: First card" });
      const box = (await panel.boundingBox()) ?? expect.fail("the panel has no box");
      expect(box.width).toBeGreaterThan(380);
      expect(box.height).toBeGreaterThan(830);
      await expect(panel.getByRole("button", { name: "Back" })).toBeVisible();
      mkdirSync(SHOTS, { recursive: true });
      await page.screenshot({ path: path.join(SHOTS, "kanban-panel-390.png") });
    });

    await test.step("And Back closes it back to the board", async () => {
      await page.getByRole("dialog", { name: "E2E-1: First card" }).getByRole("button", { name: "Back" }).click();
      await expect(page.getByRole("dialog", { name: "E2E-1: First card" })).toHaveCount(0);
      await expect(page.getByRole("button", { name: "E2E-1: First card" })).toBeVisible();
    });
  });
});
