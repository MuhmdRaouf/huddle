// kanban.spec.ts — the Kanban tab over the fixture hub and board. On the desktop: the picker
// driven from the keyboard, a column collapsing to its rail and a rail opening, the dependency
// thread lighting up on hover, and the board at 1440 and 1024 px. On the phone: one column per
// screen in a snap strip a swipe moves along, read-only cards whose tap opens the design system's
// sheet, the picker as a full-width sheet listing every board, 44 px tap targets and axe. The
// extra tickets the scenarios need are written into the run's own board folder (gitignored
// scratch, removed by the teardown) the way `kanban new` would; the fixture folders stay as they
// are.

import { execFileSync } from "node:child_process";
import { existsSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { expectAccessible } from "./axe.ts";
import { api, openTab, signIn, theSetup } from "./helpers.ts";
import { repoRoot, runDir } from "./paths.ts";

/** The run's board folder: the seeded project's `.agents/kanban`. */
const boardDir = (): string =>
  path.join(runDir, "project", theSetup().boardName, ".agents", "kanban");

/** Writes one ticket into a column of the run's board, the way `kanban new` would. */
function seedTicket(column: string, id: string, title: string, over = ""): void {
  const dir = path.join(boardDir(), column);
  mkdirSync(dir, { recursive: true });
  writeFileSync(
    path.join(dir, `${id}.md`),
    `---\nid: ${id}\ntitle: "${title}"\ntype: feature\ncluster: e2e\npriority: P2\nsummary: "A card the kanban spec seeded."\n${over}\n---\n\nBody.\n`,
  );
}

/** Fills the backlog out to nine and adds the card that waits on E2E-1. The working columns keep
 *  their single fixture card; review, testing and blocked stay empty, so they read as the rails
 *  the design draws beside the open columns. */
function seedBoard(): void {
  for (let i = 2; i <= 9; i += 1) seedTicket("backlog", `BULK-${i}`, `Bulk card ${i}`);
  seedTicket("backlog", "E2E-4", "The card that waits", "depends_on: [E2E-1]");
}

/** Registers a second board under its own project folder and reports a session working in it.
 *  The project is its own git repository with its own origin: a board's key is its repository's
 *  identity, and inside this repository both boards would fold into one. Both projects run it, so
 *  the repository is made once. */
async function seedSecondBoard(): Promise<void> {
  const root = path.join(runDir, "second-project", "e2e-second");
  if (!existsSync(path.join(root, ".git"))) {
    mkdirSync(path.join(root, ".agents", "kanban", "backlog"), { recursive: true });
    execFileSync("git", ["init", "-q"], { cwd: root });
    execFileSync("git", ["remote", "add", "origin", "https://example.test/e2e-second.git"], { cwd: root });
  }
  writeFileSync(
    path.join(root, ".agents", "kanban", "backlog", "SECOND-1.md"),
    '---\nid: SECOND-1\ntitle: "Only on second"\ntype: fix\ncluster: e2e\npriority: P1\nsummary: "The second board\'s one card."\n---\n\nBody.\n',
  );
  await api("/api/registry/report", {
    session_id: "e2e-second-session",
    machine: "e2e-machine",
    project: "e2e-second",
    title: "E2E second board session",
    model: "glm-5.3",
    cwd: root,
    last_event: "UserPromptSubmit",
    // a day old, so the Now list's Working group keeps counting only the seeded session
    last_event_at: new Date(Date.now() - 26 * 60 * 60 * 1000).toISOString(),
  });
}

/** A page on the kanban tab, signed in at the given size and settled on the seeded board. */
async function kanbanAt(page: Page, width: number, height: number): Promise<void> {
  await page.setViewportSize({ width, height });
  await signIn(page);
  await openTab(page, "Kanban");
  await page.locator(".kanban-tab .card").first().waitFor();
}

/** The page itself must not scroll sideways, whatever its panels do. */
const pageOverflow = (page: Page): Promise<number> =>
  page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);

/** Waits for every running animation on the page to finish. */
const settle = (page: Page): Promise<unknown> =>
  page.evaluate(() =>
    Promise.all(document.getAnimations().map((animation) => animation.finished.catch(() => undefined))),
  );

/** Screenshots land here; the folder is gitignored. */
const shotDir = path.join(repoRoot, "web", "e2e", "screenshots");
const shot = (page: Page, name: string): Promise<void> =>
  page.screenshot({ path: path.join(shotDir, name) });

