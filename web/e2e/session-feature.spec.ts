// session-feature.spec.ts — the feature chips a Sessions row carries under its meta line: a
// session whose member holds two cards of one cluster shows that cluster once, as a real link
// to `#/kanban/<board>/<feature>`, and tapping it opens the Kanban tab with the feature picked
// in the address. A session that holds no card shows no chip line. The chips stand on their own
// above the row's one-tap link, so the row still opens the session and the chips are links of
// their own. Axe finds nothing serious on either width.

import { expect, test } from "@playwright/test";
import fs from "node:fs";
import path from "node:path";
import { expectAccessible } from "./axe.ts";
import { api, openTab, signIn, theSetup } from "./helpers.ts";
import { runDir } from "./paths.ts";

const SESSION = "e2e-carded";
const MEMBER = "swr-holder";
const FEATURE = "swr";

/** The run project's board key, read once the cards are seeded: the same key the chips link to. */
let boardKey = "";

/** The run project's board: the fixture the global setup copied, columns and all. */
const boardDir = (): string => path.join(runDir, "project", theSetup().boardName, ".agents", "kanban");
const projectDir = (): string => path.join(runDir, "project", theSetup().boardName);

/** One ticket in a column, in the front matter the board reads: the fixture's own shape, held
 *  by the member the carded session reports. Two cards, one cluster — the row must show the
 *  cluster once. */
function writeCard(id: string, title: string): void {
  const dir = path.join(boardDir(), "in-progress");
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(
    path.join(dir, `${id}.md`),
    [
      "---",
      `id: ${id}`,
      `title: "${title}"`,
      "type: feature",
      `cluster: ${FEATURE}`,
      "priority: P1",
      `summary: "The card the ${SESSION} session holds."`,
      `claimed_by: ${MEMBER}`,
      "---",
      "",
      `# ${id}`,
      "",
      "Body text.",
      "",
    ].join("\n"),
  );
}

test.beforeAll(async () => {
  writeCard("SWR-1", "First held card");
  writeCard("SWR-2", "Second held card");
  // a session stopped an hour ago (so it sits in Idle, out of the newest row's way) whose
  // member works at the run project's board
  await api("/api/registry/report", {
    session_id: SESSION,
    machine: "e2e-machine",
    project: theSetup().boardName,
    title: "Session with cards",
    member: MEMBER,
    cwd: projectDir(),
    last_event: "Stop",
    last_event_at: new Date(Date.now() - 3_600_000).toISOString(),
  });
  const board = await api<{ boards: { key: string; name: string }[] }>("/api/kanban/boards");
  boardKey = board.boards.find((b) => b.name === theSetup().boardName)?.key ?? "";
  expect(boardKey).not.toBe("");
});

test.afterAll(() => {
  // the board is shared with the run's other specs: leave it as it was found
  for (const id of ["SWR-1", "SWR-2"]) {
    fs.rmSync(path.join(boardDir(), "in-progress", `${id}.md`), { force: true });
  }
});

test("the row shows its member's cluster once, as a link naming the board and the feature", async ({
  page,
}, info) => {
  const phone = info.project.name === "iphone";
  if (phone) await page.setViewportSize({ width: 393, height: 852 });
  await signIn(page);
  await openTab(page, "Sessions");

  // the seeded answer itself: two held cards of the one cluster, on the run's board
  const tasks = await api<{
    board: string;
    cards: { id: string; cluster: string; source: string }[];
  }>(`/api/sessions/${SESSION}/tasks`);
  expect(tasks.board).toBe(boardKey);
  expect(tasks.cards).toHaveLength(2);
  expect(tasks.cards.every((c) => c.cluster === FEATURE && c.source === "held")).toBe(true);

  const row = page.locator(`[data-row='${SESSION}']`);
  await expect(row).toBeVisible();
  // two cards, one chip: the clusters dedupe, and the link carries board and feature
  const chips = row.locator(`a[data-feature-chip='${FEATURE}']`);
  await expect(chips).toHaveCount(1);
  await expect(chips).toHaveAttribute("href", `#/kanban/${boardKey}/${FEATURE}`);

  // 28 px tall at a desk, the 44 px control height on a phone
  const box = await chips.boundingBox();
  expect(box).not.toBeNull();
  if (phone) expect(box!.height).toBeGreaterThanOrEqual(43.5);
  else expect(Math.abs(box!.height - 28)).toBeLessThanOrEqual(1);

  // the seeded session names no member, so it holds no card and shows no chip line at all
  expect(await page.locator("[data-row='e2e-seed'] [data-feature-chips]").count()).toBe(0);
  await expectAccessible(page);
});

test("the desk row still opens the session, and the chip opens the Kanban tab on the feature", async ({
  page,
}, info) => {
  test.skip(info.project.name !== "desktop", "the desk row's one tap is the desktop project's");
  await page.setViewportSize({ width: 1440, height: 900 });
  await signIn(page);
  await openTab(page, "Sessions");
  const row = page.locator(`[data-row='${SESSION}']`);
  await expect(row).toBeVisible();

  // the row's own tap: the middle of the row, away from any chip, opens the session
  await row.click();
  await page.waitForURL(/#\/sessions\/e2e-carded/);

  // the chip's tap: a link of its own, over the row's link
  await openTab(page, "Sessions");
  await expect(row).toBeVisible();
  await row.locator(`a[data-feature-chip='${FEATURE}']`).click();
  await page.waitForURL(new RegExp(`#/kanban/${boardKey}/${FEATURE}$`));
  await expect(page.locator(".kanban-tab")).toBeVisible();
});

test("the phone row's chip opens the Kanban tab on the feature", async ({ page }, info) => {
  test.skip(info.project.name !== "iphone", "the phone chip is the iphone project's");
  await page.setViewportSize({ width: 393, height: 852 });
  await signIn(page);
  await openTab(page, "Sessions");
  const row = page.locator(`[data-row='${SESSION}']`);
  await expect(row).toBeVisible();
  await row.locator(`a[data-feature-chip='${FEATURE}']`).click();
  await page.waitForURL(new RegExp(`#/kanban/${boardKey}/${FEATURE}$`));
  await expect(page.locator(".kanban-tab")).toBeVisible();
});