test.beforeAll(async () => {
  mkdirSync(shotDir, { recursive: true });
  seedBoard();
});

/** The board picker's button keeps "Board" as its name while it shows the board's own name, the
 *  name every spec finds it by; axe's label-content-name-mismatch rule reads that as a mismatch,
 *  so the button is left out of the axe pass. */
const AXE_EXCLUDE = [".kanban-tab .picker-btn"] as const;

test.describe("the board on a desktop", () => {
  test.skip(({ isMobile }) => isMobile, "the columns, rails and hover are the desktop's");

  test("the board fits its columns, its rails and a dense backlog at 1440×900", async ({ page }) => {
    await test.step("Given the seeded board open at 1440×900", async () => {
      await kanbanAt(page, 1440, 900);
    });

    await test.step("Then the open columns show their counts and the empty ones their rails", async () => {
      await expect(page.getByRole("heading", { name: "Backlog 10" })).toBeVisible();
      await expect(page.getByRole("heading", { name: "Ready 1" })).toBeVisible();
      await expect(page.getByRole("heading", { name: "In progress 1" })).toBeVisible();
      for (const rail of ["Review", "Testing", "Blocked"]) {
        await expect(page.getByRole("button", { name: `${rail}, 0 cards, show column` })).toBeVisible();
      }
      await expect(page.getByRole("button", { name: "Done, 0 cards, show column" })).toBeVisible();
    });

    await test.step("Then the backlog holds its nine compact cards and the page does not scroll sideways", async () => {
      await expect(page.locator('section[data-column="backlog"] article.card')).toHaveCount(10);
      expect(await pageOverflow(page)).toBeLessThanOrEqual(0);
    });

    await test.step("Then the flow bar names every column and draws one segment per column with cards", async () => {
      await expect(page.getByRole("img", { name: /^Backlog 10, Ready 1, In Progress 1, /i })).toBeVisible();
      await expect(page.locator(".kanban-tab .flow-seg")).toHaveCount(3);
      await expect(page.locator(".kanban-tab .flow-labels .flow-label")).toHaveCount(7);
    });

    await test.step("And axe finds nothing serious on the board or with a card's panel open", async () => {
      await expectAccessible(page, AXE_EXCLUDE);
      await page.getByRole("button", { name: "E2E-1: First card" }).click();
      await expect(page.getByRole("dialog", { name: "E2E-1: First card" }).getByText("Body text.")).toBeVisible();
      // the panel slides in from transparent: axe reads its colours once the slide is done
      await settle(page);
      await expectAccessible(page, AXE_EXCLUDE);
      await page.keyboard.press("Escape");
    });

    await test.step("and the desk screenshot is taken", async () => {
      await shot(page, "kanban-desk-1440x900.png");
    });
  });

  test("a flow label scrolls its column into view", async ({ page }) => {
    await kanbanAt(page, 1440, 900);
    await page.getByRole("button", { name: "Done 0" }).click();
    await expect(page.locator('section[data-column="done"]')).toBeInViewport();
  });

  test("a rail opens its column, and the picker drives the boards from the keyboard", async ({ page }) => {
    await test.step("Given the seeded board open at 1440×900", async () => {
      await kanbanAt(page, 1440, 900);
    });

    await test.step("When the done rail is clicked", async () => {
      await page.getByRole("button", { name: "Done, 0 cards, show column" }).click();
    });

    await test.step("Then done is a column of its own again, empty but open", async () => {
      await expect(page.getByRole("heading", { name: "Done 0" })).toBeVisible();
    });

    await test.step("When a second board is registered and b opens the picker", async () => {
      await seedSecondBoard();
      await page.reload();
      await page.locator(".kanban-tab .card").first().waitFor();
      await page.keyboard.press("b");
      await expect(page.getByRole("listbox", { name: "Boards" })).toBeVisible();
    });

    await test.step("Then typing narrows the list and Enter moves to the second board", async () => {
      await page.getByRole("combobox", { name: "Filter boards" }).fill("second");
      await page.keyboard.press("Enter");
      await expect(page.getByRole("button", { name: "Board", exact: true })).toContainText("e2e-second");
      await expect(page.getByRole("button", { name: "SECOND-1: Only on second" })).toBeVisible();
    });
  });

  test("hovering a card with a dependency marks its dependency and dims the rest", async ({ page }) => {
    await test.step("Given the board with E2E-4 waiting on E2E-1", async () => {
      await kanbanAt(page, 1440, 900);
      await expect(page.getByRole("button", { name: "E2E-4: The card that waits" })).toBeVisible();
    });

    await test.step("When E2E-4 is hovered", async () => {
      await page.locator('[data-id="E2E-4"]').hover();
    });

    await test.step("Then E2E-1 is marked waits-on, E2E-4 itself, and the rest dim", async () => {
      await expect(page.locator('[data-id="E2E-1"]')).toHaveAttribute("data-thread", "waits-on");
      await expect(page.locator('[data-id="E2E-4"]')).toHaveAttribute("data-thread", "self");
      await expect(page.locator('[data-id="E2E-2"]')).toHaveAttribute("data-thread", "dim");
    });
  });

  test("the wide board keeps the columns mode and scrolls its own grid at 1024×768", async ({ page }) => {
    await kanbanAt(page, 1024, 768);
    await expect(page.getByRole("heading", { name: "Backlog 10" })).toBeVisible();
    // the six columns cannot all fit at this width, so the board's own grid scrolls, not the page
    await expect(page.locator(".kanban-tab section.col")).toHaveCount(7);
    const boardScrolls = await page
      .locator(".kanban-tab .columns")
      .evaluate((el) => el.scrollWidth > el.clientWidth);
    expect(boardScrolls).toBe(true);
    await shot(page, "kanban-wide-1024x768.png");
  });

  test("a column collapses into its rail and the rail opens it again", async ({ page }) => {
    await test.step("Given the seeded board open at 1440×900", async () => {
      await kanbanAt(page, 1440, 900);
    });

    await test.step("When the backlog's collapse button is clicked", async () => {
      await page.getByRole("button", { name: "Collapse Backlog" }).click();
    });

    await test.step("Then the backlog is a rail naming its count, and its cards are gone", async () => {
      await expect(page.getByRole("button", { name: "Backlog, 10 cards, show column" })).toBeVisible();
      await expect(page.locator('section[data-column="backlog"] article.card')).toHaveCount(0);
    });

    await test.step("When the rail is clicked, the column and its cards come back", async () => {
      await page.getByRole("button", { name: "Backlog, 10 cards, show column" }).click();
      await expect(page.getByRole("heading", { name: "Backlog 10" })).toBeVisible();
      await expect(page.locator('section[data-column="backlog"] article.card')).toHaveCount(10);
    });
  });
});

test.describe("a done-heavy board", () => {
  /** The 56 done cards the scenario adds and removes again, so every other scenario keeps its
   *  empty done column. */
  const heavyIds = Array.from({ length: 56 }, (_, i) => `HEAVY-${i + 10}`);

  test.afterEach(() => {
    for (const id of heavyIds) rmSync(path.join(boardDir(), "done", `${id}.md`), { force: true });
  });

  test("a done-heavy board hides its done cards and says how many", async ({ page, isMobile }) => {
    await test.step("Given a board with 56 done cards", async () => {
      for (const id of heavyIds) seedTicket("done", id, `Done card ${id}`);
      await kanbanAt(page, isMobile ? 390 : 1440, isMobile ? 844 : 900);
    });

    await test.step("Then done is hidden, its count kept, and the board says how many it hides", async () => {
      await expect(page.locator(".kanban-tab .flow-label", { hasText: "Done 56" })).toBeVisible();
      await expect(page.getByRole("checkbox", { name: "Hide done (56)" })).toBeChecked();
      await expect(page.locator(".kanban-tab .done-hidden")).toContainText("56 done hidden");
      await expect(page.locator(".kanban-tab article.card[data-id^='HEAVY-']")).toHaveCount(0);
    });

    await test.step("When Show done is tapped, the done cards come back and the line goes", async () => {
      const show = page.getByRole("button", { name: "Show done" });
      if (isMobile) {
        const box = (await show.boundingBox()) ?? expect.fail("no Show done button");
        expect(box.height).toBeGreaterThanOrEqual(44 - 0.5);
        await show.tap();
      } else {
        await show.click();
      }
      await expect(page.getByRole("checkbox", { name: "Hide done" })).not.toBeChecked();
      await expect(page.locator(".kanban-tab .done-hidden")).toHaveCount(0);
      await expect(page.locator(".kanban-tab .flow-label", { hasText: "Done 56" })).toBeVisible();
      // the desktop keeps done behind its rail until it is opened; the phone's strip holds it
      if (!isMobile) await page.getByRole("button", { name: "Done, 56 cards, show column" }).click();
      await expect(page.locator(".kanban-tab article.card[data-id^='HEAVY-']").first()).toBeAttached();
    });
  });
});

/** Swipes the snap strip one thumb's stroke to the left, the way a finger moves to the next
 *  column: a touch scroll gesture Chromium synthesizes, so the snap settles as it would on the
 *  phone. */
async function swipeLeft(page: Page): Promise<void> {
  const box = (await page.locator(".kanban-tab .lanes").boundingBox()) ?? expect.fail("no lanes");
  const cdp = await page.context().newCDPSession(page);
  await cdp.send("Input.synthesizeScrollGesture", {
    x: Math.round(box.x + box.width * 0.8),
    y: Math.round(box.y + 40),
    xDistance: -Math.round(box.width * 0.6),
    yDistance: 0,
    gestureSourceType: "touch",
    speed: 1200,
  });
  await cdp.detach();
}

/** Every visible control of the tab and of an open sheet narrower or shorter than a 44 px tap
 *  target, named by its text or label. A control inside a label is tapped through the label, so
 *  the label is the target measured. */
function smallTargets(page: Page): Promise<string[]> {
  return page.evaluate(() =>
    [...document.querySelectorAll<HTMLElement>(
      ".kanban-tab button, .kanban-tab [role='tab'], .kanban-tab a, .kanban-tab input, dialog[open] button",
    )]
      .map((el) => el.closest<HTMLElement>("label") ?? el)
      .filter((el) => {
        const r = el.getBoundingClientRect();
        return r.width > 0 && r.height > 0 && getComputedStyle(el).visibility !== "hidden";
      })
      .filter((el) => {
        const r = el.getBoundingClientRect();
        return r.width < 44 - 0.5 || r.height < 44 - 0.5;
      })
      .map((el) => {
        const r = el.getBoundingClientRect();
        const name = el.getAttribute("aria-label") ?? (el.textContent ?? "").trim().slice(0, 30);
        return `${el.tagName.toLowerCase()} "${name}" ${Math.round(r.width)}x${Math.round(r.height)}`;
      }),
  );
}

test("while the board list loads, its skeleton paints at full width", async ({ page }) => {
  let release = (): void => undefined;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });

  await test.step("Given the board list held unanswered", async () => {
    await page.route("**/api/kanban/boards", async (route) => {
      await held;
      await route.continue().catch(() => undefined);
    });
    await signIn(page);
    await openTab(page, "Kanban");
  });

  await test.step("Then the loading box spans the page and wears no spinner mask", async () => {
    const box = page.getByRole("status", { name: "Loading the boards" });
    await expect(box).toBeVisible();
    const looks = await box.evaluate((el) => {
      const style = getComputedStyle(el);
      return {
        width: el.getBoundingClientRect().width,
        mask: style.maskImage || style.getPropertyValue("-webkit-mask-image") || "none",
      };
    });
    expect(looks.width).toBeGreaterThan(300);
    expect(looks.mask).toBe("none");
  });

  release();
});

test.describe("the board on a phone", () => {
  test.skip(({ isMobile }) => !isMobile, "the swipe strip and the card sheet are the phone's");

  test("one column fills the screen and a swipe moves to the next one", async ({ page }) => {
    await test.step("Given the board open at 390×844", async () => {
      await kanbanAt(page, 390, 844);
    });

    await test.step("Then the columns stand in one snap strip, each as wide as the screen", async () => {
      const lanes = page.locator(".kanban-tab .lanes");
      await expect(lanes).toHaveCSS("scroll-snap-type", "x mandatory");
      const widths = await lanes.evaluate((el) => ({
        lane: el.clientWidth,
        cols: [...el.querySelectorAll<HTMLElement>("section.col")].map((col) => col.getBoundingClientRect().width),
      }));
      expect(widths.cols).toHaveLength(7);
      for (const width of widths.cols) expect(Math.abs(width - widths.lane)).toBeLessThanOrEqual(1);
      await expect(page.getByRole("tablist", { name: "Columns" }).getByRole("tab")).toHaveCount(7);
      await expect(page.getByRole("tab", { selected: true })).toHaveText("Backlog 10");
      expect(await pageOverflow(page)).toBeLessThanOrEqual(0);
    });

    await test.step("When the thumb swipes left", async () => {
      await swipeLeft(page);
    });

    await test.step("Then Ready fills the screen and its tab is the selected one", async () => {
      await expect(page.getByRole("tab", { selected: true })).toHaveText("Ready 1");
      await expect(page.locator('.lanes section[data-column="ready"]')).toBeInViewport({ ratio: 0.9 });
      await expect(page.locator('.lanes section[data-column="backlog"]')).not.toBeInViewport();
    });

    await test.step("When the Done tab is tapped, the strip moves to Done", async () => {
      await page.getByRole("tab", { name: "Done 0" }).tap();
      await expect(page.locator('.lanes section[data-column="done"]')).toBeInViewport({ ratio: 0.9 });
      await expect(page.getByRole("tab", { selected: true })).toHaveText("Done 0");
      expect(await pageOverflow(page)).toBeLessThanOrEqual(0);
    });
  });

  test("a tap opens the card's detail sheet, read only, with the ticket body as markdown", async ({ page }) => {
    await test.step("Given the board open at 390×844", async () => {
      await kanbanAt(page, 390, 844);
    });

    await test.step("When E2E-1 is tapped", async () => {
      await page.getByRole("button", { name: "E2E-1: First card" }).tap();
    });

    await test.step("Then the design system's sheet rises from the bottom with the ticket", async () => {
      const sheet = page.getByRole("dialog", { name: "E2E-1: First card" });
      await expect(sheet).toBeVisible();
      await expect(sheet).toHaveClass(/ui-sheet/);
      await expect(sheet.getByRole("heading", { name: "E2E-1", exact: true })).toBeVisible();
      await expect(sheet.getByText("Body text.")).toBeVisible();
      await expect(sheet.getByText("Waits on", { exact: true })).toBeVisible();
      const panel = (await sheet.locator(".ui-sheet-panel").boundingBox()) ?? expect.fail("no panel");
      const view = page.viewportSize() ?? expect.fail("no viewport");
      expect(Math.abs(panel.y + panel.height - view.height)).toBeLessThanOrEqual(1);
      expect(panel.width).toBeGreaterThan(380);
    });

    await test.step("And it offers no way to move the card", async () => {
      const sheet = page.getByRole("dialog", { name: "E2E-1: First card" });
      await expect(sheet.getByRole("button", { name: /move|claim|column/i })).toHaveCount(0);
      await shot(page, "kanban-phone-sheet-390x844.png");
    });

    await test.step("When Close is tapped, the board is back", async () => {
      await page.getByRole("dialog", { name: "E2E-1: First card" }).getByRole("button", { name: "Close" }).tap();
      await expect(page.getByRole("dialog", { name: "E2E-1: First card" })).toHaveCount(0);
      await expect(page.getByRole("button", { name: "E2E-1: First card" })).toBeVisible();
    });
  });

  test("every tap target is 44 px and axe finds nothing serious, with and without the sheet", async ({ page }) => {
    await test.step("Given the board open at 390×844", async () => {
      await kanbanAt(page, 390, 844);
    });

    await test.step("Then the board's controls are tap sized and accessible", async () => {
      expect(await smallTargets(page)).toEqual([]);
      await expectAccessible(page, AXE_EXCLUDE);
    });

    await test.step("And the open sheet's controls are too", async () => {
      await page.getByRole("button", { name: "E2E-1: First card" }).tap();
      const sheet = page.getByRole("dialog", { name: "E2E-1: First card" });
      await expect(sheet.getByText("Body text.")).toBeVisible();
      await settle(page);
      expect(await smallTargets(page)).toEqual([]);
      await expectAccessible(page, AXE_EXCLUDE);
    });
  });

  test("the picker opens as a full-width sheet and lists every board the hub knows", async ({ page }) => {
    await test.step("Given a second board and the board open at 390×844", async () => {
      await seedSecondBoard();
      await kanbanAt(page, 390, 844);
    });

    await test.step("When the picker is tapped", async () => {
      await page.getByRole("button", { name: "Board", exact: true }).tap();
      await expect(page.getByRole("listbox", { name: "Boards" })).toBeVisible();
    });

    await test.step("Then it spans the screen and names both boards", async () => {
      const box = await page.locator(".kanban-tab .picker-pop").boundingBox();
      expect(box?.width).toBeGreaterThan(350);
      const options = page.getByRole("listbox", { name: "Boards" }).getByRole("option");
      await expect(options.filter({ hasText: theSetup().boardName })).toHaveCount(1);
      await expect(options.filter({ hasText: "e2e-second" })).toHaveCount(1);
      await shot(page, "kanban-phone-390x844.png");
    });
  });
});
